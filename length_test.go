package docuconf_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

// Fixed-width fields, as in a COBOL copybook: every value has to fit.
type fixedWidth struct {
	Callback  string                     `env:"CALLBACK_URL" type:"url" maxLength:"30" desc:"Where to report the run"`
	Upstream  *url.URL                   `env:"UPSTREAM" schemes:"https" maxLength:"25" desc:"Upstream base URL"`
	Token     string                     `env:"TOKEN_URL" secret:"true" type:"url" maxLength:"20" desc:"URL with an embedded token"`
	Limits    docuconf.JSON[lengthLimit] `env:"LIMITS" maxLength:"20" envDefault:"{\"n\":1}" desc:"Run limits as JSON"`
	Branches  []string                   `env:"BRANCHES" itemMinLength:"2" itemMaxLength:"4" desc:"Branch codes"`
	Unbounded []string                   `env:"UNBOUNDED" desc:"Items of any length"`
}

type lengthLimit struct {
	N int `json:"n"`
}

func parseFixedWidth(t *testing.T, env map[string]string) (fixedWidth, *docuconf.ValidationError) {
	t.Helper()
	cfg, err := docuconf.ParseWithOptions[fixedWidth](docuconf.Options{Environment: env})
	if err == nil {
		return cfg, nil
	}
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "unexpected error type: %v", err)
	return cfg, verr
}

func TestLengthLimitsAccept(t *testing.T) {
	cfg, verr := parseFixedWidth(t, map[string]string{
		"CALLBACK_URL": "https://a.example/" + strings.Repeat("x", 12), // exactly 30
		"UPSTREAM":     "https://例え.jp/" + strings.Repeat("日", 11),     // 25 characters, 60 bytes
		"LIMITS":       `{"n": 1234567890123}`,                         // exactly 20, spaces count
		"BRANCHES":     "ZÜ01,BE,日本語x",                                 // 4, 2 and 4 characters
		"UNBOUNDED":    strings.Repeat("y", 500),
	})
	require.Nil(t, verr)
	require.Equal(t, []string{"ZÜ01", "BE", "日本語x"}, cfg.Branches)
	require.Equal(t, 1234567890123, cfg.Limits.Value.N)
}

func TestLengthLimitsReject(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		input string
		want  string
	}{
		{"url above maxLength", map[string]string{"CALLBACK_URL": "https://a.example/" + strings.Repeat("x", 13)},
			"CALLBACK_URL", "is 31 characters, above maxLength 30"},
		{"url.URL above maxLength", map[string]string{"UPSTREAM": "https://例え.jp/" + strings.Repeat("日", 12)},
			"UPSTREAM", "is 26 characters, above maxLength 25"},
		{"json above maxLength", map[string]string{"LIMITS": `{"n": 12345678901234}`},
			"LIMITS", "is 21 characters of JSON, above maxLength 20"},
		{"item above itemMaxLength", map[string]string{"BRANCHES": "BE,ZÜRICH"},
			"BRANCHES", `item 1: "ZÜRICH" is 6 characters, above itemMaxLength 4`},
		{"item below itemMinLength", map[string]string{"BRANCHES": "BE,B"},
			"BRANCHES", `item 1: "B" is 1 characters, below itemMinLength 2`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, verr := parseFixedWidth(t, c.env)
			requireViolation(t, verr, c.input, docuconf.CodeOutOfRange, c.want)
			require.Len(t, verr.Violations, 1, "%v", verr)
		})
	}
}

func TestLengthLimitsHideSecrets(t *testing.T) {
	secret := "https://tok:s3cr3t@db.example/x"
	_, verr := parseFixedWidth(t, map[string]string{"TOKEN_URL": secret})
	requireViolation(t, verr, "TOKEN_URL", docuconf.CodeOutOfRange, "value is 31 characters, above maxLength 20")
	require.NotContains(t, verr.Error(), "s3cr3t")
}

