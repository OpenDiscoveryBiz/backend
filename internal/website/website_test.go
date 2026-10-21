package website

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPagesLoad(t *testing.T) {
	svc := New(Config{})
	if svc.css == "" || !strings.HasPrefix(svc.css, "/assets/") {
		t.Fatal("vite css missing; run npm run build in web/")
	}
	ts := httptest.NewServer(svc.Handler())
	defer ts.Close()
	for _, path := range []string{"/", "/specifications", "/investigator", "/publisher", "/resolver"} {
		res, body := get(t, ts.URL+path)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %d %s", path, res.StatusCode, body)
		}
		if path == "/" && !strings.Contains(body, svc.css) {
			t.Fatalf("home missing stylesheet: %s", body)
		}
	}
	res, err := http.Get(ts.URL + "/imgs/OpenDiscoveryResolver.png")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("img %d", res.StatusCode)
	}
}

func TestResolverLookupRendersResult(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "DK123" || r.URL.Query().Get("pretty") != "1" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if r.Header.Get("User-Agent") != "OpenDiscoveryWebsite (+https://www.opendiscovery.biz/)" {
			t.Errorf("ua %s", r.Header.Get("User-Agent"))
		}
		_, _ = io.WriteString(w, `{"id":"DK123"}`)
	}))
	defer upstream.Close()
	ts := httptest.NewServer(New(Config{Resolver: upstream.URL}).Handler())
	defer ts.Close()
	res, err := http.PostForm(ts.URL+"/resolver", url.Values{"id": {"DK123"}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(b), "DK123") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
}

func TestResolverUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer upstream.Close()
	ts := httptest.NewServer(New(Config{Resolver: upstream.URL}).Handler())
	defer ts.Close()
	res, err := http.PostForm(ts.URL+"/resolver", url.Values{"id": {"DK123"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("%d", res.StatusCode)
	}
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(b)
}
