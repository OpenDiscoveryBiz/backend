package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
