package docuconf

import (
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
)

// KeySet is a set of secret keys that are all valid at once, so that a key
// can be rotated without an outage (contract type "keySet", SPEC §6.1). It
// is for the side that verifies: webhook signatures, inbound API keys, JWT
// HMAC verification, cookie-signing fallbacks.
//
//	// Keys that verify the signature on incoming payment webhooks.
//	WebhookKeys docuconf.KeySet `env:"WEBHOOK_KEYS,required" keyMinLength:"32" keyMaxLength:"256"`
//
// A KeySet is always secret. The platform supplies it like a secret list,
// as one Secret key holding "old,new" during a rotation (envSeparator
// changes the comma). The tags are:
//
//	minKeys:"1"         the fewest keys (default 1, at least 1)
//	maxKeys:"2"         the most keys (default 2, at least minKeys)
//	keyMinLength:"32"   the shortest key, in characters
//	keyMaxLength:"256"  the longest key, in characters
//
// Keys are never trimmed, and an empty key (a stray separator) is always
// out of range. A rotation takes three steps: add the new key and roll
// out; switch the sender to the new key; remove the old key and roll out.
//
// Like Secret, a KeySet prints as *** under fmt, log/slog and
// encoding/json. Its keys keep the order the platform gave them.
type KeySet []Secret

var keySetType = reflect.TypeOf(KeySet(nil))

// Keys returns the keys, in the order the platform gave them.
func (k KeySet) Keys() []Secret { return slices.Clone(k) }

// Contains reports whether candidate is one of the keys, such as an API
// key a caller presents. It compares candidate with every key in constant
// time, so the time taken does not say which key matched, or how much of
// one; it only depends on the number of keys and on candidate's length.
func (k KeySet) Contains(candidate string) bool {
	found := 0
	for _, key := range k {
		found |= subtle.ConstantTimeCompare([]byte(key), []byte(candidate))
	}
	return found == 1
}

// Verify calls check with each key and reports whether any call returned
// true. It is for checks that need the key itself, such as an HMAC:
//
//	ok := cfg.WebhookKeys.Verify(func(key []byte) bool {
//		mac := hmac.New(sha256.New, key)
//		mac.Write(body)
//		return hmac.Equal(mac.Sum(nil), signature)
//	})
//
// Every key is tried, even after one matches, so the time taken does not
// say which key matched. check should compare in constant time itself,
// as hmac.Equal does.
func (k KeySet) Verify(check func(key []byte) bool) bool {
	ok := false
	for _, key := range k {
		if check([]byte(key)) {
			ok = true
		}
	}
	return ok
}

// String returns "***".
func (k KeySet) String() string { return redacted }

// GoString returns docuconf.KeySet{***}.
func (k KeySet) GoString() string { return "docuconf.KeySet{" + redacted + "}" }

// Format prints "***" for every verb, so that a struct holding a KeySet
// is safe to print with %v, %+v and %#v.
func (k KeySet) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		io.WriteString(f, k.GoString())
		return
	}
	io.WriteString(f, redacted)
}

// LogValue implements slog.LogValuer.
func (k KeySet) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON encodes the key set as "***".
func (k KeySet) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
