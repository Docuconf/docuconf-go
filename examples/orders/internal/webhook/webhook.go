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
// KeySet.Verify tries every key, so the time taken does not say which one
// matched.
func Verify(keys docuconf.KeySet, body []byte, signature string) bool {
	got, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return keys.Verify(func(key []byte) bool {
		mac := hmac.New(sha256.New, key)
		mac.Write(body)
		return hmac.Equal(mac.Sum(nil), got)
	})
}
