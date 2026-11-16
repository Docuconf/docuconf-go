// Package config is the shop's configuration. One image runs as two
// processes, the web server and the job worker, so the struct declares
// roles, and each input only one role uses names it. It lives in its own
// package because `docuconf export` imports it, and package main cannot be
// imported.
package config

import "github.com/docuconf/docuconf-go"

// Config is everything the shop reads at boot.
type Config struct {
	// The platform sets DOCUCONF_ROLE to web or worker; docuconf declares
	// and checks it.
	_ docuconf.Roles `roles:"web,worker"`
	// The OpenTelemetry SDK's variables: the contract carries them, and
	// Parse checks their values at boot, but sets no field.
	_ docuconf.Fragments `include:"otel@1"`

	// Postgres connection string.
	DatabaseURL docuconf.Secret `env:"DATABASE_URL,required" schemes:"postgres,postgresql"`

	// Port the web server listens on.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535" roles:"web"`

	// Origins the web server accepts browser requests from.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS,required" roles:"web"`

	// Queues the worker takes jobs from, in priority order.
	Queues []string `env:"QUEUES,required" roles:"worker"`

	// Log output format.
	LogFormat string `env:"LOG_FORMAT" envDefault:"text" values:"text,json"`

	// JSON schema the structured logs follow.
	LogSchemaURL string `env:"LOG_SCHEMA_URL" type:"url" schemes:"https" requiredIf:"LOG_FORMAT=json"`

	// Send order webhooks to partners.
	WebhooksEnabled bool `env:"WEBHOOKS_ENABLED" envDefault:"false" roles:"worker"`

	// Key that signs outgoing order webhooks.
	WebhookSigningKey docuconf.TextFile `file:"webhook-signing-key" path:"/etc/shop/webhooks/signing.key" secret:"true" minLength:"32" requiredIf:"WEBHOOKS_ENABLED=true" roles:"worker"`
}
