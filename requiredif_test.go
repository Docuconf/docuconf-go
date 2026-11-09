package docuconf_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

type relayConfig struct {
	// Serve HTTPS instead of HTTP.
	TLS bool `env:"TLS_ENABLED" envDefault:"false"`
	// Subject of the CA that signs client certificates.
	ClientCA string `env:"TLS_CLIENT_CA_SUBJECT" requiredIf:"TLS_ENABLED=true"`
	// Log output format.
	LogFormat string `env:"LOG_FORMAT" values:"text,json" envDefault:"text"`
	// JSON schema the structured logs follow.
	LogSchema string `env:"LOG_SCHEMA_URL" type:"url" requiredIf:"LOG_FORMAT=json"`
	// Port the relay listens on.
	Port int `env:"PORT" envDefault:"443"`
	// Trust store for upstreams, needed on the TLS port.
	Upstreams docuconf.TextFile `file:"upstreams" path:"/etc/relay/upstreams/list.txt" requiredIf:"PORT=443"`
}

// violations returns the boot violations as "INPUT code: message" lines.
func violations(t *testing.T, err error) []string {
	t.Helper()
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "%v", err)
	var out []string
	for _, v := range verr.Violations {
		out = append(out, fmt.Sprintf("%s %s: %s", v.Input, v.Code, v.Message))
	}
	return out
}

// TestRequiredIf checks conditional requirements at boot (SPEC §4.2.1):
// on the condition variable's value, else its default.
func TestRequiredIf(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/relay/upstreams"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "etc/relay/upstreams/list.txt"), []byte("a\n"), 0o644))
	parse := func(env map[string]string, root string) (relayConfig, error) {
		return docuconf.ParseWithOptions[relayConfig](docuconf.Options{Environment: env, FileRoot: root})
	}

	// Off, nothing conditional is needed; PORT's default needs the file.
	_, err := parse(map[string]string{}, root)
	require.NoError(t, err)
	_, err = parse(map[string]string{"PORT": "8080"}, t.TempDir())
	require.NoError(t, err)

	empty := t.TempDir()
	_, err = parse(map[string]string{"TLS_ENABLED": "TRUE", "LOG_FORMAT": "json"}, empty)
	require.ElementsMatch(t, []string{
		"TLS_CLIENT_CA_SUBJECT missing_required: is required when TLS_ENABLED is true but not set",
		`LOG_SCHEMA_URL missing_required: is required when LOG_FORMAT is "json" but not set`,
		"upstreams file_missing: " + filepath.Join(empty, "etc/relay/upstreams/list.txt") + " does not exist, and is required when PORT is 443",
	}, violations(t, err))

	// Set, they load.
	cfg, err := parse(map[string]string{"TLS_ENABLED": "true", "TLS_CLIENT_CA_SUBJECT": "CN=CA", "PORT": "+443"}, root)
	require.NoError(t, err)
	require.Equal(t, "CN=CA", cfg.ClientCA)

	// A condition variable that is itself invalid makes nothing required.
	_, err = parse(map[string]string{"TLS_ENABLED": "yes"}, root)
	require.Equal(t, []string{"TLS_ENABLED"}, inputs(t, err))
}

func inputs(t *testing.T, err error) []string {
	t.Helper()
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "%v", err)
	var out []string
	for _, v := range verr.Violations {
		out = append(out, v.Input)
	}
	return out
}

func TestRequiredIfExport(t *testing.T) {
	out, err := docuconf.Export[relayConfig](docuconf.Meta{Name: "relay"})
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "requiredIf: {\n\t\t\t\tTLS_ENABLED: true\n\t\t\t}")
	require.Contains(t, s, "requiredIf: {\n\t\t\t\tLOG_FORMAT: \"json\"\n\t\t\t}")
	require.Contains(t, s, "requiredIf: {\n\t\t\t\tPORT: 443\n\t\t\t}")
}

