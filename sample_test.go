package docuconf_test

import (
	"errors"
	"net/url"
	"time"

	"github.com/docuconf/docuconf-go"
)

// Gateway declares one input of every kind: each variable type and each
// file type. It is exported to testdata/gateway.golden.cue.
type Gateway struct {
	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error" group:"logging"`

	// Namespace the gateway runs in, for metrics labels.
	PodNamespace string `env:"POD_NAMESPACE,required"`

	// Go runtime soft memory limit, in bytes.
	GoMemLimit int64 `env:"GOMEMLIMIT" min:"1"`

	// Default per-client rate limits.
	RateLimits docuconf.JSON[RateLimits] `env:"RATE_LIMITS" envDefault:"{\"perMinute\":60}"`

	// Password for the partner mTLS keystore.
	PartnerKeystorePassword string `env:"PARTNER_KEYSTORE_PASSWORD,required" secret:"true"`

	// Primary Postgres connection string.
	DatabaseURL string `env:"DATABASE_URL,required" secret:"true" schemes:"postgres,postgresql"`

	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Upstream request timeout.
	//
	// The gateway gives up on an upstream after this long and answers 504.
	// Raise it for slow batch endpoints; keep it below the load balancer's
	// idle timeout.
	//
	// # Choosing a value
	//
	// Measure the upstream's p99 latency first:
	//
	//	histogram_quantile(0.99, upstream_seconds_bucket)
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s" min:"1s" max:"5m"`

	// CORS origins allowed to call the API.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS,required" minItems:"1" examples:"https://app.example.com"`

	// Extra ports to listen on.
	ExtraPorts []uint16 `env:"EXTRA_PORTS" envSeparator:";" maxItems:"4" itemMin:"1"`

	// Stripe API base URL.
	StripeAPIBase *url.URL `env:"STRIPE_API_BASE" envDefault:"https://api.stripe.com" schemes:"https"`

	// Fraction of requests traced.
	TraceSampleRatio float64 `env:"TRACE_SAMPLE_RATIO" envDefault:"0.1" min:"0" max:"1"`

	// Serve the debug endpoints.
	Debug bool `env:"DEBUG" envDefault:"false"`

	// Keys that partners present to call the API.
	PartnerAPIKeys docuconf.KeySet `env:"PARTNER_API_KEYS" envSeparator:" " keyMinLength:"32" keyMaxLength:"128"`

	// Cloud region, such as eu-west-1.
	Region string `env:"REGION" pattern:"^[a-z]{2}-[a-z]+-[0-9]$" minLength:"4" maxLength:"32" deprecated:"Read from the node's topology labels instead"`

	Cache struct {
		// Cache entry lifetime.
		TTL time.Duration `env:"TTL" envDefault:"5m"`
		// Maximum cached entries.
		Size uint `env:"SIZE" envDefault:"1000"`
	} `envPrefix:"CACHE_"`

	Worker Worker `envPrefix:"WORKER_"`

	// Routing table: path prefixes and their upstreams.
	Routes docuconf.ConfigFile[Routes] `file:"routes,required" path:"/etc/gateway/routes/routes.yaml" pathEnv:"ROUTES_FILE" reload:"watch" maxSize:"64Ki"`

	// Certificate the gateway serves HTTPS with.
	ServingTLS docuconf.TLSKeyPair `file:"serving-tls,required" path:"/etc/gateway/tls" reload:"watch" dnsNames:"gateway.internal,api.example.com" keyAlgorithms:"ECDSA,RSA" minRemaining:"720h"`

	// Private CAs the gateway trusts for upstream TLS.
	UpstreamCA docuconf.CABundle `file:"upstream-ca" path:"/etc/gateway/ca/bundle.pem" pathEnv:"SSL_CERT_FILE"`

	// Client certificate for mTLS to the partner API.
	PartnerKeystore docuconf.Keystore `file:"partner-keystore" path:"/etc/gateway/partner/keystore.p12" passwordVar:"PARTNER_KEYSTORE_PASSWORD"`

	// Gateway licence key.
	License docuconf.TextFile `file:"license,required" path:"/etc/gateway/license/license.key" pattern:"^[A-Z0-9]{5}(-[A-Z0-9]{5}){3}\\n?$"`

	GeoIP docuconf.BinaryFile `file:"geoip" path:"/data/geoip/GeoLite2-City.mmdb" maxSize:"128Mi" desc:"GeoIP database for country-based routing"`
}

// Worker is a nested group of variables.
type Worker struct {
	// Number of background workers.
	Count int8 `env:"COUNT" envDefault:"4" min:"1"`
}

// Routes is the gateway's routing table file.
type Routes struct {
	// Routes in match order.
	Routes []Route `json:"routes" minItems:"1"`
}

// Route sends requests under a path prefix to an upstream.
type Route struct {
	// Path prefix the route matches.
	Match string `json:"match" pattern:"^/"`
	// Upstream base URL.
	Upstream string `json:"upstream" pattern:"^https?://"`
	// Per-request timeout, such as 5s.
	Timeout string `json:"timeout,omitempty" pattern:"^([0-9]+(ms|s|m))+$"`
}

// Validate rejects routes that would shadow each other.
func (r Routes) Validate() error {
	seen := map[string]bool{}
	for _, x := range r.Routes {
		if seen[x.Match] {
			return errors.New("duplicate route " + x.Match)
		}
		seen[x.Match] = true
	}
	return nil
}

// RateLimits bounds each client's request rate.
type RateLimits struct {
	// Requests allowed per minute.
	PerMinute int `json:"perMinute" min:"1"`
	// Extra requests allowed in a burst.
	Burst int `json:"burst,omitempty" min:"0"`
}

// longDetails has a doc comment whose details exceed 4000 characters.
type longDetails struct {
	// HTTP listen port.
	//
	// xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
	Port int `env:"PORT"`
}
