package resolver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	status, body := lookup(t, New(Config{ProviderRoot: root.URL}), "/lookup?id=DK123")
	if status != http.StatusOK || !strings.Contains(body, `"name":"Ada"`) {
		t.Fatalf("%d %s", status, body)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
