// Package config is the orders service's configuration: an ordinary
// caarlos0/env struct with docuconf's tags. It lives in its own package
// because `docuconf export` imports it, and package main cannot be imported.
package config

import (
	"time"

	"github.com/docuconf/docuconf-go"
)

// Config is everything the orders service reads at boot.
// Doc comments become the descriptions in the contract.
type Config struct {
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error"`

	// Postgres connection string for the orders database.
	DatabaseURL docuconf.Secret `env:"DATABASE_URL,required" schemes:"postgres"`

	// Origins allowed to call the API from a browser.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envDefault:"http://localhost:3000" minItems:"1"`

	// Time limit for handling one request.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s" min:"1s" max:"5m"`

	// Number of background workers processing orders.
	WorkerCount int `env:"WORKER_COUNT" envDefault:"4" min:"1" max:"64"`

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
