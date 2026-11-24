package resolver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

func TestFrontpageRedirectsToGitHub(t *testing.T) {
	ts := httptest.NewServer(New(Config{ProviderRoot: "https://root.opendiscovery.biz"}).Handler())
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "https://github.com/OpenDiscoveryBiz/backend" {
		t.Fatalf("%d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestLookupRequiresID(t *testing.T) {
	status, body := lookup(t, New(Config{ProviderRoot: "https://root.example.test"}), "/lookup")
	if status != http.StatusBadRequest || !strings.Contains(body, `"missing_id"`) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestLookupRejectsInvalidID(t *testing.T) {
	status, body := lookup(t, New(Config{ProviderRoot: "https://root.example.test"}), "/lookup?id=X")
	if status != http.StatusBadRequest || !strings.Contains(body, `"invalid_id"`) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestLookupFollowsRedirectProviderChain(t *testing.T) {
	dk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "OpenDiscoveryResolver (+https://www.opendiscovery.biz/)" {
			t.Errorf("ua %s", r.Header.Get("User-Agent"))
		}
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{},
		})
	}))
	defer dk.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":      "redirect",
			"id":        "DK",
			"providers": []string{dk.URL},
			"ttl":       3600,
		})
	}))
	defer root.Close()

	status, body := lookup(t, New(Config{ProviderRoot: root.URL}), "/lookup?id=DK123")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	for _, want := range []string{`"id":"DK123"`, `"type":"official"`, `"error":"official_not_available"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestLookupFollowsVoluntaryProvider(t *testing.T) {
	voluntary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"type": "voluntary", "id": "DK123", "ttl": 3600, "name": "Ada"})
	}))
	defer voluntary.Close()
	dk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{voluntary.URL},
		})
	}))
	defer dk.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"type": "redirect", "id": "DK", "providers": []string{dk.URL}, "ttl": 3600})
	}))
	defer root.Close()
	status, body := lookup(t, New(Config{ProviderRoot: root.URL, PublicClient: openClient()}), "/lookup?id=DK123")
	if status != http.StatusOK || !strings.Contains(body, `"name":"Ada"`) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestVoluntaryPrivateAddressIsRejected(t *testing.T) {
	var hits int
	voluntary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeJSON(w, map[string]any{"type": "voluntary", "id": "DK123", "secret": "leak"})
	}))
	defer voluntary.Close()
	dk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{voluntary.URL},
		})
	}))
	defer dk.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"type": "redirect", "id": "DK", "providers": []string{dk.URL}, "ttl": 3600})
	}))
	defer root.Close()
	status, body := lookup(t, New(Config{ProviderRoot: root.URL}), "/lookup?id=DK123")
	if hits != 0 || status != http.StatusOK || strings.Contains(body, "leak") || !strings.Contains(body, `"upstream_down"`) || strings.Contains(body, "error_detailed") {
		t.Fatalf("hits %d %d %s", hits, status, body)
	}
}

func TestVoluntaryFailureIsCached(t *testing.T) {
	var hits int
	voluntary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer voluntary.Close()
	dk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{voluntary.URL},
		})
	}))
	defer dk.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"type": "redirect", "id": "DK", "providers": []string{dk.URL}, "ttl": 3600})
	}))
	defer root.Close()
	svc := New(Config{ProviderRoot: root.URL, PublicClient: openClient()})
	for range 2 {
		status, body := lookup(t, svc, "/lookup?id=DK123")
		if status != http.StatusOK || !strings.Contains(body, `"upstream_down"`) || strings.Contains(body, "error_detailed") {
			t.Fatalf("%d %s", status, body)
		}
	}
	if hits != 1 {
		t.Fatalf("voluntary hits %d", hits)
	}
}

func TestOfficialErrorIsNotCached(t *testing.T) {
	var hits int
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"type": "official", "error": "no_results", "id": "DK123"})
	}))
	defer root.Close()
	svc := New(Config{ProviderRoot: root.URL})
	for range 2 {
		lookup(t, svc, "/lookup?id=DK123")
	}
	if hits != 2 {
		t.Fatalf("official hits %d", hits)
	}
}

func TestSmallTTLIsRaised(t *testing.T) {
	dk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                1,
			"voluntaryProviders": []string{},
		})
	}))
	defer dk.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"type": "redirect", "id": "DK", "providers": []string{dk.URL}, "ttl": 1})
	}))
	defer root.Close()
	status, body := lookup(t, New(Config{ProviderRoot: root.URL, TTLMin: 600}), "/lookup?id=DK123")
	if status != http.StatusOK || !strings.Contains(body, `"ttl":600`) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestLookupUsesThirdProvider(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{},
		})
	}))
	defer good.Close()
	bad := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "down", http.StatusBadGateway)
		}))
	}
	bad1 := bad()
	defer bad1.Close()
	bad2 := bad()
	defer bad2.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":      "redirect",
			"id":        "DK",
			"providers": []string{bad1.URL, bad2.URL, good.URL},
			"ttl":       3600,
		})
	}))
	defer root.Close()
	status, body := lookup(t, New(Config{ProviderRoot: root.URL}), "/lookup?id=DK123")
	if status != http.StatusOK || !strings.Contains(body, `"type":"official"`) || strings.Contains(body, `"upstream_down"`) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestTooManyRedirects(t *testing.T) {
	var hits int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeJSON(w, map[string]any{
			"type":      "redirect",
			"id":        "DK",
			"providers": []string{srv.URL},
			"ttl":       3600,
		})
	}))
	defer srv.Close()
	status, body := lookup(t, New(Config{ProviderRoot: srv.URL}), "/lookup?id=DK123")
	if status != http.StatusOK || hits != 5 || !strings.Contains(body, `"upstream_down"`) || strings.Contains(body, `"official":null`) {
		t.Fatalf("hits %d %d %s", hits, status, body)
	}
}

func TestLookupRateLimit(t *testing.T) {
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{},
		})
	}))
	defer root.Close()
	ts := httptest.NewServer(New(Config{ProviderRoot: root.URL}).Handler())
	defer ts.Close()
	var last int
	var body string
	for range 11 {
		res, err := http.Get(ts.URL + "/lookup?id=DK123")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		last = res.StatusCode
		body = string(b)
	}
	if last != http.StatusTooManyRequests || !strings.Contains(body, `"rate_limited"`) {
		t.Fatalf("%d %s", last, body)
	}
}

func TestLookupFailsOverToSecondProvider(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":               "official",
			"id":                 "DK123",
			"ttl":                3600,
			"voluntaryProviders": []string{},
		})
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	root := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"type":      "redirect",
			"id":        "DK",
			"providers": []string{bad.URL, good.URL},
			"ttl":       3600,
		})
	}))
	defer root.Close()
	status, body := lookup(t, New(Config{ProviderRoot: root.URL}), "/lookup?id=DK123")
	if status != http.StatusOK || !strings.Contains(body, `"type":"official"`) {
		t.Fatalf("%d %s", status, body)
	}
}

func lookup(t *testing.T, svc *Service, path string) (int, string) {
	t.Helper()
	ts := httptest.NewServer(svc.Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

func TestUnsupportedScheme(t *testing.T) {
	status, body := lookup(t, New(Config{ProviderRoot: "ftp://example.com"}), "/lookup?id=DK123")
	if status != http.StatusOK || !strings.Contains(body, `"upstream_down"`) || strings.Contains(body, "error_detailed") {
		t.Fatalf("%d %s", status, body)
	}
}

func openClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: httpx.NoRedirect}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
