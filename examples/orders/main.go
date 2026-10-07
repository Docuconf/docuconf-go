// Command orders is a tiny HTTP service that loads its configuration with
// docuconf. See README.md.
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/internal/config"
)

func main() {
	// On bad configuration, ParseOrExit prints every problem and exits 1.
	cfg := docuconf.ParseOrExit[config.Config]()
	slog.Info("config loaded", "config", docuconf.LogValue(cfg)) // secrets print as ***

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(docuconf.Redacted(cfg))
	})
	mux.HandleFunc("GET /discounts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg.Discounts.Value().Codes)
	})

	srv := &http.Server{
		Addr:        ":" + strconv.Itoa(cfg.Port),
		Handler:     http.TimeoutHandler(mux, cfg.RequestTimeout, "request timed out"),
		ReadTimeout: cfg.RequestTimeout,
	}
	var err error
	if cfg.TLS.Present() {
		srv.TLSConfig = &tls.Config{GetCertificate: cfg.TLS.GetCertificate}
		slog.Info("orders listening with HTTPS", "port", cfg.Port)
		err = srv.ListenAndServeTLS("", "")
	} else {
		slog.Info("orders listening", "port", cfg.Port)
		err = srv.ListenAndServe()
	}
	slog.Error("server stopped", "err", err)
	os.Exit(1)
}
