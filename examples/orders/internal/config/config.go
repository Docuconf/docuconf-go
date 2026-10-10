// Package config is the orders service's configuration: an ordinary
// caarlos0/env struct with docuconf's tags. It lives in its own package
// because `docuconf export` imports it, and package main cannot be imported.
package config

import (
	"time"

	"github.com/docuconf/docuconf-go"
)

// Config is everything the orders service reads at boot.
// Doc comments become the descriptions in the contract: the first
// paragraph is the description, and any later paragraphs are its details.
type Config struct {
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error"`

	// Postgres connection string for the orders database.
	DatabaseURL docuconf.Secret `env:"DATABASE_URL,required" schemes:"postgres" maxLength:"2048"`

	// Origins allowed to call the API from a browser.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envDefault:"http://localhost:3000" minItems:"1"`

	// Time limit for handling one request.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s" min:"1s" max:"5m"`

	// Number of background workers processing orders.
	//
	// Each worker holds one database connection, so keep it below the
	// database's connection limit divided by the number of replicas.
	// Raise it when the order queue grows faster than it drains.
	WorkerCount int `env:"WORKER_COUNT" envDefault:"4" min:"1" max:"64"`

	// Keys that verify the signature on incoming payment webhooks.
	//
	// A webhook is accepted when it is signed with any key in the set, so
	// the key can be rotated without turning webhooks away. Each key is 32
	// to 256 characters, so an empty or truncated key fails at boot.
	// Without this variable, the service rejects every webhook.
	WebhookKeys docuconf.KeySet `env:"WEBHOOK_KEYS" keyMinLength:"32" keyMaxLength:"256"`

	// Certificate to serve HTTPS with. Without it, the service serves HTTP.
	TLS docuconf.TLSKeyPair `file:"serving-tls" path:"/etc/orders/tls" dnsNames:"orders.example.com" minRemaining:"720h" reload:"watch"`

	// Discount codes accepted at checkout.
	Discounts docuconf.ConfigFile[Discounts] `file:"discounts" path:"/etc/orders/discounts/discounts.yaml"`
}

// Discounts is the content of the discounts file.
type Discounts struct {
	// Percent off for each discount code.
	Codes map[string]int `json:"codes" yaml:"codes"`
}
