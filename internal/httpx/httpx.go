package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
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
	errc := make(chan error, 1)
	go func() {
		errc <- srv.ListenAndServe()
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// PublicClient dials only global unicast addresses. The dial uses the resolved IP, so a name cannot rebind to a private address after the check.
func PublicClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var chosen net.IP
			for _, ip := range ips {
				if isPublic(ip.IP) {
					chosen = ip.IP
					break
				}
			}
			if chosen == nil {
				return nil, fmt.Errorf("address not public")
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(chosen.String(), port))
		},
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: NoRedirect}
}

var (
	// Tailscale uses this whole carrier-grade NAT range for IPv4.
	cgnat      = mustCIDR("100.64.0.0/10")
	nat64WK    = mustCIDR("64:ff9b::/96")
	nat64Local = mustCIDR("64:ff9b:1::/48")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func isPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// NAT64 is public or not based on the IPv4 it embeds.
	if v4 := nat64IPv4(ip); v4 != nil {
		return isPublic(v4)
	}
	if ip4 := ip.To4(); ip4 != nil && cgnat.Contains(ip4) {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
}

// nat64IPv4 extracts the RFC 6052 IPv4 address from the well-known or local NAT64 prefix.
func nat64IPv4(ip net.IP) net.IP {
	if ip.To4() != nil {
		return nil
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}
	switch {
	case nat64WK.Contains(ip16):
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
	case nat64Local.Contains(ip16):
		return net.IPv4(ip16[6], ip16[7], ip16[9], ip16[10]).To4()
	default:
		return nil
	}
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
