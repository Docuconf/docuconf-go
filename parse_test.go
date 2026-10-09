package docuconf_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

const (
	dbPassword       = "hunter2-very-secret"
	keystorePassword = "s3cret-keystore-pass"
)

// fixture is a valid environment and file tree for Gateway.
type fixture struct {
	t    *testing.T
	root string
	ca   *testCA
	env  map[string]string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, root: t.TempDir(), ca: newCA(t)}
	f.env = map[string]string{
		"POD_NAMESPACE":             "payments",
		"PARTNER_KEYSTORE_PASSWORD": keystorePassword,
		"DATABASE_URL":              "postgres://app:" + dbPassword + "@db.internal/payments",
		"ALLOWED_ORIGINS":           "https://app.example.com,https://admin.example.com",
		"RATE_LIMITS":               `{"perMinute":600,"burst":50}`,
		"EXTRA_PORTS":               "9090;9091",
		"DEBUG":                     "TRUE",
		"HOSTNAME":                  "ignored-undeclared",
	}
	cert, key := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "*.example.com"}})
	writeTLS(t, f.path("/etc/gateway/tls"), f.ca, cert, key)
	f.write("/etc/gateway/routes/routes.yaml", "routes:\n  - match: /billing\n    upstream: http://billing.svc:8080\n    timeout: 5s\n")
	f.write("/etc/gateway/ca/bundle.pem", string(certPEM(f.ca.cert)))
	writeFile(t, f.path("/etc/gateway/partner/keystore.p12"), keystore(t, f.ca, keystorePassword))
	f.write("/etc/gateway/license/license.key", "ABCDE-12345-FGHIJ-67890\n")
	f.write("/data/geoip/GeoLite2-City.mmdb", "\x00binary")
	return f
}

func (f *fixture) path(p string) string { return filepath.Join(f.root, p) }

func (f *fixture) write(p, content string) { writeFile(f.t, f.path(p), []byte(content)) }

func (f *fixture) opts() docuconf.Options {
	return docuconf.Options{
		Environment:    f.env,
		FileRoot:       f.root,
		TerminationLog: "-",
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (f *fixture) parse() (Gateway, *docuconf.ValidationError) {
	f.t.Helper()
	cfg, err := docuconf.ParseWithOptions[Gateway](f.opts())
	if err == nil {
		return cfg, nil
	}
	var verr *docuconf.ValidationError
	require.True(f.t, errors.As(err, &verr), "unexpected error type: %v", err)
	return cfg, verr
}

// requireViolation asserts that err has a violation for input with code,
// whose message contains want.
func requireViolation(t *testing.T, err *docuconf.ValidationError, input string, code docuconf.Code, want string) {
	t.Helper()
	require.NotNil(t, err, "expected violations")
	for _, v := range err.Violations {
		if v.Input == input && v.Code == code && strings.Contains(v.Message, want) {
			return
		}
	}
	t.Fatalf("no %s violation for %s containing %q in:\n%v", code, input, want, err)
}

func TestParseValid(t *testing.T) {
	f := newFixture(t)
	cfg, verr := f.parse()
	require.Nil(t, verr)

	require.Equal(t, "payments", cfg.PodNamespace)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, 30*time.Second, cfg.RequestTimeout)
	require.Equal(t, "info", cfg.LogLevel)
	require.True(t, cfg.Debug)
	require.Equal(t, []string{"https://app.example.com", "https://admin.example.com"}, cfg.AllowedOrigins)
	require.Equal(t, []uint16{9090, 9091}, cfg.ExtraPorts)
	require.Equal(t, "api.stripe.com", cfg.StripeAPIBase.Host)
	require.Equal(t, 600, cfg.RateLimits.Value.PerMinute)
	require.Equal(t, 5*time.Minute, cfg.Cache.TTL)
	require.Equal(t, int8(4), cfg.Worker.Count)

	require.Equal(t, "/billing", cfg.Routes.Value().Routes[0].Match)
	require.Equal(t, "ABCDE-12345-FGHIJ-67890\n", cfg.License.Content())
	require.True(t, cfg.GeoIP.Present())
	data, err := cfg.GeoIP.ReadAll()
	require.NoError(t, err)
	require.Equal(t, "\x00binary", string(data))
	pool, err := cfg.UpstreamCA.Load()
	require.NoError(t, err)
	require.NotNil(t, pool)
	require.Equal(t, "client.example.com", cfg.PartnerKeystore.Certificate().Leaf.DNSNames[0])
	require.Len(t, cfg.PartnerKeystore.CACertificates(), 1)

	// The key pair serves a real handshake through tls.Config.GetCertificate.
	roots := x509.NewCertPool()
	roots.AddCert(f.ca.cert)
	handshake(t, &tls.Config{GetCertificate: cfg.ServingTLS.GetCertificate},
		&tls.Config{RootCAs: roots, ServerName: "api.example.com"})
	require.NotNil(t, cfg.ServingTLS.CAPool())
}

func handshake(t *testing.T, server, client *tls.Config) {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	errc := make(chan error, 1)
	go func() { errc <- tls.Server(a, server).Handshake() }()
	require.NoError(t, tls.Client(b, client).Handshake())
	require.NoError(t, <-errc)
}

func TestParseOptionalFilesAbsent(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.RemoveAll(f.path("/etc/gateway/ca")))
	require.NoError(t, os.RemoveAll(f.path("/data/geoip")))
	cfg, verr := f.parse()
	require.Nil(t, verr)
	require.False(t, cfg.UpstreamCA.Present())
	require.False(t, cfg.GeoIP.Present())
}

