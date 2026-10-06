package docuconf

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// check validates one present value of a variable against its type and
// constraints. Messages never include the value of a secret variable.
func (v *varDecl) check(raw string) []Violation {
	viol := func(code Code, format string, args ...any) []Violation {
		return []Violation{{Input: v.name, Code: code, Message: fmt.Sprintf(format, args...)}}
	}
	show := func(s string) string { return v.show(s) }

	switch v.typ {
	case typeString, typeEnum:
		if v.custom != nil {
			if err := v.custom(raw); err != nil {
				return viol(CodeInvalidType, "%s is not a valid %v%s", show(raw), v.goType, v.reason(err))
			}
		}
		if v.typ == typeEnum {
			for _, x := range v.values {
				if raw == x {
					return nil
				}
			}
			return viol(CodeNotInEnum, "%s is not one of %s", show(raw), strings.Join(v.values, ", "))
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
		return out

	case typeInt:
		n, code, msg := v.parseInt(raw)
		if msg != "" {
			return viol(code, "%s", msg)
		}
		if v.minInt != nil && n.Cmp(v.minInt) < 0 {
			return viol(CodeOutOfRange, "%s is below min %s", v.showNum(raw), v.minInt)
		}
		if v.maxInt != nil && n.Cmp(v.maxInt) > 0 {
			return viol(CodeOutOfRange, "%s is above max %s", v.showNum(raw), v.maxInt)
		}
		return nil

	case typeFloat:
		bits := 64
		if v.goType.Kind() == reflect.Float32 {
			bits = 32
		}
		f, err := strconv.ParseFloat(raw, bits)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return viol(CodeInvalidType, "%s is not a finite number", show(raw))
		}
		if v.minFloat != nil && f < *v.minFloat {
			return viol(CodeOutOfRange, "%s is below min %s", v.showNum(raw), formatFloat(*v.minFloat))
		}
		if v.maxFloat != nil && f > *v.maxFloat {
			return viol(CodeOutOfRange, "%s is above max %s", v.showNum(raw), formatFloat(*v.maxFloat))
		}
		return nil

	case typeBool:
		if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false") {
			return nil
		}
		if _, err := strconv.ParseBool(raw); err != nil {
			return viol(CodeInvalidType, "%s is not a bool (true or false)", show(raw))
		}
		return nil

	case typeDuration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return viol(CodeInvalidType, "%s is not a duration such as 1m30s", show(raw))
		}
		if v.minDur != nil && d < *v.minDur {
			return viol(CodeOutOfRange, "%s is below min %s", v.showNum(raw), formatDuration(*v.minDur))
		}
		if v.maxDur != nil && d > *v.maxDur {
			return viol(CodeOutOfRange, "%s is above max %s", v.showNum(raw), formatDuration(*v.maxDur))
		}
		return nil

	case typeURL:
		u, err := url.Parse(raw)
		if err != nil || !urlRe.MatchString(raw) {
			return viol(CodeInvalidType, "%s is not a URL of the form scheme://...", show(raw))
		}
		if v.custom != nil {
			if err := v.custom(raw); err != nil {
				return viol(CodeInvalidType, "%s is not a valid %v%s", show(raw), v.goType, v.reason(err))
			}
		}
		if len(v.schemes) > 0 {
			for _, s := range v.schemes {
				if strings.EqualFold(u.Scheme, s) {
					return nil
				}
			}
			if v.secret {
				return viol(CodeInvalidScheme, "scheme is not one of %s", strings.Join(v.schemes, ", "))
			}
			return viol(CodeInvalidScheme, "scheme %q is not one of %s", u.Scheme, strings.Join(v.schemes, ", "))
		}
		return nil

	case typeList:
		items := strings.Split(raw, v.separator)
		for i, item := range items {
			if v.items == "int" {
				if _, _, msg := v.parseInt(item); msg != "" {
					return viol(CodeInvalidType, "item %d: %s", i, msg)
				}
			} else if v.custom != nil {
				if err := v.custom(item); err != nil {
					return viol(CodeInvalidType, "item %d: %s is not a valid %v%s", i, show(item), v.goType.Elem(), v.reason(err))
				}
			}
		}
		if v.minItems != nil && len(items) < *v.minItems {
			return viol(CodeTooFewItems, "has %s, below minItems %d", plural(len(items), "item"), *v.minItems)
		}
		if v.maxItems != nil && len(items) > *v.maxItems {
			return viol(CodeTooManyItems, "has %s, above maxItems %d", plural(len(items), "item"), *v.maxItems)
		}
		return nil

	case typeJSON:
		doc, err := decodeJSON([]byte(raw))
		if err != nil {
			return viol(CodeInvalidType, "is not valid JSON%s", v.reason(err))
		}
		var out []Violation
		for _, p := range v.schema.validate(doc) {
			out = append(out, viol(CodeSchemaMismatch, "%s", p)...)
		}
		if len(out) > 0 {
			return out
		}
		if err := v.custom(raw); err != nil {
			return viol(CodeSchemaMismatch, "does not bind to %v%s", v.jsonType, v.reason(err))
		}
		if err := validateValue(v.jsonType, []byte(raw)); err != nil {
			return viol(CodeSchemaMismatch, "%s", v.redact(err.Error()))
		}
		return nil
	}
	return nil
}

// parseInt parses an integer the way caarlos0/env does for the field's
// kind. On failure it returns a code and message.
func (v *varDecl) parseInt(raw string) (*big.Int, Code, string) {
	var err error
	if v.unsigned {
		_, err = strconv.ParseUint(raw, 10, v.intBits)
	} else {
		_, err = strconv.ParseInt(raw, 10, v.intBits)
	}
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return nil, CodeOutOfRange, fmt.Sprintf("%s is outside the range of %v", v.showNum(raw), v.goTypeOrElem())
		}
		return nil, CodeInvalidType, fmt.Sprintf("%s is not an integer", v.show(raw))
	}
	n, _ := new(big.Int).SetString(raw, 10)
	return n, "", ""
}

func (v *varDecl) goTypeOrElem() any {
	if v.typ == typeList {
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
