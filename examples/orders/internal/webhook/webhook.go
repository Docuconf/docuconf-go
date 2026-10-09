// Package webhook checks the signature on incoming payment webhooks
// against the key set in WEBHOOK_KEYS.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/docuconf/docuconf-go"
)

// Verify reports whether signature, the hex-encoded HMAC-SHA256 of body,
// was made with any of keys. Accepting every key in the set is what lets
// a key be rotated: during the overlap the old and the new key both work.
func Verify(keys []docuconf.Secret, body []byte, signature string) bool {
	got, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	ok := false
	for _, k := range keys {
		mac := hmac.New(sha256.New, []byte(k.Reveal()))
		mac.Write(body)
		// Check every key, so the time taken does not say which one matched.
		ok = hmac.Equal(mac.Sum(nil), got) || ok
	}
	return ok
}