func TestParseCertificates(t *testing.T) {
	dir := "/etc/gateway/tls"
	cases := []struct {
		name  string
		setup func(f *fixture)
		code  docuconf.Code
		want  string
	}{
		{"expired", func(f *fixture) {
			c, k := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "api.example.com"},
				notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-time.Hour)})
			writeTLS(t, f.path(dir), f.ca, c, k)
		}, docuconf.CodeCertificateInvalid, "certificate expired at"},
		{"expiring", func(f *fixture) {
			c, k := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "api.example.com"},
				notAfter: time.Now().Add(10 * 24 * time.Hour)})
			writeTLS(t, f.path(dir), f.ca, c, k)
		}, docuconf.CodeCertificateExpiring, "less than minRemaining 720h"},
		{"dns mismatch", func(f *fixture) {
			c, k := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "*.example.org"}})
			writeTLS(t, f.path(dir), f.ca, c, k)
		}, docuconf.CodeCertificateNameMismatch, "certificate does not cover api.example.com"},
		{"key mismatch", func(f *fixture) {
			c, _ := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "api.example.com"}})
			_, other := f.ca.issue(t, leafOpts{})
			writeTLS(t, f.path(dir), f.ca, c, other)
		}, docuconf.CodeKeyMismatch, "tls.key does not match"},
		{"key algorithm", func(f *fixture) {
			c, k := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "api.example.com"}, rsa: true})
			writeTLS(t, f.path(dir), f.ca, c, k)
			// RSA is allowed by Gateway; check a stricter declaration below.
		}, "", ""},
		{"missing key", func(f *fixture) {
			require.NoError(t, os.Remove(f.path(dir+"/tls.key")))
		}, docuconf.CodeFileMissing, "tls.key does not exist"},
		{"garbage certificate", func(f *fixture) {
			f.write(dir+"/tls.crt", "not a certificate")
		}, docuconf.CodeFileMalformed, "tls.crt holds no PEM certificate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			_, verr := f.parse()
			if c.code == "" {
				require.Nil(t, verr)
				return
			}
			requireViolation(t, verr, "serving-tls", c.code, c.want)
		})
	}
}

