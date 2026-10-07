package config_test

import (
	"errors"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/internal/config"
)

func TestConfig(t *testing.T) {
	_, err := docuconf.ParseWithOptions[config.Config](docuconf.Options{
		// Read these variables instead of the process environment.
		Environment:    map[string]string{"DATABASE_URL": "postgres://u:p@db/orders", "PORT": "0"},
		TerminationLog: "-", // do not write /dev/termination-log
	})
	var verr *docuconf.ValidationError
	if !errors.As(err, &verr) || !verr.Has(docuconf.CodeOutOfRange) {
		t.Fatalf("want out_of_range for PORT=0, got %v", err)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := docuconf.ParseWithOptions[config.Config](docuconf.Options{
		Environment:    map[string]string{"DATABASE_URL": "postgres://u:p@db/orders"},
		TerminationLog: "-",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.WorkerCount != 4 || cfg.TLS.Present() {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.DatabaseURL.Reveal() != "postgres://u:p@db/orders" {
		t.Error("DatabaseURL lost its value")
	}
}
