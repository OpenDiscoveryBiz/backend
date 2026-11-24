package resolver

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OpenDiscoveryBiz/backend/internal/cache"
	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

const (
	lookupBudget = 15 * time.Second
	maxHops      = 5
	maxProviders = 5
	limitBurst   = 10
	limitKeys    = 1024
)

type Config struct {
	ProviderRoot string
	TTLMin       int
	TTLMax       int
	TTLDefault   int
	Client       *http.Client
	PublicClient *http.Client
}

type Service struct {
	roots        string
	ttlMin       int
	ttlMax       int
	ttlDefault   int
	client       *http.Client
	publicClient *http.Client
	cache        *cache.Cache
	limit        *limiter
}

func New(cfg Config) *Service {
	if cfg.TTLMin == 0 {
		cfg.TTLMin = 600
	}
	if cfg.TTLMax == 0 {
		cfg.TTLMax = 43200
	}
	if cfg.TTLDefault == 0 {
		cfg.TTLDefault = 3600
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: httpx.NoRedirect}
	}
	if cfg.PublicClient == nil {
		cfg.PublicClient = httpx.PublicClient(5 * time.Second)
	}
	return &Service{
		roots:        cfg.ProviderRoot,
		ttlMin:       cfg.TTLMin,
		ttlMax:       cfg.TTLMax,
		ttlDefault:   cfg.TTLDefault,
		client:       cfg.Client,
		publicClient: cfg.PublicClient,
		cache:        cache.New(),
		limit:        newLimiter(),
	}
}

func NewFromEnv() *Service {
	return New(Config{
		ProviderRoot: httpx.Env("PROVIDER_ROOT", "https://root.opendiscovery.biz"),
		TTLMin:       httpx.IntEnv("RESOLVER_TTL_MIN", 600),
		TTLMax:       httpx.IntEnv("RESOLVER_TTL_MAX", 43200),
		TTLDefault:   httpx.IntEnv("RESOLVER_TTL_DEFAULT", 3600),
	})
}

func (s *Service) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/OpenDiscoveryBiz/backend", http.StatusFound)
	})
	r.Get("/lookup", s.lookup)
	return r
}

type apiErr struct {
	Err string `json:"error"`
}

type result struct {
	ID        string `json:"id"`
	TTL       int    `json:"ttl"`
	Official  any    `json:"official"`
	Voluntary any    `json:"voluntary"`
}

