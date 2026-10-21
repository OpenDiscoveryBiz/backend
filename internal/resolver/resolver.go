package resolver

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OpenDiscoveryBiz/backend/internal/cache"
	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

type Config struct {
	ProviderRoot string
	TTLMin       int
	TTLMax       int
	TTLDefault   int
	Client       *http.Client
}

type Service struct {
	roots      string
	ttlMin     int
	ttlMax     int
	ttlDefault int
	client     *http.Client
	cache      *cache.Cache
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
	return &Service{
		roots:      cfg.ProviderRoot,
		ttlMin:     cfg.TTLMin,
		ttlMax:     cfg.TTLMax,
		ttlDefault: cfg.TTLDefault,
		client:     cfg.Client,
		cache:      cache.New(),
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
		data, err := s.follow(providers, id, "official", true, seen)
		if err != nil {
			official = map[string]any{
				"type":           "official",
				"error":          "upstream_down",
				"error_detailed": err.Error(),
			}
		} else if data != nil {
			official = data
			s.cache.Set(id+"_official", official, s.expire(data["ttl"]))
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
			data, err := s.follow(list, id, "voluntary", false, seen)
			if err != nil {
				voluntary = map[string]any{
					"type":           "voluntary",
					"error":          "upstream_down",
					"error_detailed": err.Error(),
				}
			} else {
				voluntary = data
			}
			if voluntary != nil {
				ttl := any(nil)
				if m, ok := voluntary.(map[string]any); ok {
					ttl = m["ttl"]
				}
				s.cache.Set(id+"_voluntary", voluntary, s.expire(ttl))
			}
		}
	} else {
		voluntary = map[string]any{"type": "voluntary", "error": "official_not_available"}
	}

	httpx.WriteJSON(w, http.StatusOK, httpx.Pretty(r), result{
		ID:        id,
		TTL:       3600,
		Official:  official,
		Voluntary: voluntary,
	})
}

func (s *Service) follow(providers []string, id, want string, cacheRedirect bool, seen map[string]any) (map[string]any, error) {
	lastErr := ""
	for range 5 {
		var data map[string]any
		for i := 0; i < 2 && i < len(providers); i++ {
			got, err := s.fetch(providers[i], id)
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
			return nil, fmt.Errorf("voluntary providers down")
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
	return nil, nil
}

func (s *Service) fetch(host, id string) (map[string]any, error) {
	host = strings.TrimRight(host, "/")
	endpoint := host + "/.well-known/opendiscovery/" + url.PathEscape(id) + ".json"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OpenDiscoveryResolver (+https://www.opendiscovery.biz/)")
	resp, err := s.client.Do(req)
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