func TestParseTLSRequireCAAndAlgorithm(t *testing.T) {
	type strict struct {
		// Certificate for mTLS.
		TLS docuconf.TLSKeyPair `file:"mtls,required" path:"/etc/app/tls" keyAlgorithms:"Ed25519" requireCA:"true"`
	}
	root := t.TempDir()
	ca, other := newCA(t), newCA(t)
	c, k := ca.issue(t, leafOpts{dnsNames: []string{"app"}})
	writeTLS(t, filepath.Join(root, "etc/app/tls"), other, c, k) // ca.crt is the wrong CA
	_, err := docuconf.ParseWithOptions[strict](docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-"})
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "%v", err)
	requireViolation(t, verr, "mtls", docuconf.CodeCertificateInvalid, "key algorithm ECDSA is not one of Ed25519")
	requireViolation(t, verr, "mtls", docuconf.CodeCertificateInvalid, "") // and the chain
	require.Contains(t, verr.Error(), "does not chain to ca.crt")
}

func TestParseFiles(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		input string
		code  docuconf.Code
		want  string
	}{
		{"missing required file", func(f *fixture) {
			require.NoError(t, os.Remove(f.path("/etc/gateway/license/license.key")))
		}, "license", docuconf.CodeFileMissing, "license.key does not exist"},
		{"text pattern", func(f *fixture) {
			f.write("/etc/gateway/license/license.key", "not-a-licence")
		}, "license", docuconf.CodePatternMismatch, "does not match pattern"},
		{"malformed yaml", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes: [unclosed\n")
		}, "routes", docuconf.CodeFileMalformed, "is not valid YAML"},
		{"schema: missing property", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes:\n  - match: /a\n")
		}, "routes", docuconf.CodeSchemaMismatch, `at routes[0]: missing required property "upstream"`},
		{"schema: unknown property", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes:\n  - match: /a\n    upstream: http://a\n    retries: 3\n")
		}, "routes", docuconf.CodeSchemaMismatch, `property "retries" is not allowed`},
		{"schema: pattern", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes:\n  - match: a\n    upstream: http://a\n")
		}, "routes", docuconf.CodeSchemaMismatch, "at routes[0].match: does not match pattern ^/"},
		{"schema: minItems", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes: []\n")
		}, "routes", docuconf.CodeSchemaMismatch, "has 0 items, below minItems 1"},
		{"Validate method", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes:\n  - {match: /a, upstream: http://a}\n  - {match: /a, upstream: http://b}\n")
		}, "routes", docuconf.CodeSchemaMismatch, "duplicate route /a"},
		{"too large", func(f *fixture) {
			f.write("/etc/gateway/routes/routes.yaml", "routes:\n"+strings.Repeat("# padding\n", 7000))
		}, "routes", docuconf.CodeFileTooLarge, "above maxSize 65536"},
		{"empty ca bundle", func(f *fixture) {
			f.write("/etc/gateway/ca/bundle.pem", "")
		}, "upstream-ca", docuconf.CodeFileMalformed, "holds no PEM certificates"},
		{"keystore password", func(f *fixture) {
			f.env["PARTNER_KEYSTORE_PASSWORD"] = "wrong-password"
		}, "partner-keystore", docuconf.CodeKeystoreUnreadable, "cannot open with the password from PARTNER_KEYSTORE_PASSWORD"},
		{"directory", func(f *fixture) {
			require.NoError(t, os.Remove(f.path("/data/geoip/GeoLite2-City.mmdb")))
			require.NoError(t, os.Mkdir(f.path("/data/geoip/GeoLite2-City.mmdb"), 0o755))
		}, "geoip", docuconf.CodeFileMalformed, "is a directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			_, verr := f.parse()
			requireViolation(t, verr, c.input, c.code, c.want)
		})
	}
}

