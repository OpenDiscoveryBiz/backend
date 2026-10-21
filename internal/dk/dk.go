package dk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OpenDiscoveryBiz/backend/internal/cache"
	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

const defaultCVRURL = "http://distribution.virk.dk/cvr-permanent/virksomhed/_search"

type Config struct {
	User   string
	Pass   string
	CVRURL string
	TTL    int
	Client *http.Client
}

type Service struct {
	user   string
	pass   string
	cvrURL string
	ttl    int
	client *http.Client
	cache  *cache.Cache
}

type cvrEntry struct {
	missing bool
	src     map[string]any
}

func New(cfg Config) *Service {
	if cfg.CVRURL == "" {
		cfg.CVRURL = defaultCVRURL
	}
	if cfg.TTL == 0 {
		cfg.TTL = 3600
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: httpx.NoRedirect}
	}
	return &Service{
		user:   cfg.User,
		pass:   cfg.Pass,
		cvrURL: cfg.CVRURL,
		ttl:    cfg.TTL,
		client: cfg.Client,
		cache:  cache.New(),
	}
}

func NewFromEnv() *Service {
	return New(Config{
		User:   httpx.Env("ERST_CVR_USER", ""),
		Pass:   httpx.Env("ERST_CVR_PASS", ""),
		CVRURL: defaultCVRURL,
		TTL:    httpx.IntEnv("DK_TTL", 3600),
	})
}

func (s *Service) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/OpenDiscoveryBiz/backend", http.StatusFound)
	})
	r.Get("/.well-known/opendiscovery/{id}.json", s.lookup)
	return r
}

type apiErr struct {
	Type   string `json:"type"`
	Err    string `json:"error"`
	Detail string `json:"error_detailed,omitempty"`
}

