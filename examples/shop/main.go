// Command shop is a tiny program that loads its configuration with
// docuconf, as the web server or the job worker, prints what it would run
// and exits. See README.md.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/shop/internal/config"
)

func main() {
	// On bad configuration, ParseOrExit prints every problem and exits 1.
	cfg := docuconf.ParseOrExit[config.Config]()
	// Parse has checked DOCUCONF_ROLE: it is web or worker.
	fmt.Println(summary(os.Getenv(docuconf.EnvRole), cfg))
}

// summary is the one line the shop prints for its role, from the typed
// config.
func summary(role string, cfg config.Config) string {
	if role == "web" {
		return fmt.Sprintf("shop web: port %d, origins %s", cfg.Port, strings.Join(cfg.AllowedOrigins, ","))
	}
	webhooks := "off"
	if cfg.WebhooksEnabled {
		webhooks = "on"
	}
	return fmt.Sprintf("shop worker: queues %s, webhooks %s", strings.Join(cfg.Queues, ","), webhooks)
}
