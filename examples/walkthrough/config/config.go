// Package config is the orders API's configuration: an ordinary
// caarlos0/env struct with docuconf's tags. The walkthrough exports it to
// ../contract.cue with `docuconf export`.
package config

import (
	"time"

	"github.com/docuconf/docuconf-go"
)

// Config is everything the orders API reads at boot.
type Config struct {
	// Primary Postgres connection string.
	DatabaseURL string `env:"DATABASE_URL,required" secret:"true" schemes:"postgres,postgresql"`

	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error"`

	// Timeout for calls to the payments service.
	PaymentsTimeout time.Duration `env:"PAYMENTS_TIMEOUT" envDefault:"5s" min:"100ms" max:"30s"`

	// Kafka brokers orders are published to.
	Brokers []string `env:"KAFKA_BROKERS,required" minItems:"1"`

	// Fraction of requests traced.
	TraceSampleRatio float64 `env:"TRACE_SAMPLE_RATIO" envDefault:"0.1" min:"0" max:"1"`

	// Certificate the API serves HTTPS with.
	TLS docuconf.TLSKeyPair `file:"serving-tls,required" path:"/etc/orders/tls" reload:"watch" dnsNames:"orders.internal" minRemaining:"720h"`

	// Tax rates by country, loaded at boot.
	TaxRates docuconf.ConfigFile[TaxRates] `file:"tax-rates,required" path:"/etc/orders/tax/rates.yaml"`
}

// TaxRates maps ISO country codes to VAT rates.
type TaxRates struct {
	// Rate used when a country is not listed.
	Default float64 `json:"default" min:"0" max:"1"`
	// Rates by ISO 3166-1 alpha-2 country code.
	Countries map[string]float64 `json:"countries"`
}