func TestRequiredIfDeclarationErrors(t *testing.T) {
	type bad struct {
		Flag     bool              `env:"FLAG" desc:"Feature flag"`
		Token    string            `env:"TOKEN" secret:"true" desc:"API token"`
		Timeout  string            `env:"TIMEOUT" desc:"Request timeout" envDefault:"1s"`
		Missing  string            `env:"MISSING" requiredIf:"NOPE=1" desc:"Needs NOPE"`
		Self     string            `env:"SELF" requiredIf:"SELF=x" desc:"Needs itself"`
		OnSecret string            `env:"ON_SECRET" requiredIf:"TOKEN=x" desc:"Needs a token"`
		BadValue string            `env:"BAD_VALUE" requiredIf:"FLAG=yes" desc:"Needs the flag"`
		Both     string            `env:"BOTH,required" requiredIf:"FLAG=true" desc:"Always needed"`
		Default  string            `env:"DEFAULT" envDefault:"x" requiredIf:"FLAG=true" desc:"Has a default"`
		Old      string            `env:"OLD" deprecated:"Use NEW" requiredIf:"FLAG=true" desc:"Old input"`
		Shape    string            `env:"SHAPE" requiredIf:"FLAG" desc:"Malformed tag"`
		Licence  docuconf.TextFile `file:"licence,required" path:"/etc/app/licence/l.txt" requiredIf:"FLAG=true" desc:"Licence file"`
	}
	_, err := docuconf.ParseWithOptions[bad](docuconf.Options{Environment: map[string]string{"BOTH": "1"}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	all := strings.Join(de.Problems, "\n")
	for _, want := range []string{
		"MISSING (bad.Missing): requiredIf names NOPE, which is not a declared variable",
		"SELF (bad.Self): requiredIf must name another variable",
		"ON_SECRET (bad.OnSecret): requiredIf names TOKEN, which is secret",
		"BAD_VALUE (bad.BadValue): requiredIf: FLAG ",
		"BOTH (bad.Both): a required variable cannot also have requiredIf",
		"DEFAULT (bad.Default): a variable with requiredIf must not have a default",
		"OLD (bad.Old): a variable with requiredIf cannot be deprecated",
		"SHAPE (bad.Shape): requiredIf must be VAR=value",
		"file input licence (bad.Licence): a required file input cannot also have requiredIf",
	} {
		require.Contains(t, all, want)
	}
}

// TestRequiredIfContract checks contract-first mode: a profile default
// can turn the condition on (SPEC §4.4).
func TestRequiredIfContract(t *testing.T) {
	contract := `{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "relay", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"vars": {
			"APP_ENV": {"type": "string", "description": "Deployment environment", "default": "development"},
			"TLS_ENABLED": {"type": "bool", "description": "Serve HTTPS", "default": false},
			"CA": {"type": "string", "description": "Client CA subject", "requiredIf": {"TLS_ENABLED": true}}
		},
		"profiles": {"selector": "APP_ENV", "default": "development", "defaults": {"production": {"TLS_ENABLED": true}}}}`
	load := func(env map[string]string) error {
		_, err := docuconf.LoadContract([]byte(contract), docuconf.Options{Environment: env})
		return err
	}
	require.NoError(t, load(map[string]string{}))
	require.Equal(t, []string{"CA missing_required: is required when TLS_ENABLED is true but not set"}, violations(t, load(map[string]string{"APP_ENV": "production"})))
	require.NoError(t, load(map[string]string{"APP_ENV": "production", "TLS_ENABLED": "false"}))
	require.NoError(t, load(map[string]string{"APP_ENV": "production", "CA": "CN=CA"}))

	for field, want := range map[string]string{
		`"requiredIf": {"TLS_ENABLED": "true"}`:                 "the value for TLS_ENABLED must be a JSON boolean",
		`"requiredIf": {"TLS_ENABLED": true, "APP_ENV": "x"}`:   "requiredIf must be an object naming exactly one variable",
		`"requiredIf": {"NOPE": true}`:                          "requiredIf names NOPE, which is not a declared variable",
		`"requiredIf": {"TLS_ENABLED": true}, "required": true`: "a required variable cannot also have requiredIf",
		`"requiredIf": {"TLS_ENABLED": true}, "default": "x"`:   "a variable with requiredIf must not have a default",
		`"requiredIf": {"APP_ENV": "production"}`:               "",
	} {
		c := strings.Replace(contract, `"requiredIf": {"TLS_ENABLED": true}`, field, 1)
		_, err := docuconf.LoadContract([]byte(c), docuconf.Options{Environment: map[string]string{}})
		if want == "" {
			require.NoError(t, err)
			continue
		}
		require.ErrorContains(t, err, want)
	}
}
