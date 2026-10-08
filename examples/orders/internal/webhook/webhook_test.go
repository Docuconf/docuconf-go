package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/docuconf/docuconf-go/examples/orders/internal/config"
	"github.com/docuconf/docuconf-go/examples/orders/internal/webhook"
)

var (
	oldKey = strings.Repeat("o", 32)
	newKey = strings.Repeat("n", 32)
	body   = []byte(`{"order":"42","status":"paid"}`)
)

func sign(key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// keys loads WEBHOOK_KEYS as the service does at boot.
func keys(t *testing.T, value string) []docuconf.Secret {
	t.Helper()
	cfg, err := docuconf.ParseWithOptions[config.Config](docuconf.Options{
		Environment:    map[string]string{"DATABASE_URL": "postgres://u:p@db/orders", "WEBHOOK_KEYS": value},
		TerminationLog: "-",
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg.WebhookKeys
}

// TestRotation walks through a key rotation: each step is a rollout with
// a new WEBHOOK_KEYS, and a webhook signed with the key in use always
// verifies.
func TestRotation(t *testing.T) {
	steps := []struct {
		name, keys string
		accepts    map[string]bool
	}{
		{"before", oldKey, map[string]bool{oldKey: true, newKey: false}},
		{"overlap", oldKey + "," + newKey, map[string]bool{oldKey: true, newKey: true}},
		{"after", newKey, map[string]bool{oldKey: false, newKey: true}},
	}
	for _, s := range steps {
		ks := keys(t, s.keys)
		for key, want := range s.accepts {
			if got := webhook.Verify(ks, body, sign(key)); got != want {
				t.Errorf("%s: Verify with key %.1s... = %v, want %v", s.name, key, got, want)
			}
		}
	}
	if webhook.Verify(keys(t, oldKey), body, "not hex") {
		t.Error("accepted a malformed signature")
	}
	if webhook.Verify(nil, body, sign(oldKey)) {
		t.Error("accepted a webhook with no keys configured")
	}
}

// TestBadKeySets: the key set's constraints catch an empty or truncated
// key, and a third key, at boot, without printing any key.
func TestBadKeySets(t *testing.T) {
	for _, c := range []struct {
		value string
		code  docuconf.Code
	}{
		{oldKey + ",", docuconf.CodeOutOfRange},               // an empty second key
		{oldKey + "," + newKey[:10], docuconf.CodeOutOfRange}, // a truncated key
		{oldKey + "," + newKey + "," + strings.Repeat("x", 32), docuconf.CodeTooManyItems},
	} {
		_, err := docuconf.ParseWithOptions[config.Config](docuconf.Options{
			Environment:    map[string]string{"DATABASE_URL": "postgres://u:p@db/orders", "WEBHOOK_KEYS": c.value},
			TerminationLog: "-",
		})
		var verr *docuconf.ValidationError
		if !errors.As(err, &verr) || !verr.Has(c.code) {
			t.Errorf("want %s, got %v", c.code, err)
			continue
		}
		if strings.Contains(err.Error(), oldKey) || strings.Contains(err.Error(), newKey[:10]) {
			t.Errorf("the error printed a key: %v", err)
		}
	}
}
