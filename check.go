package docuconf

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Wire encodings (SPEC §5).
const (
	encGo       = "go"
	encISO8601  = "iso8601"
	encSeconds  = "seconds"
	encTimespan = "timespan"

	encCSV     = "csv"
	encJSON    = "json"
	encIndexed = "indexed"
)

// floatRe is the wire form of a float (SPEC §5): decimal digits on both
// sides of an optional point, an optional sign and an optional exponent.
var floatRe = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// check validates one present value of a variable against its type and
// constraints. Messages never include the value of a secret variable.
func (v *varDecl) check(raw string) []Violation {
	_, out := v.parse(raw)
	return out
}

// violation returns a one-violation slice for the variable.
func (v *varDecl) violation(code Code, format string, args ...any) []Violation {
	return []Violation{{Input: v.name, Code: code, Message: fmt.Sprintf(format, args...)}}
}

// parse validates one present value, as check does, and returns it typed:
// string, int64 (*big.Int beyond that range), float64, bool,
// time.Duration, []string, []int64, or a decoded JSON value with numbers
// as json.Number. The value is nil when there are violations.
func (v *varDecl) parse(raw string) (any, []Violation) {
	viol := v.violation
	show := v.show

	switch v.typ {
	case typeString, typeEnum:
		if v.custom != nil {
			if err := v.custom(raw); err != nil {
				return nil, viol(CodeInvalidType, "%s is not a valid %v%s", show(raw), v.goType, v.reason(err))
			}
		}
		if v.typ == typeEnum {
			if slices.Contains(v.values, raw) {
				return raw, nil
			}
			return nil, viol(CodeNotInEnum, "%s is not one of %s", show(raw), strings.Join(v.values, ", "))
		}
		var out []Violation
		n := utf8.RuneCountInString(raw)
		if v.minLength != nil && n < *v.minLength {
			out = append(out, viol(CodeOutOfRange, "%s is %d characters, below minLength %d", show(raw), n, *v.minLength)...)
		}
		if v.maxLength != nil && n > *v.maxLength {
			out = append(out, viol(CodeOutOfRange, "%s is %d characters, above maxLength %d", show(raw), n, *v.maxLength)...)
		}
		if v.pattern != nil && !v.pattern.MatchString(raw) {
			out = append(out, viol(CodePatternMismatch, "%s does not match pattern %s", show(raw), v.pattern)...)
		}
		if len(out) > 0 {
			return nil, out
		}
		return raw, nil

	case typeInt:
		n, code, msg := v.parseInt(raw)
		if msg != "" {
			return nil, viol(code, "%s", msg)
		}
		if v.minInt != nil && n.Cmp(v.minInt) < 0 {
			return nil, viol(CodeOutOfRange, "%s is below min %s", v.showNum(raw), v.minInt)
		}
		if v.maxInt != nil && n.Cmp(v.maxInt) > 0 {
			return nil, viol(CodeOutOfRange, "%s is above max %s", v.showNum(raw), v.maxInt)
		}
		if n.IsInt64() {
			return n.Int64(), nil
		}
		return n, nil

	case typeFloat:
		bits := 64
		if v.goType.Kind() == reflect.Float32 {
			bits = 32
		}
		// The wire form is decimal only (SPEC §5): ParseFloat alone would
		// also take hex floats, inf, nan, ".5" and "5.".
		if !floatRe.MatchString(raw) {
			return nil, viol(CodeInvalidType, "%s is not a finite decimal number", show(raw))
		}
		f, err := strconv.ParseFloat(raw, bits)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, viol(CodeInvalidType, "%s is not a finite decimal number", show(raw))
		}
		if v.minFloat != nil && f < *v.minFloat {
			return nil, viol(CodeOutOfRange, "%s is below min %s", v.showNum(raw), formatFloat(*v.minFloat))
		}
		if v.maxFloat != nil && f > *v.maxFloat {
			return nil, viol(CodeOutOfRange, "%s is above max %s", v.showNum(raw), formatFloat(*v.maxFloat))
		}
		return f, nil

	case typeBool:
		// Exactly true or false, in any case (SPEC §5): never 1, 0, t, f,
		// yes or no, which strconv.ParseBool and other hosts accept.
		if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false") {
			return strings.EqualFold(raw, "true"), nil
		}
		return nil, viol(CodeInvalidType, "%s is not a bool (true or false)", show(raw))

	case typeDuration:
		d, err := v.parseDuration(raw)
		if err != nil {
			return nil, viol(CodeInvalidType, "%s is not a duration %s", show(raw), durationHint(v.durEncoding))
		}
		if v.minDur != nil && d < *v.minDur {
			return nil, viol(CodeOutOfRange, "%s is below min %s", v.showNum(raw), formatDuration(*v.minDur))
		}
		if v.maxDur != nil && d > *v.maxDur {
			return nil, viol(CodeOutOfRange, "%s is above max %s", v.showNum(raw), formatDuration(*v.maxDur))
		}
		return d, nil

	case typeURL:
		u, err := url.Parse(raw)
		if err != nil || !urlRe.MatchString(raw) {
			return nil, viol(CodeInvalidType, "%s is not a URL of the form scheme://...", show(raw))
		}
		if v.custom != nil {
			if err := v.custom(raw); err != nil {
				return nil, viol(CodeInvalidType, "%s is not a valid %v%s", show(raw), v.goType, v.reason(err))
			}
		}
		if len(v.schemes) > 0 && !slices.ContainsFunc(v.schemes, func(s string) bool { return strings.EqualFold(u.Scheme, s) }) {
			if v.secret {
				return nil, viol(CodeInvalidScheme, "scheme is not one of %s", strings.Join(v.schemes, ", "))
			}
			return nil, viol(CodeInvalidScheme, "scheme %q is not one of %s", u.Scheme, strings.Join(v.schemes, ", "))
		}
		if out := v.checkMaxLength(raw); out != nil {
			return nil, out
		}
		return raw, nil

	case typeList, typeKeySet:
		items, out := v.splitItems(raw)
		if len(out) > 0 {
			return nil, out
		}
		return v.parseItems(items)

	case typeJSON:
		doc, err := decodeJSON([]byte(raw))
		if err != nil {
			return nil, viol(CodeInvalidType, "is not valid JSON%s", v.reason(err))
		}
		// The length of the value as received, not re-encoded: that is
		// what a fixed-width field has to hold.
		if out := v.checkMaxLength(raw); out != nil {
			return nil, out
		}
		var out []Violation
		if v.schema != nil {
			for _, p := range v.schema.validate(doc) {
				out = append(out, viol(CodeSchemaMismatch, "%s", p)...)
			}
		}
		if len(out) > 0 {
			return nil, out
		}
		if v.custom != nil {
			if err := v.custom(raw); err != nil {
				return nil, viol(CodeSchemaMismatch, "does not bind to %v%s", v.jsonType, v.reason(err))
			}
		}
		if v.jsonType != nil {
			if err := validateValue(v.jsonType, []byte(raw)); err != nil {
				return nil, viol(CodeSchemaMismatch, "%s", v.redact(err.Error()))
			}
		}
		return doc, nil
	}
	return raw, nil
}

