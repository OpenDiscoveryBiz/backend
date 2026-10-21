package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
	"github.com/OpenDiscoveryBiz/backend/internal/website"
)

func main() {
	if err := httpx.Listen(website.NewFromEnv().Handler()); err != nil && err != http.ErrServerClosed {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
