package httpx

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIntEnv(t *testing.T) {
	if got := IntEnv("BACKEND_INTENV_UNSET", 7); got != 7 {
		t.Fatalf("unset: %d", got)
	}
	t.Setenv("BACKEND_INTENV", "nope")
	if got := IntEnv("BACKEND_INTENV", 3600); got != 3600 {
		t.Fatalf("bad: %d", got)
	}
	t.Setenv("BACKEND_INTENV", "12")
	if got := IntEnv("BACKEND_INTENV", 3600); got != 12 {
		t.Fatalf("ok: %d", got)
	}
}

func TestPretty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?pretty=1", nil)
	if !Pretty(req) {
		t.Fatal("pretty=1")
	}
	req = httptest.NewRequest(http.MethodGet, "/?pretty=0", nil)
	if Pretty(req) {
		t.Fatal("pretty=0")
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	if Pretty(req) {
		t.Fatal("missing")
	}
}

func TestIsPublic(t *testing.T) {
	cases := []struct {
		ip string
		ok bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2001:4860:4860::8888", true},
		{"127.0.0.1", false},
		{"10.1.1.1", false},
		{"192.168.1.1", false},
		{"172.16.0.1", false},
		{"169.254.169.254", false},
		{"0.0.0.0", false},
		{"::1", false},
		{"fe80::1", false},
		{"100.64.0.1", false},
		{"100.127.255.254", false},
		{"100.128.0.1", true},
		{"fd7a:115c:a1e0::1", false},
		{"64:ff9b::808:808", true},
		{"64:ff9b::a00:1", false},
		{"64:ff9b::7f00:1", false},
		{"64:ff9b::6440:1", false},
		{"64:ff9b:1:a00:0:1::", false},
		{"64:ff9b:1:808:0:808::", true},
	}
	for _, tc := range cases {
		if got := isPublic(net.ParseIP(tc.ip)); got != tc.ok {
			t.Errorf("%s: %v", tc.ip, got)
		}
	}
}

func TestPublicClientRejectsLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	if _, err := PublicClient(time.Second).Get(ts.URL); err == nil {
		t.Fatal("loopback was dialed")
	}
}
