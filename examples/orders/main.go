// Command orders is a tiny HTTP service that loads its configuration with
// docuconf. See README.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/config"
)

func main() {
	// Parse reads the environment and runs every check in one pass. On
	// failure it returns every violation at once, each with a stable code
	// such as missing_required, and never prints a secret's value.
	cfg, err := docuconf.Parse[config.Config]()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"PORT":            cfg.Port,
			"LOG_LEVEL":       cfg.LogLevel,
			"DATABASE_URL":    "***", // secret: true in the contract
			"ALLOWED_ORIGINS": cfg.AllowedOrigins,
			"REQUEST_TIMEOUT": cfg.RequestTimeout.String(),
			"WORKER_COUNT":    cfg.WorkerCount,
		})
	})

	srv := &http.Server{
		Addr:        ":" + strconv.Itoa(cfg.Port),
		Handler:     http.TimeoutHandler(mux, cfg.RequestTimeout, "request timed out"),
		ReadTimeout: cfg.RequestTimeout,
	}
	slog.Info("orders listening", "port", cfg.Port, "workers", cfg.WorkerCount)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
