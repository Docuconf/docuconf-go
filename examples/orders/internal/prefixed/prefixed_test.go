package prefixed_test

import (
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/internal/prefixed"
)

func TestDocuconfOptions(t *testing.T) {
	cfg, err := docuconf.ParseWithOptions[prefixed.Config](docuconf.Options{
		Environment:    map[string]string{"ORDERS_UPSTREAM": "inventory:8080"},
		TerminationLog: "-",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream != (prefixed.HostPort{Host: "inventory", Port: "8080"}) {
		t.Errorf("Upstream = %+v", cfg.Upstream)
	}
	contract, err := docuconf.Export[prefixed.Config](docuconf.Meta{Name: "orders-api"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contract), "ORDERS_UPSTREAM: {") {
		t.Errorf("export did not use the prefix:\n%s", contract)
	}
}
