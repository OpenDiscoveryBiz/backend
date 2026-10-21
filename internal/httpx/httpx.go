package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

func Listen(handler http.Handler) error {
	addr := ":8000"
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	slog.Info("listening", "addr", addr)
	return srv.ListenAndServe()
}

func WriteJSON(w http.ResponseWriter, status int, pretty bool, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "    ")
	}
	if err := enc.Encode(v); err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	b := bytes.TrimRight(buf.Bytes(), "\n")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// Pretty is true for any non-empty query value except "0" (PHP empty("0")).
func Pretty(r *http.Request) bool {
	v := r.URL.Query().Get("pretty")
	return v != "" && v != "0"
}

func Env(key, fallback string) string {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	return v
}

func IntEnv(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func NoRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func MaxRedirects(n int) func(*http.Request, []*http.Request) error {
	return func(_ *http.Request, via []*http.Request) error {
		if len(via) >= n {
			return fmt.Errorf("stopped after %d redirects", n)
		}
		return nil
	}
}