// checkMaxLength checks a url or json value against maxLength, counted
// in characters (Unicode code points) as for a string.
func (v *varDecl) checkMaxLength(raw string) []Violation {
	if v.maxLength == nil {
		return nil
	}
	if n := utf8.RuneCountInString(raw); n > *v.maxLength {
		if v.typ == typeJSON {
			return v.violation(CodeOutOfRange, "is %d characters of JSON, above maxLength %d", n, *v.maxLength)
		}
		return v.violation(CodeOutOfRange, "%s is %d characters, above maxLength %d", v.show(raw), n, *v.maxLength)
	}
	return nil
}

// splitItems splits a list value in its encoding. An indexed list spans
// several variables, so the caller collects its items instead.
func (v *varDecl) splitItems(raw string) ([]string, []Violation) {
	if v.listEncoding != encJSON {
		return strings.Split(raw, v.separator), nil
	}
	doc, err := decodeJSON([]byte(raw))
	arr, ok := doc.([]any)
	if err != nil || !ok {
		return nil, v.violation(CodeInvalidType, "is not a JSON array%s", v.reason(err))
	}
	items := make([]string, len(arr))
	for i, x := range arr {
		switch x := x.(type) {
		case string:
			if v.items == "string" {
				items[i] = x
				continue
			}
		case json.Number:
			if v.items == "int" {
				items[i] = x.String()
				continue
			}
		}
		want := "a string"
		if v.items == "int" {
			want = "an integer"
		}
		return nil, v.violation(CodeInvalidType, "item %d is %s, not %s", i, jsonKind(x), want)
	}
	return items, nil
}