func TestLengthLimitsExport(t *testing.T) {
	out, err := docuconf.Export[fixedWidth](docuconf.Meta{Name: "ledger"})
	require.NoError(t, err)
	s := string(out)
	for _, want := range []string{
		"CALLBACK_URL: {\n\t\t\ttype:        \"url\"",
		"maxLength:   30",
		"maxLength: 25",
		"itemMinLength: 2",
		"itemMaxLength: 4",
	} {
		require.Contains(t, s, want)
	}

}

func TestLengthLimitsDeclarationErrors(t *testing.T) {
	type bad struct {
		// Item lengths apply to string lists only.
		Ports []int `env:"PORTS" itemMaxLength:"5" desc:"Ports to open"`
		// maxLength does not apply to an int.
		Port int `env:"PORT" maxLength:"5" desc:"Listen port"`
		// minLength stays string-only.
		URL string `env:"U" type:"url" minLength:"5" desc:"Some URL"`
		// Lengths are non-negative.
		Names []string `env:"NAMES" itemMaxLength:"-1" desc:"Some names"`
		// Defaults satisfy their own constraints.
		Home  string   `env:"HOME_URL" type:"url" maxLength:"10" envDefault:"https://example.com" desc:"Home page"`
		Codes []string `env:"CODES" itemMaxLength:"2" envDefault:"AB,CDE" desc:"Some codes"`
	}
	_, err := docuconf.ParseWithOptions[bad](docuconf.Options{Environment: map[string]string{}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	all := strings.Join(de.Problems, "\n")
	for _, want := range []string{
		"PORTS (bad.Ports): itemMinLength and itemMaxLength apply only to lists of strings",
		"PORT (bad.Port): tag maxLength does not apply to an int variable",
		"U (bad.URL): tag minLength does not apply to a url variable",
		"NAMES (bad.Names): itemMaxLength must be a non-negative integer",
		"HOME_URL (bad.Home): default \"https://example.com\" is 19 characters, above maxLength 10",
		"CODES (bad.Codes): default item 1: \"CDE\" is 3 characters, above itemMaxLength 2",
	} {
		require.Contains(t, all, want)
	}
}

func TestLoadContractLengthLimits(t *testing.T) {
	c := contractWith(`
		"CALLBACK": {"type": "url", "description": "Callback URL", "maxLength": 12},
		"LIMITS": {"type": "json", "description": "Rate limits", "maxLength": 10, "default": {"a": "<>"}},
		"CODES": {"type": "list", "description": "Some codes", "items": "string", "encoding": "indexed", "itemMinLength": 1, "itemMaxLength": 2}`)
	vals, err := docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{
		"CALLBACK": "https://a.jp",
		"CODES__0": "日本",
		"CODES__1": "x",
	}})
	require.NoError(t, err)
	require.Equal(t, "https://a.jp", vals["CALLBACK"])
	require.Equal(t, []string{"日本", "x"}, vals["CODES"])
	// The default is measured as compact JSON without HTML escaping, as
	// CUE renders it: {"a":"<>"} is 10 characters, not 20.
	require.Equal(t, map[string]any{"a": "<>"}, vals["LIMITS"])
	_, err = docuconf.LoadContract(contractWith(`
		"LIMITS": {"type": "json", "description": "Rate limits", "maxLength": 9, "default": {"a": "<>"}}`), docuconf.Options{})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	require.Contains(t, de.Error(), "LIMITS: default is 10 characters of JSON, above maxLength 9")

	_, err = docuconf.LoadContract(contractWith(`
		"PORTS": {"type": "list", "description": "Some ports", "items": "int", "itemMaxLength": 2},
		"NAME": {"type": "enum", "description": "A name", "values": ["a"], "maxLength": 2},
		"U": {"type": "url", "description": "A URL", "maxLength": -1}`), docuconf.Options{})
	require.True(t, errors.As(err, &de), "%v", err)
	for _, want := range []string{
		"PORTS: itemMinLength and itemMaxLength apply only to lists of strings",
		"NAME: field maxLength does not apply to a enum variable",
		"U: maxLength must be a non-negative integer",
	} {
		require.Contains(t, de.Error(), want)
	}
}
