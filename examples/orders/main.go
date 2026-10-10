// Command orders is a tiny HTTP service that loads its configuration with
// docuconf. See README.md.
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/internal/config"
	"github.com/docuconf/docuconf-go/examples/orders/internal/webhook"
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
	// Reload status of the watched certificate, for a health check: its
	// generation, and the last rotation that was rejected, by code.
	mux.HandleFunc("GET /reloadz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]docuconf.ReloadStatus{"serving-tls": cfg.TLS.ReloadStatus()})
	})
	mux.HandleFunc("GET /discounts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg.Discounts.Value().Codes)
	})
	// Payment webhooks, signed with any key in WEBHOOK_KEYS (CONFIG.md says
	// how to rotate it).
	mux.HandleFunc("POST /webhooks/payments", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if !webhook.Verify(cfg.WebhookKeys, body, r.Header.Get("X-Signature")) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{
		Addr:        ":" + strconv.Itoa(cfg.Port),
		Handler:     http.TimeoutHandler(mux, cfg.RequestTimeout, "request timed out"),
		ReadTimeout: cfg.RequestTimeout,
	}
	var err error
	if cfg.TLS.Present() {
		// GetCertificate reads the current certificate on every handshake,
		// so a renewed one is served without a restart.
		srv.TLSConfig = &tls.Config{GetCertificate: cfg.TLS.GetCertificate}
		cfg.TLS.OnChange(func(cert *tls.Certificate) {
			slog.Info("serving a renewed certificate", "notAfter", cert.Leaf.NotAfter)
		})
		slog.Info("orders listening with HTTPS", "port", cfg.Port)
		err = srv.ListenAndServeTLS("", "")
	} else {
		slog.Info("orders listening", "port", cfg.Port)
		err = srv.ListenAndServe()
	}
	slog.Error("server stopped", "err", err)
	os.Exit(1)
}