// parseItems checks a list's items and returns them as []string or
// []int64, or a key set's keys as a KeySet. (A []uint64 declaration may
// hold larger items; its typed value is never used, since caarlos0/env
// parses declared lists.)
func (v *varDecl) parseItems(items []string) (any, []Violation) {
	if v.typ == typeKeySet {
		return v.parseKeys(items)
	}
	viol := v.violation
	var ints []int64
	for i, item := range items {
		if v.items == "int" {
			n, code, msg := v.parseInt(item)
			if msg != "" {
				return nil, viol(code, "item %d: %s", i, msg)
			}
			if v.itemMin != nil && n.Cmp(v.itemMin) < 0 {
				return nil, viol(CodeOutOfRange, "item %d: %s is below itemMin %s", i, v.showNum(item), v.itemMin)
			}
			if v.itemMax != nil && n.Cmp(v.itemMax) > 0 {
				return nil, viol(CodeOutOfRange, "item %d: %s is above itemMax %s", i, v.showNum(item), v.itemMax)
			}
			ints = append(ints, n.Int64())
		} else {
			if v.custom != nil {
				if err := v.custom(item); err != nil {
					return nil, viol(CodeInvalidType, "item %d: %s is not a valid %v%s", i, v.show(item), v.goType.Elem(), v.reason(err))
				}
			}
			n := utf8.RuneCountInString(item)
			if v.itemMinLength != nil && n < *v.itemMinLength {
				return nil, viol(CodeOutOfRange, "item %d: %s is %d characters, below itemMinLength %d", i, v.show(item), n, *v.itemMinLength)
			}
			if v.itemMaxLength != nil && n > *v.itemMaxLength {
				return nil, viol(CodeOutOfRange, "item %d: %s is %d characters, above itemMaxLength %d", i, v.show(item), n, *v.itemMaxLength)
			}
		}
	}
	if v.minItems != nil && len(items) < *v.minItems {
		return nil, viol(CodeTooFewItems, "has %s, below minItems %d", plural(len(items), "item"), *v.minItems)
	}
	if v.maxItems != nil && len(items) > *v.maxItems {
		return nil, viol(CodeTooManyItems, "has %s, above maxItems %d", plural(len(items), "item"), *v.maxItems)
	}
	if v.items == "int" {
		if ints == nil {
			ints = []int64{}
		}
		return ints, nil
	}
	return slices.Clone(items), nil
}

// parseKeys checks a key set's keys (SPEC §4.3): their number, and the
// length of each, which is never zero. Keys are secret, so no message
// holds one.
func (v *varDecl) parseKeys(keys []string) (any, []Violation) {
	for i, key := range keys {
		n := utf8.RuneCountInString(key)
		switch {
		case n == 0:
			return nil, v.violation(CodeOutOfRange, "key %d is empty", i)
		case v.itemMinLength != nil && n < *v.itemMinLength:
			return nil, v.violation(CodeOutOfRange, "key %d is %d characters, below keyMinLength %d", i, n, *v.itemMinLength)
		case v.itemMaxLength != nil && n > *v.itemMaxLength:
			return nil, v.violation(CodeOutOfRange, "key %d is %d characters, above keyMaxLength %d", i, n, *v.itemMaxLength)
		}
	}
	if v.minItems != nil && len(keys) < *v.minItems {
		return nil, v.violation(CodeTooFewItems, "has %s, below minKeys %d", plural(len(keys), "key"), *v.minItems)
	}
	if v.maxItems != nil && len(keys) > *v.maxItems {
		return nil, v.violation(CodeTooManyItems, "has %s, above maxKeys %d", plural(len(keys), "key"), *v.maxItems)
	}
	out := make(KeySet, len(keys))
	for i, key := range keys {
		out[i] = Secret(key)
	}
	return out, nil
}

