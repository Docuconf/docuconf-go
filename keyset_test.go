package docuconf_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

type keySetConfig struct {
	// Keys that verify the signature on incoming webhooks.
	WebhookKeys docuconf.KeySet `env:"WEBHOOK_KEYS,required" keyMinLength:"8" keyMaxLength:"16"`
	// Keys a cookie may be signed with, newest first.
	CookieKeys docuconf.KeySet `env:"COOKIE_KEYS" envSeparator:";" minKeys:"2" maxKeys:"3"`
}

const (
	oldKey = "old-key-1234"
	newKey = "new-key-5678"
)

func parseKeySets(t *testing.T, env map[string]string) (keySetConfig, *docuconf.ValidationError) {
	t.Helper()
	cfg, err := docuconf.ParseWithOptions[keySetConfig](docuconf.Options{Environment: env, TerminationLog: "-"})
	if err == nil {
		return cfg, nil
	}
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "unexpected error type: %v", err)
	for _, k := range []string{oldKey, newKey, "k1", "k2"} {
		require.NotContains(t, verr.Error(), k, "a key leaked into the violations")
	}
	return cfg, verr
}

func TestKeySetParse(t *testing.T) {
	cfg, verr := parseKeySets(t, map[string]string{"WEBHOOK_KEYS": oldKey + "," + newKey, "COOKIE_KEYS": "k1 ;k2"})
	require.Nil(t, verr)
	require.Equal(t, []docuconf.Secret{oldKey, newKey}, cfg.WebhookKeys.Keys(), "keys keep their order")
	require.Equal(t, []docuconf.Secret{"k1 ", "k2"}, cfg.CookieKeys.Keys(), "keys are never trimmed")

	cfg, verr = parseKeySets(t, map[string]string{"WEBHOOK_KEYS": oldKey})
	require.Nil(t, verr)
	require.Len(t, cfg.WebhookKeys, 1)
	require.Nil(t, cfg.CookieKeys, "an unset optional key set is empty")
}

func TestKeySetViolations(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		code docuconf.Code
		want string
	}{
		{"unset", map[string]string{}, docuconf.CodeMissingRequired, "is required but not set"},
		{"empty is unset", map[string]string{"WEBHOOK_KEYS": ""}, docuconf.CodeMissingRequired, "is required but not set"},
		{"trailing separator", map[string]string{"WEBHOOK_KEYS": oldKey + ","}, docuconf.CodeOutOfRange, "key 1 is empty"},
		{"short key", map[string]string{"WEBHOOK_KEYS": oldKey + ",short"}, docuconf.CodeOutOfRange, "key 1 is 5 characters, below keyMinLength 8"},
		{"long key", map[string]string{"WEBHOOK_KEYS": strings.Repeat("ü", 17)}, docuconf.CodeOutOfRange, "key 0 is 17 characters, above keyMaxLength 16"},
		{"three keys", map[string]string{"WEBHOOK_KEYS": oldKey + "," + newKey + "," + oldKey}, docuconf.CodeTooManyItems, "has 3 keys, above maxKeys 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, verr := parseKeySets(t, c.env)
			requireViolation(t, verr, "WEBHOOK_KEYS", c.code, c.want)
			require.Len(t, verr.Violations, 1, "%v", verr)
		})
	}
	_, verr := parseKeySets(t, map[string]string{"WEBHOOK_KEYS": oldKey, "COOKIE_KEYS": "k1"})
	requireViolation(t, verr, "COOKIE_KEYS", docuconf.CodeTooFewItems, "has 1 key, below minKeys 2")
}

func TestKeySetHelpers(t *testing.T) {
	keys := docuconf.KeySet{oldKey, newKey}
	require.True(t, keys.Contains(oldKey))
	require.True(t, keys.Contains(newKey))
	require.False(t, keys.Contains("old-key-123"))
	require.False(t, keys.Contains(""))
	require.False(t, docuconf.KeySet(nil).Contains(""))

	body := []byte(`{"amount":100}`)
	sign := func(key string) []byte {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write(body)
		return mac.Sum(nil)
	}
	tried := 0
	verify := func(sig []byte) bool {
		tried = 0
		return keys.Verify(func(key []byte) bool {
			tried++
			mac := hmac.New(sha256.New, key)
			mac.Write(body)
			return hmac.Equal(mac.Sum(nil), sig)
		})
	}
	require.True(t, verify(sign(oldKey)))
	require.Equal(t, 2, tried, "every key is tried, even after a match")
	require.True(t, verify(sign(newKey)))
	require.False(t, verify(sign("another-key")))
}

func TestKeySetNeverPrints(t *testing.T) {
	cfg, verr := parseKeySets(t, map[string]string{"WEBHOOK_KEYS": oldKey + "," + newKey})
	require.Nil(t, verr)
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("cfg", "keys", cfg.WebhookKeys, "config", docuconf.LogValue(cfg))
	j, err := json.Marshal(cfg)
	require.NoError(t, err)
	for _, out := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), logged.String(), string(j)} {
		require.NotContains(t, out, "key-")
	}
	require.Equal(t, "***", docuconf.Redacted(cfg)["WEBHOOK_KEYS"])
}

