package dk

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFrontpageRedirectsToGitHub(t *testing.T) {
	ts := httptest.NewServer(New(Config{TTL: 3600}).Handler())
	defer ts.Close()
	res := getNoFollow(t, ts.URL+"/")
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status %d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "https://github.com/OpenDiscoveryBiz/backend" {
		t.Fatalf("location %s", loc)
	}
}

func TestLookupRejectsInvalidDKID(t *testing.T) {
	ts := httptest.NewServer(New(Config{TTL: 3600}).Handler())
	defer ts.Close()
	res, body := get(t, ts.URL+"/.well-known/opendiscovery/DKABC.json")
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, `"invalid_id"`) {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}

func TestLookupNoResultsIsCached(t *testing.T) {
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hits":{"total":0,"hits":[]}}`)
	}))
	defer upstream.Close()
	svc := New(Config{User: "user", Pass: "pass", CVRURL: upstream.URL, TTL: 3600})
	ts := httptest.NewServer(svc.Handler())
	defer ts.Close()
	for range 2 {
		res, body := get(t, ts.URL+"/.well-known/opendiscovery/DK12345678.json")
		if res.StatusCode != http.StatusNotFound || !strings.Contains(body, `"no_results"`) {
			t.Fatalf("%d %s", res.StatusCode, body)
		}
	}
	if hits != 1 {
		t.Fatalf("upstream hits %d", hits)
	}
}

func TestLookupReturnsCompanyFromCVR(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "user" || pass != "pass" {
			t.Errorf("auth %q %q ok %v", user, pass, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"hits":{"total":1,"hits":[{"_source":{"Vrvirksomhed":{
				"cvrNummer":12345678,
				"virksomhedMetadata":{
					"nyesteNavn":{"navn":"Test ApS"},
					"nyesteHovedbranche":{"branchekode":"620100"},
					"nyesteErstMaanedsbeskaeftigelse":{"aar":2024,"maaned":3,"antalAnsatte":5},
					"nyesteBeliggenhedsadresse":{
						"vejnavn":"Testvej","husnummerFra":"1","bogstavFra":"","etage":"","sidedoer":"",
						"postnummer":"1000","postdistrikt":"København K","landekode":"DK"
					}
				},
				"hjemmeside":[{"kontaktoplysning":"example.com","periode":{"gyldigFra":"2020-01-01","gyldigTil":null}}],
				"virksomhedsstatus":[{"status":"NORMAL","periode":{"gyldigFra":"2020-01-01"}}],
				"livsforloeb":[],
				"deltagerRelation":[{
					"deltager":{
						"enhedstype":"PERSON",
						"enhedsNummer":99,
						"navne":[{"navn":"Ada","periode":{"gyldigFra":"2020-01-01","gyldigTil":null}}]
					},
					"organisationer":[{
						"hovedtype":"LEDELSESORGAN",
						"medlemsData":[{"attributter":[{
							"type":"FUNKTION",
							"vaerdier":[{"vaerdi":"DIREKTØR","periode":{"gyldigFra":"2020-01-01","gyldigTil":null}}]
						}]}]
					}]
				}]
			}}}]}
		}`)
	}))
	defer upstream.Close()
	ts := httptest.NewServer(New(Config{User: "user", Pass: "pass", CVRURL: upstream.URL, TTL: 3600}).Handler())
	defer ts.Close()
	res, body := get(t, ts.URL+"/.well-known/opendiscovery/dk12345678.json")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	for _, want := range []string{
		`"type":"official"`,
		`"id":"DK12345678"`,
		`"name":"Test ApS"`,
		`"mainLineOfBusinessNaceV2":"6201"`,
		`"date":"2024-3"`,
		`"from":5`,
		`"homepage":"example.com"`,
		`"http://example.com"`,
		`"Testvej 1"`,
		`"1000 København K"`,
		`"I normal drift"`,
		`"Direktør"`,
		`"voluntaryProviders":["http://example.com"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestLookupIntervalEmployees(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"hits":{"total":{"value":1},"hits":[{"_source":{"Vrvirksomhed":{
				"cvrNummer":12345678,
				"virksomhedMetadata":{
					"nyesteNavn":{"navn":"Interval ApS"},
					"nyesteHovedbranche":{"branchekode":"62"},
					"nyesteMaanedsbeskaeftigelse":{"aar":"2020","maaned":"01","intervalKodeAntalAnsatte":"ANTAL_10_19"}
				},
				"hjemmeside":[],
				"virksomhedsstatus":[],
				"livsforloeb":[{"periode":{"gyldigFra":"2019-01-01","gyldigTil":"2021-05-01"}}],
				"deltagerRelation":[]
			}}}]}
		}`)
	}))
	defer upstream.Close()
	ts := httptest.NewServer(New(Config{CVRURL: upstream.URL, TTL: 3600}).Handler())
	defer ts.Close()
	res, body := get(t, ts.URL+"/.well-known/opendiscovery/DK12345678.json")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	for _, want := range []string{`"from":10`, `"to":19`, `"date":"2020-01"`, `"Aktiv"`, `"Ophørt"`, `"mainLineOfBusinessNaceV2":"62"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestUnknownStatusIsUpstreamDown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"hits":{"total":1,"hits":[{"_source":{"Vrvirksomhed":{
				"cvrNummer":12345678,
				"virksomhedMetadata":{"nyesteNavn":{"navn":"X"},"nyesteHovedbranche":{"branchekode":"620100"}},
				"hjemmeside":[],
				"virksomhedsstatus":[
					{"status":"NORMAL","periode":{"gyldigFra":"2020-01-01"}},
					{"status":"WAT","periode":{"gyldigFra":"2021-01-01"}}
				],
				"livsforloeb":[],
				"deltagerRelation":[]
			}}}]}
		}`)
	}))
	defer upstream.Close()
	ts := httptest.NewServer(New(Config{CVRURL: upstream.URL, TTL: 3600}).Handler())
	defer ts.Close()
	res, body := get(t, ts.URL+"/.well-known/opendiscovery/DK12345678.json")
	if res.StatusCode != http.StatusGatewayTimeout || !strings.Contains(body, `"upstream_down"`) || !strings.Contains(body, "Unknown status: WAT") {
		t.Fatalf("%d %s", res.StatusCode, body)
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

func getNoFollow(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}