func (s *Service) lookup(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSuffix(chi.URLParam(r, "id"), ".json")
	if raw == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, false, apiErr{Type: "official", Err: "missing_id"})
		return
	}
	id := strings.ToUpper(raw)
	id = nonAlnum.ReplaceAllString(id, "")
	m := dkID.FindStringSubmatch(id)
	if m == nil {
		httpx.WriteJSON(w, http.StatusBadRequest, false, apiErr{Type: "official", Err: "invalid_id"})
		return
	}
	company, err := s.resolve(r.Context(), m[1], m[2])
	if err != nil {
		slog.Error("cvr", "err", err)
		httpx.WriteJSON(w, http.StatusGatewayTimeout, false, apiErr{Type: "official", Err: "upstream_down", Detail: err.Error()})
		return
	}
	if company == nil {
		httpx.WriteJSON(w, http.StatusNotFound, false, apiErr{Type: "official", Err: "no_results"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.Pretty(r), company)
}

func (s *Service) resolve(ctx context.Context, country, localID string) (*company, error) {
	src, err := s.erstData(ctx, localID)
	if err != nil || src == nil {
		return nil, err
	}
	c := &company{
		Type:               "official",
		ID:                 country + localID,
		TTL:                s.ttl,
		VoluntaryProviders: []string{},
		DkStatusTimeline:   []statusRow{},
		DkManagement:       []manager{},
	}
	c.Name = asString(walk(src, "virksomhedMetadata", "nyesteNavn", "navn"))
	if err := translateHomepage(c, src); err != nil {
		return nil, err
	}
	if err := translateStatus(c, src); err != nil {
		return nil, err
	}
	c.MainLineOfBusinessNaceV2 = prefix4(asString(walk(src, "virksomhedMetadata", "nyesteHovedbranche", "branchekode")))
	translateEmployees(c, src)
	if err := translateManagement(c, src); err != nil {
		return nil, err
	}
	translateAddress(c, src)
	rewriteHomepage(c)
	return c, nil
}

func (s *Service) erstData(ctx context.Context, localID string) (map[string]any, error) {
	n, err := strconv.Atoi(localID)
	if err != nil {
		return nil, fmt.Errorf("cvr number: %w", err)
	}
	key := "erstData_" + strconv.Itoa(n)
	if v, ok := s.cache.Get(key); ok {
		hit := v.(cvrEntry)
		if hit.missing {
			return nil, nil
		}
		return hit.src, nil
	}
	body, err := json.Marshal(map[string]any{
		"from": 0,
		"size": 1,
		"query": map[string]any{
			"bool": map[string]any{
				"must": []any{
					map[string]any{
						"term": map[string]any{
							"Vrvirksomhed.cvrNummer": n,
						},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cvrURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.user, s.pass)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cvr status %d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	total, ok := hitTotal(walk(payload, "hits", "total"))
	if !ok {
		return nil, fmt.Errorf("cvr total missing")
	}
	if total == 0 {
		s.cache.Set(key, cvrEntry{missing: true}, 60*time.Second)
		return nil, nil
	}
	if total != 1 {
		return nil, fmt.Errorf("ID matched more than one company")
	}
	hits, _ := walk(payload, "hits", "hits").([]any)
	if len(hits) != 1 {
		return nil, fmt.Errorf("ID matched more than one company")
	}
	src, _ := walk(hits[0], "_source", "Vrvirksomhed").(map[string]any)
	if src == nil {
		return nil, fmt.Errorf("ERST lookup did not return correct company")
	}
	got, ok := asInt(src["cvrNummer"])
	if !ok || got != n {
		return nil, fmt.Errorf("ERST lookup did not return correct company")
	}
	s.cache.Set(key, cvrEntry{src: src}, 60*time.Second)
	return src, nil
}

type company struct {
	Type                     string      `json:"type"`
	ID                       string      `json:"id"`
	TTL                      int         `json:"ttl"`
	VoluntaryProviders       []string    `json:"voluntaryProviders"`
	Name                     string      `json:"name"`
	Homepage                 string      `json:"homepage,omitempty"`
	DkStatusTimeline         []statusRow `json:"dkStatusTimeline"`
	MainLineOfBusinessNaceV2 string      `json:"mainLineOfBusinessNaceV2"`
	DkEmployees              *employees  `json:"dkEmployees,omitempty"`
	DkManagement             []manager   `json:"dkManagement"`
	AddressLines             []string    `json:"addressLines,omitempty"`
}

type statusRow struct {
	Date       string `json:"date"`
	Value      string `json:"value"`
	Translated string `json:"translated"`
}

type employees struct {
	Date string `json:"date"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

type manager struct {
	ID   any    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

var statusText = map[string][2]string{
	"UDEN RETSVIRKNING":                  {"Uden retsvirkning", "Without legal force"},
	"NORMAL":                             {"I normal drift", "In normal operation"},
	"UNDER FRIVILLIG LIKVIDATION":        {"Under frivillig likvidation", "In voluntary liquidation"},
	"UNDER REKONSTRUKTION":               {"Under rekonstruktion", "Under reconstruction"},
	"UNDER KONKURS":                      {"Under konkursbehandling", "Filed for bankruptcy"},
	"UNDER TVANGSOPLØSNING":              {"Under tvangsopløsning", "In compulsory dissolution"},
	"OPLØST EFTER KONKURS":               {"Opløst efter konkurs", "Dissolved after bankruptcy"},
	"TVANGSOPLØST":                       {"Tvangsopløst", "Compulsorily dissolved"},
	"OPLØST EFTER FRIVILLIG LIKVIDATION": {"Opløst efter frivillig likvidation", "Dissolved after voluntary liquidation"},
	"OPLØST EFTER ERKLÆRING":             {"Opløst efter erklæring", "Dissolved after statement"},
	"UNDER REASSUMERING":                 {"Under reassumering", "Under reassumption"},
	"SLETTET":                            {"Slettet", "Deleted"},
	"OPLØST EFTER FUSION":                {"Opløst efter fusion", "Dissolved after merger"},
	"OPLØST EFTER SPALTNING":             {"Opløst efter spaltning", "Dissolved after demerger"},
	"Aktiv":                              {"Aktiv", "Active"},
	"Ophørt":                             {"Ophørt", "Closed down"},
}

func translateHomepage(c *company, src map[string]any) error {
	cur, err := erstCurrent(src["hjemmeside"])
	if err != nil || cur == nil {
		return err
	}
	c.Homepage = asString(cur["kontaktoplysning"])
	return nil
}

func translateStatus(c *company, src map[string]any) error {
	var last map[string]any
	reachedNormal := false
	for _, raw := range asSlice(src["virksomhedsstatus"]) {
		status, _ := raw.(map[string]any)
		if status == nil {
			continue
		}
		last = status
		name := asString(status["status"])
		if name == "NORMAL" {
			reachedNormal = true
		}
		if !reachedNormal {
			continue
		}
		tr, ok := statusText[name]
		if !ok {
			return fmt.Errorf("Unknown status: %s", name)
		}
		periode, _ := status["periode"].(map[string]any)
		c.DkStatusTimeline = append(c.DkStatusTimeline, statusRow{
			Date:       asString(periode["gyldigFra"]),
			Value:      tr[0],
			Translated: tr[1],
		})
	}
	if last != nil {
		return nil
	}
	life, err := erstLast(src["livsforloeb"])
	if err != nil || life == nil {
		return err
	}
	periode, _ := life["periode"].(map[string]any)
	tr := statusText["Aktiv"]
	c.DkStatusTimeline = append(c.DkStatusTimeline, statusRow{
		Date:       asString(periode["gyldigFra"]),
		Value:      tr[0],
		Translated: tr[1],
	})
	if asString(periode["gyldigTil"]) != "" {
		tr = statusText["Ophørt"]
		c.DkStatusTimeline = append(c.DkStatusTimeline, statusRow{
			Date:       asString(periode["gyldigTil"]),
			Value:      tr[0],
			Translated: tr[1],
		})
	}
	return nil
}

func translateEmployees(c *company, src map[string]any) {
	if nym, _ := walk(src, "virksomhedMetadata", "nyesteErstMaanedsbeskaeftigelse").(map[string]any); len(nym) > 0 {
		n, _ := asInt(nym["antalAnsatte"])
		c.DkEmployees = &employees{
			Date: asString(nym["aar"]) + "-" + asString(nym["maaned"]),
			From: n,
			To:   n,
		}
		return
	}
	nym, _ := walk(src, "virksomhedMetadata", "nyesteMaanedsbeskaeftigelse").(map[string]any)
	if len(nym) == 0 {
		return
	}
	parts := strings.Split(asString(nym["intervalKodeAntalAnsatte"]), "_")
	from, to := 0, 0
	if len(parts) > 1 {
		from, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		to, _ = strconv.Atoi(parts[2])
	}
	c.DkEmployees = &employees{
		Date: asString(nym["aar"]) + "-" + asString(nym["maaned"]),
		From: from,
		To:   to,
	}
}

func translateManagement(c *company, src map[string]any) error {
	for _, raw := range asSlice(src["deltagerRelation"]) {
		rel, _ := raw.(map[string]any)
		if rel == nil {
			continue
		}
		person, _ := rel["deltager"].(map[string]any)
		if person == nil || asString(person["enhedstype"]) != "PERSON" {
			continue
		}
		cur, err := erstCurrent(person["navne"])
		if err != nil {
			return err
		}
		if cur == nil {
			return fmt.Errorf("missing name")
		}
		role := ""
		for _, orgRaw := range asSlice(rel["organisationer"]) {
			org, _ := orgRaw.(map[string]any)
			if org == nil {
				continue
			}
			kind := asString(org["hovedtype"])
			if kind != "LEDELSESORGAN" && kind != "FULDT_ANSVARLIG_DELTAGERE" {
				continue
			}
			for _, memRaw := range asSlice(org["medlemsData"]) {
				mem, _ := memRaw.(map[string]any)
				if mem == nil {
					continue
				}
				for _, attrRaw := range asSlice(mem["attributter"]) {
					attr, _ := attrRaw.(map[string]any)
					if attr == nil || asString(attr["type"]) != "FUNKTION" {
						continue
					}
					val, err := erstCurrent(attr["vaerdier"])
					if err != nil {
						return err
					}
					if val == nil {
						continue
					}
					role = ucfirst(asString(val["vaerdi"]))
				}
			}
		}
		if role == "" {
			continue
		}
		c.DkManagement = append(c.DkManagement, manager{
			ID:   person["enhedsNummer"],
			Name: asString(cur["navn"]),
			Role: role,
		})
	}
	return nil
}

func translateAddress(c *company, src map[string]any) {
	nya, _ := walk(src, "virksomhedMetadata", "nyesteBeliggenhedsadresse").(map[string]any)
	if nya == nil {
		return
	}
	// PHP preg_replace ran against a single space, so the line is the concatenated fields.
	line1 := strings.TrimSpace(asString(nya["vejnavn"]) + " " + asString(nya["husnummerFra"]) + asString(nya["bogstavFra"]) + " " + asString(nya["etage"]) + " " + asString(nya["sidedoer"]))
	c.AddressLines = []string{
		line1,
		asString(nya["postnummer"]) + " " + asString(nya["postdistrikt"]),
		asString(nya["landekode"]),
	}
}

func rewriteHomepage(c *company) {
	homepage := strings.TrimSpace(c.Homepage)
	if homepage == "" || strings.Contains(homepage, " ") {
		return
	}
	if !schemeRE.MatchString(homepage) {
		homepage = "http://" + homepage
	}
	u, err := urlParse(homepage)
	if err != nil || u.host == "" || u.host == "localhost" {
		return
	}
	if u.scheme != "http" && u.scheme != "https" {
		u.scheme = "http"
	}
	provider := u.scheme + "://" + u.host
	if u.port != "" {
		provider += ":" + u.port
	}
	c.VoluntaryProviders = append(c.VoluntaryProviders, provider)
}

func erstCurrent(v any) (map[string]any, error) {
	arr := asSlice(v)
	if len(arr) == 0 {
		return nil, nil
	}
	first, _ := arr[0].(map[string]any)
	if first == nil {
		return nil, fmt.Errorf("The array needs items with the period param")
	}
	if _, ok := first["periode"]; !ok {
		return nil, fmt.Errorf("The array needs items with the period param")
	}
	for _, raw := range arr {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		periode, _ := item["periode"].(map[string]any)
		if periode["gyldigTil"] == nil {
			return item, nil
		}
	}
	return nil, nil
}

func erstLast(v any) (map[string]any, error) {
	arr := asSlice(v)
	if len(arr) == 0 {
		return nil, nil
	}
	first, _ := arr[0].(map[string]any)
	if first == nil {
		return nil, fmt.Errorf("The array needs items with the period param")
	}
	if _, ok := first["periode"]; !ok {
		return nil, fmt.Errorf("The array needs items with the period param")
	}
	last, _ := arr[len(arr)-1].(map[string]any)
	return last, nil
}

func walk(v any, keys ...string) any {
	cur := v
	for _, k := range keys {
		m, _ := cur.(map[string]any)
		if m == nil {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
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
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	default:
		return 0, false
	}
}

// ES 6 returns a number. ES 7 wraps it as {"value": n}.
func hitTotal(v any) (int, bool) {
	if m, ok := v.(map[string]any); ok {
		v = m["value"]
	}
	return asInt(v)
}

func prefix4(s string) string {
	if len(s) > 4 {
		return s[:4]
	}
	return s
}

func ucfirst(s string) string {
	s = strings.ToLower(s)
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

type parsedURL struct {
	scheme string
	host   string
	port   string
}

func urlParse(raw string) (parsedURL, error) {
	u, err := parseURL(raw)
	if err != nil {
		return parsedURL{}, err
	}
	return parsedURL{scheme: u.Scheme, host: u.Hostname(), port: u.Port()}, nil
}
