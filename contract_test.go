package docuconf_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

func contractWith(vars string) []byte {
	return []byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"vars": {` + vars + `}}`)
}

func TestLoadContractTypes(t *testing.T) {
	c := contractWith(`
		"PORT": {"type": "int", "description": "Listen port", "default": 8080},
		"RATIO": {"type": "float", "description": "Sample ratio"},
		"TIMEOUT": {"type": "duration", "description": "Request timeout", "encoding": "iso8601", "default": "30s"},
		"SHARDS": {"type": "list", "description": "Shard ids", "items": "int", "encoding": "indexed", "itemMax": 9},
		"NAMES": {"type": "list", "description": "Some names", "items": "string", "separator": ";"},
		"LIMITS": {"type": "json", "description": "Rate limits", "schema": {"type": "object", "properties": {"n": {"type": "integer"}}}},
		"MODE": {"type": "enum", "description": "Run mode", "values": ["a", "b"]}`)
	vals, err := docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{
		"RATIO":     "0.5",
		"TIMEOUT":   "P1DT2H0.25S",
		"SHARDS__0": "3", "SHARDS__1": "9",
		"NAMES":  "x;y",
		"LIMITS": `{"n": 5}`,
	}})
	require.NoError(t, err)
	require.Equal(t, int64(8080), vals["PORT"])
	require.Equal(t, 0.5, vals["RATIO"])
	require.Equal(t, 26*time.Hour+250*time.Millisecond, vals["TIMEOUT"])
	require.Equal(t, []int64{3, 9}, vals["SHARDS"])
	require.Equal(t, []string{"x", "y"}, vals["NAMES"])
	require.Contains(t, vals, "MODE")
	require.Nil(t, vals["MODE"])

	vals, err = docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{}})
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, vals["TIMEOUT"], "a default is in Go syntax whatever the encoding")
}

func TestLoadContractDurationEncodings(t *testing.T) {
	cases := []struct {
		encoding, raw string
		want          time.Duration
		ok            bool
	}{
		{"iso8601", "PT1M30S", 90 * time.Second, true},
		{"iso8601", "PT0,5S", 500 * time.Millisecond, true},
		{"iso8601", "P2D", 48 * time.Hour, true},
		{"iso8601", "PT", 0, false},
		{"iso8601", "P1Y", 0, false},
		{"iso8601", "-PT1S", 0, false},
		{"seconds", "1.000000001", time.Second + 1, true},
		{"seconds", "-1", 0, false},
		{"seconds", "1e3", 0, false},
		{"seconds", "99999999999999999999", 0, false},
		{"timespan", "1:02:03", time.Hour + 2*time.Minute + 3*time.Second, true},
		{"timespan", "3.00:00:00.1234567", 72*time.Hour + 123456700, true},
		{"timespan", "24:00:00", 0, false},
		{"timespan", "00:60:00", 0, false},
		{"timespan", "00:01", 0, false},
		{"go", "1h1.5s", time.Hour + 1500*time.Millisecond, true},
	}
	for _, c := range cases {
		t.Run(c.encoding+" "+c.raw, func(t *testing.T) {
			contract := contractWith(`"D": {"type": "duration", "description": "A duration", "encoding": "` + c.encoding + `"}`)
			vals, err := docuconf.LoadContract(contract, docuconf.Options{Environment: map[string]string{"D": c.raw}, TerminationLog: "-"})
			if !c.ok {
				var verr *docuconf.ValidationError
				require.ErrorAs(t, err, &verr)
				require.True(t, verr.Has(docuconf.CodeInvalidType), "%v", verr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, vals["D"])
		})
	}
}

func TestLoadContractInvalidContract(t *testing.T) {
	c := contractWith(`
		"PORT": {"type": "int", "description": "Listen port", "min": 1, "default": 0},
		"NAMES": {"type": "list", "description": "Some names", "items": "string", "itemMin": 1},
		"X": {"type": "integer", "description": "Not a type"},
		"P": {"type": "string", "description": "Bad pattern", "pattern": "(?=x)"},
		"J": {"type": "json", "description": "Odd schema", "schema": {"oneOf": []}},
		"T": {"type": "bool", "description": "A bool", "minLength": 1}`)
	_, err := docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	for _, want := range []string{
		"PORT: default 0 is below min 1",
		"NAMES: itemMin and itemMax apply only to lists of integers",
		`X: type "integer" is not a contract type`,
		"P: pattern is not valid RE2",
		"J: schema: keyword oneOf needs a type",
		"T: field minLength does not apply to a bool variable",
	} {
		require.Contains(t, de.Error(), want)
	}

	_, err = docuconf.LoadContract([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract", "vars": {},
		"overlays": {"app": {"format": "json", "path": "/etc/app/overlay.json"}}}`), docuconf.Options{})
	require.ErrorContains(t, err, `overlay app: keySeparator must be ":" or "."`)

	_, err = docuconf.LoadContract([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"vars": {"PORT": {"type": "int", "description": "Listen port", "max": 9000}, "KEY": {"type": "string", "description": "A secret key", "secret": true}},
		"profiles": {"selector": "APP_ENV", "default": "Production",
			"defaults": {"Production": {"PORT": 9999, "KEY": "x", "NOPE": 1}}}}`), docuconf.Options{})
	for _, want := range []string{
		`profiles.selector "APP_ENV" must be a declared variable`,
		"profiles.defaults.Production: PORT 9999 is above max 9000",
		"profiles.defaults.Production: KEY is secret",
		"profiles.defaults.Production: NOPE is not a declared variable",
	} {
		require.ErrorContains(t, err, want)
	}

	_, err = docuconf.LoadContract(contractWithFiles(``, `
		"settings": {"type": "config", "description": "App settings", "path": "/etc/app/settings.ini", "format": "ini"},
		"jks": {"type": "keystore", "description": "Java keystore", "path": "/etc/app/ks.jks", "format": "jks"},
		"Bad_Name": {"type": "text", "description": "Bad name", "path": "/etc/x"},
		"rel": {"type": "text", "description": "Relative path", "path": "etc/x"},
		"odd": {"type": "binary", "description": "A binary file", "path": "/etc/odd", "pattern": "x"}`), docuconf.Options{})
	require.True(t, errors.As(err, &de), "%v", err)
	for _, want := range []string{
		`file input settings: format "ini" is not a config file format`,
		`file input jks: format "jks" is not supported`,
		"file input Bad_Name: input name must be a DNS label",
		`file input rel: path "etc/x" must be absolute and normalised`,
		"file input odd: field pattern does not apply to a binary input",
	} {
		require.Contains(t, de.Error(), want)
	}
}

func contractWithFiles(vars, files string) []byte {
	return []byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"vars": {` + vars + `}, "files": {` + files + `}}`)
}