func (s *Service) lookup(w http.ResponseWriter, r *http.Request) {
	if !s.limit.allow(clientIP(r)) {
		httpx.WriteJSON(w, http.StatusTooManyRequests, false, apiErr{Err: "rate_limited"})
		return
	}
	raw := r.URL.Query().Get("id")
	if raw == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, false, apiErr{Err: "missing_id"})
		return
	}
	id := nonAlnum.ReplaceAllString(strings.ToUpper(raw), "")
	if !idRE.MatchString(id) {
		httpx.WriteJSON(w, http.StatusBadRequest, false, apiErr{Err: "invalid_id"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lookupBudget)
	defer cancel()

	var redirectKeys []string
	for i := len(id) - 1; i > 1; i-- {
		redirectKeys = append(redirectKeys, id[:i]+"_redirect")
	}
	seen := map[string]any{}
	for _, key := range append([]string{id + "_voluntary", id + "_official"}, redirectKeys...) {
		if v, ok := s.cache.Get(key); ok {
			seen[key] = v
		}
	}

	var official map[string]any
	if v, ok := seen[id+"_official"]; ok {
		official, _ = v.(map[string]any)
	} else {
		providers := splitCSV(s.roots)
		// redirectKeys is longest-first, and every hit overwrites, so the shortest cached prefix wins.
		for _, key := range redirectKeys {
			v, ok := seen[key]
			if !ok {
				continue
			}
			m, _ := v.(map[string]any)
			if list, ok := asStrings(m["providers"]); ok {
				providers = list
			}
		}
		data, err := s.follow(ctx, providers, id, "official", true, seen)
		if err != nil {
			slog.Error("official", "id", id, "err", err)
			official = map[string]any{"type": "official", "error": "upstream_down"}
		} else if data != nil && !hasError(data) {
			official = data
			s.cache.Set(id+"_official", official, s.expire(data["ttl"]))
		} else {
			official = data
		}
	}

	var voluntary any
	list, hasProviders := asStrings(nil)
	if official != nil {
		list, hasProviders = asStrings(official["voluntaryProviders"])
	}
	if official != nil && !hasError(official) && hasProviders && len(list) > 0 {
		if v, ok := seen[id+"_voluntary"]; ok {
			voluntary = v
		} else {
			data, err := s.follow(ctx, list, id, "voluntary", false, seen)
			if err != nil || (data != nil && hasError(data)) {
				if err != nil {
					slog.Error("voluntary", "id", id, "err", err)
					voluntary = map[string]any{"type": "voluntary", "error": "upstream_down"}
				} else {
					voluntary = data
				}
				s.cache.Set(id+"_voluntary", voluntary, time.Duration(s.ttlMin)*time.Second)
			} else if data != nil {
				voluntary = data
				s.cache.Set(id+"_voluntary", voluntary, s.expire(data["ttl"]))
			}
		}
	} else {
		voluntary = map[string]any{"type": "voluntary", "error": "official_not_available"}
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Pretty(r), result{
		ID:        id,
		TTL:       s.responseTTL(official, voluntary),
		Official:  official,
		Voluntary: voluntary,
	})
}

func (s *Service) responseTTL(official map[string]any, voluntary any) int {
	if official == nil || hasError(official) {
		return 0
	}
	ttl := int(s.expire(official["ttl"]) / time.Second)
	m, _ := voluntary.(map[string]any)
	if m == nil {
		return ttl
	}
	if !hasError(m) {
		v := int(s.expire(m["ttl"]) / time.Second)
		if v < ttl {
			return v
		}
		return ttl
	}
	if asString(m["error"]) != "official_not_available" && s.ttlMin < ttl {
		return s.ttlMin
	}
	return ttl
}

func (s *Service) follow(ctx context.Context, providers []string, id, want string, cacheRedirect bool, seen map[string]any) (map[string]any, error) {
	public := want == "voluntary"
	lastErr := ""
	for range maxHops {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var data map[string]any
		n := len(providers)
		if n > maxProviders {
			n = maxProviders
		}
		for i := range n {
			got, err := s.fetch(ctx, providers[i], id, public)
			if err != nil {
				lastErr = err.Error()
				data = nil
				continue
			}
			data = got
			break
		}
		if data == nil {
			if want == "official" {
				return nil, fmt.Errorf("Official providers down: %s", lastErr)
			}
			return nil, fmt.Errorf("voluntary providers down: %s", lastErr)
		}
		typ, _ := data["type"].(string)
		if typ == want {
			return data, nil
		}
		if typ != "redirect" {
			if want == "official" {
				return nil, fmt.Errorf("Got unsupported type from official providers")
			}
			return nil, fmt.Errorf("Got unsupported type from voluntary providers")
		}
		next, ok := asStrings(data["providers"])
		if !ok || len(next) < 1 {
			if want == "official" {
				return nil, fmt.Errorf("No providers in redirect from offical provider")
			}
			return nil, fmt.Errorf("No providers in redirect from voluntary provider")
		}
		if cacheRedirect {
			idv := asString(data["id"])
			if idv != "" && idv != "0" {
				key := idv + "_redirect"
				if _, exists := seen[key]; !exists {
					seen[key] = data
					s.cache.Set(key, data, s.expire(data["ttl"]))
				}
			}
		}
		providers = next
	}
	return nil, fmt.Errorf("too many redirects")
}

func (s *Service) fetch(ctx context.Context, host, id string, public bool) (map[string]any, error) {
	host = strings.TrimRight(host, "/")
	endpoint := host + "/.well-known/opendiscovery/" + url.PathEscape(id) + ".json"
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OpenDiscoveryResolver (+https://www.opendiscovery.biz/)")
	client := s.client
	if public {
		client = s.publicClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("Missing ID")
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if hasError(m) {
		return m, nil
	}
	if asString(m["id"]) == "" {
		return nil, fmt.Errorf("Missing ID")
	}
	if !strings.HasPrefix(id, strings.ToUpper(asString(m["id"]))) {
		return nil, fmt.Errorf("Unknown ID returned")
	}
	return m, nil
}

func (s *Service) expire(ttl any) time.Duration {
	n, _ := asInt(ttl)
	if n < 1 {
		n = s.ttlDefault
	} else {
		if n > s.ttlMax {
			n = s.ttlMax
		}
		if n < s.ttlMin {
			n = s.ttlMin
		}
	}
	return time.Duration(n) * time.Second
}

func splitCSV(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func asStrings(v any) ([]string, bool) {
	switch t := v.(type) {
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

func hasError(m map[string]any) bool {
	if m == nil {
		return false
	}
	v, ok := m["error"]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case string:
		return t != "" && t != "0"
	case float64:
		return t != 0
	case bool:
		return t
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// 60 requests per minute, burst 10. The map drops the oldest address past limitKeys.
type limiter struct {
	mu    sync.Mutex
	items map[string]*bucket
	order *list.List
}

type bucket struct {
	key  string
	el   *list.Element
	tok  float64
	last time.Time
}

func newLimiter() *limiter {
	return &limiter{items: map[string]*bucket{}, order: list.New()}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.items[key]
	if ok {
		l.order.MoveToFront(b.el)
		b.tok += now.Sub(b.last).Seconds()
		if b.tok > limitBurst {
			b.tok = limitBurst
		}
	} else {
		if l.order.Len() >= limitKeys {
			old := l.order.Remove(l.order.Back()).(*bucket)
			delete(l.items, old.key)
		}
		b = &bucket{key: key, tok: limitBurst}
		b.el = l.order.PushFront(b)
		l.items[key] = b
	}
	b.last = now
	if b.tok < 1 {
		return false
	}
	b.tok--
	return true
}
