package docuconf_test

import (
	"errors"
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
		"files": {"tls": {"type": "tls"}}}`), docuconf.Options{})
	require.ErrorContains(t, err, "contract's files are not supported")
}