// indexedItems collects an indexed list from NAME__0, NAME__1, ... The
// list is present when any NAME__<n> is set. Indices must run from 0 with
// no gap; otherwise missing names the first absent index. Only canonical
// decimal suffixes count, so nested keys such as NAME__HOST are ignored.
func indexedItems(environ map[string]string, name string) (items []string, present bool, missing int) {
	prefix := name + "__"
	n := 0
	for k := range environ {
		if i, ok := listIndex(k, prefix); ok && i+1 > n {
			n = i + 1
		}
	}
	for i := 0; i < n; i++ {
		s, ok := environ[prefix+strconv.Itoa(i)]
		if !ok {
			return nil, true, i
		}
		items = append(items, s)
	}
	return items, n > 0, -1
}

// listIndex parses the index of an indexed list item name: digits with no
// leading zero, after prefix.
func listIndex(key, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(key, prefix)
	if !ok || rest == "" || (len(rest) > 1 && rest[0] == '0') {
		return 0, false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	i, err := strconv.Atoi(rest)
	return i, err == nil
}

// intRe is the wire form of an integer (SPEC §5): decimal ASCII digits
// with an optional sign. Leading zeros are decimal, never octal.
var intRe = regexp.MustCompile(`^[+-]?[0-9]+$`)

// parseInt parses an integer and checks it against the range of the
// field's Go kind (64-bit signed in contract-first mode). On failure it
// returns a code and message.
func (v *varDecl) parseInt(raw string) (*big.Int, Code, string) {
	n, ok := new(big.Int).SetString(raw, 10)
	if !intRe.MatchString(raw) || !ok {
		return nil, CodeInvalidType, fmt.Sprintf("%s is not an integer", v.show(raw))
	}
	lo, hi := new(big.Int), new(big.Int).Lsh(big.NewInt(1), uint(v.intBits))
	if v.unsigned {
		hi.Sub(hi, big.NewInt(1))
	} else {
		hi.Rsh(hi, 1)
		lo.Neg(hi)
		hi.Sub(hi, big.NewInt(1))
	}
	if n.Cmp(lo) < 0 || n.Cmp(hi) > 0 {
		return nil, CodeOutOfRange, fmt.Sprintf("%s is outside the range of %v", v.showNum(raw), v.goTypeOrElem())
	}
	return n, "", ""
}

func (v *varDecl) goTypeOrElem() any {
	if v.typ == typeList || v.typ == typeKeySet {
		return v.goType.Elem()
	}
	return v.goType
}

// show quotes a value for a message, or hides it for a secret.
func (v *varDecl) show(s string) string {
	if v.secret {
		return "value"
	}
	if len(s) > 64 {
		s = s[:61] + "..."
	}
	return strconv.Quote(s)
}

// showNum shows a value that parsed as a number unquoted.
func (v *varDecl) showNum(s string) string {
	if v.secret {
		return "value"
	}
	return s
}

// reason formats a parser's error for a message. Parser errors often
// quote the input, so they are left out for secrets.
func (v *varDecl) reason(err error) string {
	if v.secret || err == nil {
		return ""
	}
	return ": " + err.Error()
}

func (v *varDecl) redact(s string) string {
	if v.secret {
		return "value does not satisfy its type's Validate method"
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// formatDuration writes a duration in the contract's Go syntax, which
// allows only integer components: 1h30m, 1500ms, 0s.
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	for _, u := range []struct {
		unit time.Duration
		name string
	}{{time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}, {time.Millisecond, "ms"}, {time.Microsecond, "us"}, {time.Nanosecond, "ns"}} {
		if n := d / u.unit; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.name)
			d -= n * u.unit
		}
	}
	return b.String()
}

// decodeJSON decodes a JSON document into generic values, keeping numbers
// exact as json.Number.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}