func TestParseJSONConfigFile(t *testing.T) {
	type cfg struct {
		// Feature limits.
		Limits docuconf.ConfigFile[RateLimits] `file:"limits,required" path:"/etc/app/limits/limits.json"`
	}
	root := t.TempDir()
	p := filepath.Join(root, "etc/app/limits/limits.json")
	parse := func(content string) (cfg, *docuconf.ValidationError) {
		writeFile(t, p, []byte(content))
		c, err := docuconf.ParseWithOptions[cfg](docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-"})
		var verr *docuconf.ValidationError
		if err != nil {
			require.True(t, errors.As(err, &verr), "%v", err)
		}
		return c, verr
	}

	c, verr := parse("\xef\xbb\xbf{\"perMinute\": 5}") // a byte-order mark is accepted
	require.Nil(t, verr)
	require.Equal(t, 5, c.Limits.Value().PerMinute)

	_, verr = parse(`{"perMinute": 5,`)
	requireViolation(t, verr, "limits", docuconf.CodeFileMalformed, "is not valid JSON")

	_, verr = parse(`{"perMinute": 0, "burst": "lots"}`)
	requireViolation(t, verr, "limits", docuconf.CodeSchemaMismatch, "at burst: expected an integer, got a string")
	requireViolation(t, verr, "limits", docuconf.CodeSchemaMismatch, "at perMinute: is below minimum 1")
}

func TestParseVariables(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		input string
		code  docuconf.Code
		want  string
	}{
		{"bad int", map[string]string{"PORT": "eighty"}, "PORT", docuconf.CodeInvalidType, `"eighty" is not an integer`},
		{"int above max", map[string]string{"PORT": "70000"}, "PORT", docuconf.CodeOutOfRange, "70000 is above max 65535"},
		{"int8 range", map[string]string{"WORKER_COUNT": "300"}, "WORKER_COUNT", docuconf.CodeOutOfRange, "300 is outside the range of int8"},
		{"missing required", map[string]string{"POD_NAMESPACE": "\x00unset"}, "POD_NAMESPACE", docuconf.CodeMissingRequired, "is required but not set"},
		{"empty required list", map[string]string{"ALLOWED_ORIGINS": ""}, "ALLOWED_ORIGINS", docuconf.CodeMissingRequired, "is required but not set"},
		{"enum", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL", docuconf.CodeNotInEnum, `"verbose" is not one of debug, info, warn, error`},
		{"scheme", map[string]string{"STRIPE_API_BASE": "http://api.stripe.com"}, "STRIPE_API_BASE", docuconf.CodeInvalidScheme, `scheme "http" is not one of https`},
		{"not a url", map[string]string{"STRIPE_API_BASE": "api.stripe.com"}, "STRIPE_API_BASE", docuconf.CodeInvalidType, "is not a URL"},
		{"duration", map[string]string{"REQUEST_TIMEOUT": "10m"}, "REQUEST_TIMEOUT", docuconf.CodeOutOfRange, "10m is above max 5m"},
		{"bad duration", map[string]string{"REQUEST_TIMEOUT": "90 sec"}, "REQUEST_TIMEOUT", docuconf.CodeInvalidType, "is not a duration"},
		{"float", map[string]string{"TRACE_SAMPLE_RATIO": "NaN"}, "TRACE_SAMPLE_RATIO", docuconf.CodeInvalidType, "is not a finite decimal number"},
		{"bool", map[string]string{"DEBUG": "yes"}, "DEBUG", docuconf.CodeInvalidType, "is not a bool"},
		// strconv.ParseBool, which caarlos0/env uses, takes 1, t and F.
		{"bool 1", map[string]string{"DEBUG": "1"}, "DEBUG", docuconf.CodeInvalidType, "is not a bool"},
		{"bool t", map[string]string{"DEBUG": "t"}, "DEBUG", docuconf.CodeInvalidType, "is not a bool"},
		{"hex float", map[string]string{"TRACE_SAMPLE_RATIO": "0x1p-2"}, "TRACE_SAMPLE_RATIO", docuconf.CodeInvalidType, "is not a finite decimal number"},
		{"float .5", map[string]string{"TRACE_SAMPLE_RATIO": ".5"}, "TRACE_SAMPLE_RATIO", docuconf.CodeInvalidType, "is not a finite decimal number"},
		{"negative uint", map[string]string{"CACHE_SIZE": "-5"}, "CACHE_SIZE", docuconf.CodeOutOfRange, "-5 is outside the range of uint"},
		{"pattern", map[string]string{"REGION": "Europe"}, "REGION", docuconf.CodePatternMismatch, "does not match pattern"},
		{"too many items", map[string]string{"EXTRA_PORTS": "1;2;3;4;5"}, "EXTRA_PORTS", docuconf.CodeTooManyItems, "has 5 items, above maxItems 4"},
		{"list item", map[string]string{"EXTRA_PORTS": "1;x"}, "EXTRA_PORTS", docuconf.CodeInvalidType, `item 1: "x" is not an integer`},
		{"list item below itemMin", map[string]string{"EXTRA_PORTS": "80;0"}, "EXTRA_PORTS", docuconf.CodeOutOfRange, "item 1: 0 is below itemMin 1"},
		{"list item beyond uint16", map[string]string{"EXTRA_PORTS": "70000"}, "EXTRA_PORTS", docuconf.CodeOutOfRange, "item 0: 70000 is outside the range of uint16"},
		{"json syntax", map[string]string{"RATE_LIMITS": "{"}, "RATE_LIMITS", docuconf.CodeInvalidType, "is not valid JSON"},
		{"json schema", map[string]string{"RATE_LIMITS": `{"burst":1}`}, "RATE_LIMITS", docuconf.CodeSchemaMismatch, `missing required property "perMinute"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			for k, v := range c.env {
				if v == "\x00unset" {
					delete(f.env, k)
				} else {
					f.env[k] = v
				}
			}
			_, verr := f.parse()
			requireViolation(t, verr, c.input, c.code, c.want)
			require.Len(t, verr.Violations, 1, "%v", verr)
		})
	}
}

// TestParseSignedAndZeroPaddedInts checks that the spec's integer form
// (SPEC §5), which strconv.ParseUint rejects with a sign, reaches the
// struct through caarlos0/env.
func TestParseSignedAndZeroPaddedInts(t *testing.T) {
	f := newFixture(t)
	f.env["CACHE_SIZE"] = "+0050"
	f.env["PORT"] = "+08080"
	f.env["EXTRA_PORTS"] = "+1;007"
	cfg, verr := f.parse()
	require.Nil(t, verr)
	require.Equal(t, uint(50), cfg.Cache.Size)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, []uint16{1, 7}, cfg.ExtraPorts)
}

func TestParseEmptyIsUnset(t *testing.T) {
	f := newFixture(t)
	f.env["PORT"] = ""
	f.env["GOMEMLIMIT"] = ""
	f.env["POD_NAMESPACE"] = "" // an empty string is a present string
	cfg, verr := f.parse()
	require.Nil(t, verr)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, int64(0), cfg.GoMemLimit)
	require.Equal(t, "", cfg.PodNamespace)
}

func TestParseSecretRedaction(t *testing.T) {
	f := newFixture(t)
	f.env["DATABASE_URL"] = "mysql://app:" + dbPassword + "@db"
	f.env["PARTNER_KEYSTORE_PASSWORD"] = "wrong-" + keystorePassword
	logPath := filepath.Join(t.TempDir(), "termination-log")
	opts := f.opts()
	opts.TerminationLog = logPath
	_, err := docuconf.ParseWithOptions[Gateway](opts)
	require.Error(t, err)
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr))
	requireViolation(t, verr, "DATABASE_URL", docuconf.CodeInvalidScheme, "scheme is not one of postgres, postgresql")
	requireViolation(t, verr, "partner-keystore", docuconf.CodeKeystoreUnreadable, "")

	logged, rerr := os.ReadFile(logPath)
	require.NoError(t, rerr)
	for _, out := range []string{err.Error(), string(logged)} {
		require.NotContains(t, out, dbPassword)
		require.NotContains(t, out, "mysql")
		require.NotContains(t, out, keystorePassword)
	}

	// A secret that fails to parse as an int is not echoed either.
	type secretInt struct {
		// A numeric PIN.
		PIN int `env:"PIN" secret:"true"`
	}
	_, err = docuconf.ParseWithOptions[secretInt](docuconf.Options{Environment: map[string]string{"PIN": "12ab-secret"}, TerminationLog: "-"})
	require.ErrorContains(t, err, "PIN: value is not an integer (invalid_type)")
	require.NotContains(t, err.Error(), "12ab")
}

func TestParseUnresolvedInjectorReference(t *testing.T) {
	for ref, scheme := range map[string]string{
		"vault:secret/data/payments/db#url":      "vault:",
		"op://prod/payments/database-url":        "op://",
		"ref+awssecrets://payments/database-url": "ref+",
	} {
		t.Run(scheme, func(t *testing.T) {
			f := newFixture(t)
			f.env["DATABASE_URL"] = ref
			logPath := filepath.Join(t.TempDir(), "termination-log")
			opts := f.opts()
			opts.TerminationLog = logPath
			_, err := docuconf.ParseWithOptions[Gateway](opts)
			var verr *docuconf.ValidationError
			require.True(t, errors.As(err, &verr), "%v", err)
			require.Equal(t, []docuconf.Violation{{
				Input:   "DATABASE_URL",
				Code:    docuconf.CodeInvalidType,
				Message: "holds an unresolved " + scheme + " reference; the injector that should resolve it did not run",
			}}, verr.Violations)

			logged, rerr := os.ReadFile(logPath)
			require.NoError(t, rerr)
			require.Contains(t, string(logged), "unresolved "+scheme+" reference")
			for _, out := range []string{err.Error(), string(logged)} {
				require.NotContains(t, out, ref)
				require.NotContains(t, out, ref[len(scheme):])
			}
		})
	}

	// A secret of another type reports the reference, not a parse error, and
	// a non-secret value that happens to look like a reference is ordinary.
	type cfg struct {
		// A numeric PIN.
		PIN int `env:"PIN" secret:"true"`
		// Upstream address.
		Upstream string `env:"UPSTREAM"`
	}
	_, err := docuconf.ParseWithOptions[cfg](docuconf.Options{
		Environment:    map[string]string{"PIN": "vault:secret/data/pin#value", "UPSTREAM": "vault:8200"},
		TerminationLog: "-",
	})
	require.EqualError(t, err, "docuconf: 1 configuration problem:\n  PIN: holds an unresolved vault: reference; the injector that should resolve it did not run (invalid_type)")

	c, err := docuconf.ParseWithOptions[cfg](docuconf.Options{
		Environment:    map[string]string{"PIN": "1234", "UPSTREAM": "vault:8200"},
		TerminationLog: "-",
	})
	require.NoError(t, err)
	require.Equal(t, "vault:8200", c.Upstream)
}

func TestParseReportsEverythingTogether(t *testing.T) {
	f := newFixture(t)
	delete(f.env, "POD_NAMESPACE")
	f.env["PORT"] = "70000"
	require.NoError(t, os.Remove(f.path("/etc/gateway/license/license.key")))
	logPath := filepath.Join(t.TempDir(), "termination-log")
	f.env[docuconf.EnvTerminationLog] = logPath
	opts := f.opts()
	opts.TerminationLog = ""
	_, err := docuconf.ParseWithOptions[Gateway](opts)
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr))
	require.Len(t, verr.Violations, 3)
	require.Equal(t, `docuconf: 3 configuration problems:
  POD_NAMESPACE: is required but not set (missing_required)
  PORT: 70000 is above max 65535 (out_of_range)
  license: `+f.path("/etc/gateway/license/license.key")+` does not exist (file_missing)`, err.Error())
	logged, rerr := os.ReadFile(logPath)
	require.NoError(t, rerr)
	require.Equal(t, err.Error()+"\n", string(logged))
}

func TestParseFileRootAndPathEnv(t *testing.T) {
	f := newFixture(t)
	f.env[docuconf.EnvFileRoot] = f.root
	f.env["ROUTES_FILE"] = "/elsewhere/routes.yaml"
	f.write("/elsewhere/routes.yaml", "routes:\n  - {match: /x, upstream: https://x}\n")
	opts := f.opts()
	opts.FileRoot = ""
	cfg, err := docuconf.ParseWithOptions[Gateway](opts)
	require.NoError(t, err)
	require.Equal(t, "/x", cfg.Routes.Value().Routes[0].Match)
	require.Equal(t, f.path("/elsewhere/routes.yaml"), cfg.Routes.Path())
}

func TestParseDotEnv(t *testing.T) {
	type cfg struct {
		// Listen port.
		Port int `env:"PORT"`
		// Name of the service.
		Name string `env:"NAME"`
		// A multi-line value.
		Banner string `env:"BANNER"`
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	writeFile(t, p, []byte("# local settings\nexport PORT=9000\nNAME='from file' # comment\nBANNER=\"line1\nline2\"\n"))
	c, err := docuconf.ParseWithOptions[cfg](docuconf.Options{
		Environment:    map[string]string{"PORT": "7000"},
		DotEnv:         []string{p, filepath.Join(dir, "missing.env")},
		TerminationLog: "-",
	})
	require.NoError(t, err)
	require.Equal(t, 7000, c.Port, "the real environment wins")
	require.Equal(t, "from file", c.Name)
	require.Equal(t, "line1\nline2", c.Banner)
}

func TestValidateAfterHostParse(t *testing.T) {
	type cfg struct {
		// Listen port.
		Port int `env:"PORT" min:"1024"`
	}
	c := cfg{Port: 80}
	err := docuconf.Validate(&c, docuconf.Options{Environment: map[string]string{"PORT": "80"}, TerminationLog: "-"})
	require.ErrorContains(t, err, "PORT: 80 is below min 1024 (out_of_range)")
}

func TestWatchReloadsCertificate(t *testing.T) {
	f := newFixture(t)
	opts := f.opts()
	opts.WatchInterval = time.Millisecond
	cfg, err := docuconf.ParseWithOptions[Gateway](opts)
	require.NoError(t, err)
	before, err := cfg.ServingTLS.Current()
	require.NoError(t, err)

	// Rotate as Kubernetes does: new files, swapped in by rename.
	c, k := f.ca.issue(t, leafOpts{dnsNames: []string{"gateway.internal", "api.example.com"}})
	tmp := f.path("/staging")
	writeTLS(t, tmp, f.ca, c, k)
	for _, n := range []string{"tls.crt", "tls.key"} {
		require.NoError(t, os.Rename(filepath.Join(tmp, n), f.path("/etc/gateway/tls/"+n)))
	}
	time.Sleep(5 * time.Millisecond)
	after, err := cfg.ServingTLS.Current()
	require.NoError(t, err)
	require.NotEqual(t, before.Leaf.SerialNumber, after.Leaf.SerialNumber)
	require.Equal(t, c.SerialNumber, after.Leaf.SerialNumber)

	// A broken rotation keeps the previous certificate.
	f.write("/etc/gateway/tls/tls.crt", "garbage")
	time.Sleep(5 * time.Millisecond)
	still, err := cfg.ServingTLS.Current()
	require.NoError(t, err)
	require.Equal(t, c.SerialNumber, still.Leaf.SerialNumber)
}