func TestLoadContractFiles(t *testing.T) {
	root := t.TempDir()
	ca := newCA(t)
	cert, key := ca.issue(t, leafOpts{dnsNames: []string{"api.example.com"}})
	writeTLS(t, filepath.Join(root, "etc/app/tls"), ca, cert, key)
	writeFile(t, filepath.Join(root, "etc/app/ca.pem"), certPEM(ca.cert))
	writeFile(t, filepath.Join(root, "etc/app/ks.p12"), keystore(t, ca, "s3cret"))
	writeFile(t, filepath.Join(root, "data/orders.txt"), []byte("A-1 3\n"))
	writeFile(t, filepath.Join(root, "etc/app/geo.db"), []byte{0, 1, 2})
	writeFile(t, filepath.Join(root, "etc/app/settings.yaml"), []byte("retries: 3\n"))

	c := contractWithFiles(`
		"KS_PASSWORD": {"type": "string", "description": "Keystore password", "secret": true, "required": true},
		"ORDERS_FILE": {"type": "string", "description": "Where the orders are"}`, `
		"tls": {"type": "tls", "description": "Serving certificate", "path": "/etc/app/tls", "dnsNames": ["api.example.com"], "requireCA": true},
		"ca": {"type": "caBundle", "description": "Trusted CAs", "path": "/etc/app/ca.pem"},
		"ks": {"type": "keystore", "description": "Client keystore", "path": "/etc/app/ks.p12", "format": "pkcs12", "passwordVar": "KS_PASSWORD"},
		"orders": {"type": "text", "description": "Orders to process", "path": "/var/orders.txt", "pathEnv": "ORDERS_FILE", "pattern": "^A-"},
		"geo": {"type": "binary", "description": "GeoIP database", "path": "/etc/app/geo.db", "maxSize": 10},
		"settings": {"type": "config", "description": "App settings", "path": "/etc/app/settings.yaml", "format": "yaml",
			"schema": {"type": "object", "properties": {"retries": {"type": "integer", "minimum": 0}}, "additionalProperties": false}},
		"optional": {"type": "text", "description": "Not mounted", "path": "/etc/app/none.txt"}`)
	env := map[string]string{"KS_PASSWORD": "s3cret", "ORDERS_FILE": "/data/orders.txt"}
	vals, err := docuconf.LoadContract(c, docuconf.Options{Environment: env, FileRoot: root, TerminationLog: "-"})
	require.NoError(t, err)
	require.True(t, vals["tls"].(docuconf.TLSKeyPair).Present())
	require.Len(t, vals["ca"].(docuconf.CABundle).Certificates(), 1)
	require.NotNil(t, vals["ks"].(docuconf.Keystore).PrivateKey())
	require.Equal(t, "A-1 3\n", vals["orders"].(docuconf.TextFile).Content())
	require.Equal(t, filepath.Join(root, "data/orders.txt"), vals["orders"].(docuconf.TextFile).Path())
	require.True(t, vals["geo"].(docuconf.BinaryFile).Present())
	require.Equal(t, map[string]any{"retries": float64(3)}, vals["settings"].(docuconf.ConfigFile[any]).Value())
	require.False(t, vals["optional"].(docuconf.TextFile).Present())

	// Every problem is reported together with the variables', and the
	// keystore password never appears.
	other, otherKey := newCA(t).issue(t, leafOpts{dnsNames: []string{"other.example.com"}})
	writeTLS(t, filepath.Join(root, "etc/app/tls"), ca, other, otherKey)
	writeFile(t, filepath.Join(root, "etc/app/geo.db"), make([]byte, 11))
	writeFile(t, filepath.Join(root, "etc/app/settings.yaml"), []byte("retries: -1\n"))
	env = map[string]string{"KS_PASSWORD": "wrong-password", "ORDERS_FILE": "/data/missing.txt"}
	_, err = docuconf.LoadContract(c, docuconf.Options{Environment: env, FileRoot: root, TerminationLog: "-"})
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "%v", err)
	for _, code := range []docuconf.Code{docuconf.CodeCertificateNameMismatch, docuconf.CodeKeystoreUnreadable,
		docuconf.CodeFileTooLarge, docuconf.CodeSchemaMismatch} {
		require.True(t, verr.Has(code), "want %s in %v", code, err)
	}
	require.NotContains(t, err.Error(), "wrong-password")

	// A required file that is missing.
	c = contractWithFiles(``, `"orders": {"type": "text", "description": "Orders to process", "path": "/var/orders.txt", "required": true}`)
	_, err = docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-"})
	require.True(t, errors.As(err, &verr), "%v", err)
	require.Equal(t, docuconf.CodeFileMissing, verr.Violations[0].Code)
	require.Equal(t, filepath.Join(root, "var/orders.txt")+" does not exist", verr.Violations[0].Message)

	// With a pathEnv, the message names the variable that moves the file.
	c = contractWithFiles(`"ORDERS_FILE": {"type": "string", "description": "Where the orders are"}`,
		`"orders": {"type": "text", "description": "Orders to process", "path": "/var/orders.txt", "pathEnv": "ORDERS_FILE", "required": true}`)
	_, err = docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-"})
	require.True(t, errors.As(err, &verr), "%v", err)
	require.Equal(t, filepath.Join(root, "var/orders.txt")+" does not exist (mount it there, or set ORDERS_FILE)", verr.Violations[0].Message)
}

func TestContractCUEWithProfilesAndOverlays(t *testing.T) {
	out, err := docuconf.ContractCUE([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"vars": {"APP_ENV": {"type": "string", "description": "Selected profile", "default": "Production"},
			"PAGE_SIZE": {"type": "int", "description": "Items per page", "configKey": "Catalog:PageSize"}},
		"profiles": {"selector": "APP_ENV", "default": "Production", "defaults": {"Production": {"PAGE_SIZE": 50}}},
		"overlays": {"platform": {"format": "json", "path": "/app/config/platform.json", "keySeparator": ":"}}}`), "")
	require.NoError(t, err)
	s := string(out)
	require.Contains(t, s, "overlays: {\n\t\tplatform: {")
	require.Contains(t, s, "profiles: {\n\t\tselector: \"APP_ENV\"")
	require.Less(t, strings.Index(s, "overlays:"), strings.Index(s, "profiles:"))
}
