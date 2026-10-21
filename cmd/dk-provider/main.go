package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/OpenDiscoveryBiz/backend/internal/dk"
	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

func main() {
	if err := httpx.Listen(dk.NewFromEnv().Handler()); err != nil && err != http.ErrServerClosed {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
