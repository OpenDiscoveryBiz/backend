package root

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFrontpageRedirectsToGitHub(t *testing.T) {
	ts := httptest.NewServer(New("https://dk.opendiscovery.biz", 3600).Handler())
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "https://github.com/OpenDiscoveryBiz/root-provider" {
		t.Fatalf("%d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestLookupRejectsInvalidID(t *testing.T) {
	body := get(t, New("https://dk.opendiscovery.biz", 3600), "/.well-known/opendiscovery/X.json")
	if body.status != http.StatusBadRequest || !strings.Contains(body.text, `"invalid_id"`) {
		t.Fatalf("%d %s", body.status, body.text)
	}
}

func TestLookupCountryNotSupported(t *testing.T) {
	body := get(t, New("https://dk.opendiscovery.biz", 3600), "/.well-known/opendiscovery/XX123.json")
	if body.status != http.StatusNotFound || !strings.Contains(body.text, `"country_not_supported"`) {
		t.Fatalf("%d %s", body.status, body.text)
	}
}

func TestLookupRedirectForDK(t *testing.T) {
	body := get(t, New("https://dk.opendiscovery.biz", 3600), "/.well-known/opendiscovery/DK123.json")
	if body.status != http.StatusOK {
		t.Fatalf("%d %s", body.status, body.text)
	}
	for _, want := range []string{`"type":"redirect"`, `"id":"DK"`, `"providers":["https://dk.opendiscovery.biz"]`, `"ttl":3600`} {
		if !strings.Contains(body.text, want) {
			t.Errorf("missing %s in %s", want, body.text)
		}
	}
}

func TestLookupPrettyJSON(t *testing.T) {
	body := get(t, New("https://dk.opendiscovery.biz", 3600), "/.well-known/opendiscovery/DK123.json?pretty=1")
	if body.status != http.StatusOK || !strings.Contains(body.text, "\n") {
		t.Fatalf("%d %s", body.status, body.text)
	}
}

type result struct {
	status int
	text   string
}

func get(t *testing.T, svc *Service, path string) result {
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
	return result{res.StatusCode, string(b)}
}
