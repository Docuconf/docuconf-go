package docuconf_test

import (
	"os"
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

// ExportFixture is the Go declaration of the shared export fixture,
// conformance/export/fixture.yaml (SPEC §11.2 item 3, §12). Its export is
// testdata/conformance-export.cue, which the CLI's tests compare with
// conformance/export/golden.cue through "docuconf conformance export".
type ExportFixture struct {
	// Service name, used in logs and metrics.
	//
	// Lower case, as a DNS label allows.
	AppName string `env:"APP_NAME" envDefault:"orders" minLength:"2" maxLength:"40" pattern:"^[a-z][a-z0-9-]*$" group:"general" examples:"orders|billing" configKey:"App:Name"`

	// Primary Postgres connection string.
	DatabaseURL string `env:"DATABASE_URL,required" secret:"true" schemes:"postgres,postgresql" type:"url" maxLength:"2048" group:"database"`

	// HTTP listen port.
	Port int64 `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Fraction of requests traced.
	TraceRatio float64 `env:"TRACE_RATIO" envDefault:"0.25" min:"0" max:"1"`

	// Serve the debug endpoints.
	Debug bool `env:"DEBUG" envDefault:"false"`

	// Upstream request timeout.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"1m30s" min:"1s" max:"5m"`

	// Minimum log level.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error"`

	// CORS origins allowed to call the API.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envSeparator:";" minItems:"1" maxItems:"5" itemMinLength:"1" itemMaxLength:"255"`

	// Shards this instance owns.
	Shards []int64 `env:"SHARDS" itemMin:"0" itemMax:"1023"`

	// Keys that verify webhook signatures.
	WebhookKeys docuconf.KeySet `env:"WEBHOOK_KEYS" keyMinLength:"32" keyMaxLength:"256"`

	// Per-client rate limits.
	RateLimits docuconf.JSON[FixtureLimits] `env:"RATE_LIMITS" envDefault:"{\"perMinute\":60}" maxLength:"1024"`

	// Port the service used to listen on.
	OldPort int64 `env:"OLD_PORT" deprecated:"Use PORT instead" replacedBy:"PORT"`

	// Password of the partner keystore.
	PartnerPassword string `env:"PARTNER_PASSWORD" secret:"true"`

	// Application settings.
	Settings docuconf.ConfigFile[FixtureSettings] `file:"settings,required" path:"/etc/app/settings/settings.json" pathEnv:"SETTINGS_FILE" reload:"watch" maxSize:"64Ki" group:"general"`

	// Routing rules.
	Rules docuconf.ConfigFile[FixtureSettings] `file:"rules" path:"/etc/app/rules/rules.yaml"`

	// Feature defaults.
	Flags docuconf.ConfigFile[FixtureSettings] `file:"flags" path:"/etc/app/flags/flags.toml"`

	// Certificate the service serves HTTPS with.
	ServingTLS docuconf.TLSKeyPair `file:"serving-tls" path:"/etc/app/tls" dnsNames:"app.example.test,api.example.test" keyAlgorithms:"ECDSA,Ed25519" minRemaining:"720h" requireCA:"true" reload:"watch"`

	// CAs the service trusts.
	Trust docuconf.CABundle `file:"trust" path:"/etc/app/trust/bundle.pem" minCertificates:"2"`

	// Client certificate for the partner API.
	Partner docuconf.Keystore `file:"partner" path:"/etc/app/partner/keystore.p12" format:"pkcs12" passwordVar:"PARTNER_PASSWORD"`

	// Licence key.
	Licence docuconf.TextFile `file:"licence" path:"/etc/app/licence/licence.key" pattern:"^[A-Z0-9-]+\\n?$" minLength:"8" maxLength:"64"`

	// GeoIP database.
	GeoIP docuconf.BinaryFile `file:"geoip" path:"/data/geoip/geoip.mmdb" maxSize:"128Mi" deprecated:"Use geo-db instead" replacedBy:"geo-db"`

	// City-level location database.
	GeoDB docuconf.BinaryFile `file:"geo-db" path:"/data/geo-db/geo.mmdb"`
}

// FixtureLimits is the type of RATE_LIMITS.
type FixtureLimits struct {
	PerMinute int64 `json:"perMinute" min:"1"`
	Burst     int64 `json:"burst,omitempty" min:"0"`
}

// FixtureSettings is the type of the fixture's config files.
type FixtureSettings struct {
	Name     string   `json:"name" minLength:"1"`
	Replicas int64    `json:"replicas" min:"1"`
	Tags     []string `json:"tags,omitempty"`
}

const exportFixtureGolden = "testdata/conformance-export.cue"

// TestExportConformanceFixture keeps the Go SDK's export of the shared
// fixture in testdata. The CLI's TestConformanceExportGoSDK compares it
// with conformance/export/golden.cue as data.
func TestExportConformanceFixture(t *testing.T) {
	out, err := docuconf.Export[ExportFixture](docuconf.Meta{Name: "docuconf-fixture", AppVersion: "1.0.0"})
	require.NoError(t, err)
	if *update {
		require.NoError(t, os.WriteFile(exportFixtureGolden, out, 0o644))
	}
	want, err := os.ReadFile(exportFixtureGolden)
	require.NoError(t, err)
	require.Equal(t, withoutGeneratorVersion(want), withoutGeneratorVersion(out), "run go test -run TestExportConformanceFixture -update to accept")
}

// TestParseTOMLConfigFile checks a declared ConfigFile in the toml format,
// whose format comes from the path's extension.
func TestParseTOMLConfigFile(t *testing.T) {
	type cfg struct {
		Flags docuconf.ConfigFile[FixtureSettings] `file:"flags,required" path:"/etc/app/flags/flags.toml"`
	}
	root := t.TempDir()
	writeFile(t, root+"/etc/app/flags/flags.toml", []byte("name = \"orders\"\nreplicas = 2\ntags = [\"a\"]\n"))
	got, err := docuconf.ParseWithOptions[cfg](docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-"})
	require.NoError(t, err)
	require.Equal(t, FixtureSettings{Name: "orders", Replicas: 2, Tags: []string{"a"}}, got.Flags.Value())

	writeFile(t, root+"/etc/app/flags/flags.toml", []byte("name = \"orders\"\nreplicas = \"two\"\n"))
	_, err = docuconf.ParseWithOptions[cfg](docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-"})
	require.ErrorContains(t, err, "schema_mismatch")
}