func TestKeySetExport(t *testing.T) {
	out, err := docuconf.Export[keySetConfig](docuconf.Meta{Name: "svc"})
	require.NoError(t, err)
	s := string(out)
	for _, want := range []string{
		"WEBHOOK_KEYS: {\n\t\t\ttype:         \"keySet\"",
		"required:     true\n\t\t\tsecret:       true\n\t\t\tencoding:     \"csv\"\n\t\t\tseparator:    \",\"\n\t\t\tminKeys:      1\n\t\t\tmaxKeys:      2\n\t\t\tkeyMinLength: 8\n\t\t\tkeyMaxLength: 16",
		"separator:   \";\"\n\t\t\tminKeys:     2\n\t\t\tmaxKeys:     3",
	} {
		require.Contains(t, s, want)
	}
}

func TestKeySetDeclarationErrors(t *testing.T) {
	type bad struct {
		// A key set is always secret.
		NotSecret docuconf.KeySet `env:"NOT_SECRET" secret:"false" desc:"Keys that are not secret"`
		// minKeys is at least 1.
		NoKeys docuconf.KeySet `env:"NO_KEYS" minKeys:"0" desc:"Keys with no minimum"`
		// maxKeys is at least minKeys.
		Inverted docuconf.KeySet `env:"INVERTED" minKeys:"3" maxKeys:"2" desc:"Keys bounded backwards"`
		// A list's tags do not apply.
		ListTags docuconf.KeySet `env:"LIST_TAGS" itemMinLength:"8" desc:"Keys with list tags"`
		// A key set has no default.
		Default docuconf.KeySet `env:"DEFAULT" envDefault:"abc" desc:"Keys with a default"`
		// Lengths are at least 1.
		Lengths docuconf.KeySet `env:"LENGTHS" keyMinLength:"0" desc:"Keys of any length"`
	}
	_, err := docuconf.ParseWithOptions[bad](docuconf.Options{Environment: map[string]string{}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	all := strings.Join(de.Problems, "\n")
	for _, want := range []string{
		`NOT_SECRET (bad.NotSecret): a docuconf.KeySet field is always secret; remove secret:"false"`,
		"NO_KEYS (bad.NoKeys): minKeys must be at least 1",
		"INVERTED (bad.Inverted): maxKeys must be at least minKeys",
		"LIST_TAGS (bad.ListTags): tag itemMinLength does not apply to a keySet variable",
		"DEFAULT (bad.Default): a secret variable must not have a default",
		"LENGTHS (bad.Lengths): keyMinLength must be at least 1",
	} {
		require.Contains(t, all, want)
	}
}

func TestKeySetContractFirst(t *testing.T) {
	contract := `{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "go", "sdk": "x", "version": "1"}},
		"vars": {"KEYS": {"type": "keySet", "description": "Keys that verify webhooks", "secret": true,
			"encoding": "%s", "minKeys": 1, "maxKeys": 2, "keyMinLength": 4}}}`
	load := func(enc string, env map[string]string) (map[string]any, error) {
		return docuconf.LoadContract([]byte(fmt.Sprintf(contract, enc)), docuconf.Options{Environment: env, TerminationLog: "-"})
	}
	for _, c := range []struct {
		enc string
		env map[string]string
	}{
		{"csv", map[string]string{"KEYS": "aaaa,bbbb"}},
		{"json", map[string]string{"KEYS": `["aaaa","bbbb"]`}},
		{"indexed", map[string]string{"KEYS__0": "aaaa", "KEYS__1": "bbbb"}},
	} {
		vals, err := load(c.enc, c.env)
		require.NoError(t, err, c.enc)
		require.Equal(t, docuconf.KeySet{"aaaa", "bbbb"}, vals["KEYS"], c.enc)
	}
	_, err := load("json", map[string]string{"KEYS": `["aaaa",""]`})
	var verr *docuconf.ValidationError
	require.True(t, errors.As(err, &verr), "%v", err)
	requireViolation(t, verr, "KEYS", docuconf.CodeOutOfRange, "key 1 is empty")

	notSecret := strings.Replace(fmt.Sprintf(contract, "csv"), `"secret": true`, `"secret": false`, 1)
	_, err = docuconf.LoadContract([]byte(notSecret), docuconf.Options{Environment: map[string]string{}})
	require.ErrorContains(t, err, "a keySet is always secret")
	listField := strings.Replace(fmt.Sprintf(contract, "csv"), `"keyMinLength"`, `"itemMinLength"`, 1)
	_, err = docuconf.LoadContract([]byte(listField), docuconf.Options{Environment: map[string]string{}})
	require.ErrorContains(t, err, "field itemMinLength does not apply to a keySet variable")
}
