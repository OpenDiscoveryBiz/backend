package root

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

type Service struct {
	providerDK string
	ttl        int
}

func New(providerDK string, ttl int) *Service {
	if ttl == 0 {
		ttl = 3600
	}
	return &Service{providerDK: providerDK, ttl: ttl}
}

func NewFromEnv() *Service {
	return New(httpx.Env("PROVIDER_DK", "https://dk.opendiscovery.biz"), httpx.IntEnv("ROOT_TTL", 3600))
}

func (s *Service) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/OpenDiscoveryBiz/root-provider", http.StatusFound)
	})
	r.Get("/.well-known/opendiscovery/{id}.json", s.lookup)
	return r
}

type apiErr struct {
	Type   string `json:"type"`
	Err    string `json:"error"`
	Detail string `json:"error_detailed,omitempty"`
}

type redirect struct {
	Type      string   `json:"type"`
	ID        string   `json:"id"`
	Providers []string `json:"providers"`
	TTL       int      `json:"ttl"`
}

var (
	nonAlnum = regexp.MustCompile(`[^A-Z0-9]+`)
	idRE     = regexp.MustCompile(`^([A-Z]{2})([A-Z0-9]+)$`)
)

func (s *Service) lookup(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSuffix(chi.URLParam(r, "id"), ".json")
	if raw == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, false, apiErr{Type: "official", Err: "missing_id"})
		return
	}
	id := nonAlnum.ReplaceAllString(strings.ToUpper(raw), "")
	m := idRE.FindStringSubmatch(id)
	if m == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, false, apiErr{Type: "official", Err: "invalid_id"})
		return
	}
	country := m[1]
	providerEnv := ""
	if country == "DK" {
		providerEnv = s.providerDK
	}
	if providerEnv == "" {
		httpx.WriteJSON(w, http.StatusNotFound, false, apiErr{
			Type:   "official",
			Err:    "country_not_supported",
			Detail: "The country " + country + " is not supported",
		})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.Pretty(r), redirect{
		Type:      "redirect",
		ID:        country,
		Providers: strings.Split(providerEnv, ","),
		TTL:       s.ttl,
	})
}
