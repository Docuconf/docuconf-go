package docuconf

import (
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
)

// Redacted is what docuconf prints in place of a secret value.
const redacted = "***"

// Secret is a string that never prints its value. A field of type Secret
// is a secret variable without a secret tag, and caarlos0/env parses it
// like a string:
//
//	DatabaseURL docuconf.Secret `env:"DATABASE_URL,required" schemes:"postgres"`
//
// fmt (every verb, including %v, %+v and %#v), log/slog and
// encoding/json all print *** instead of the value, so logging the whole
// configuration struct is safe. Call Reveal where the value is used:
//
//	db, err := sql.Open("pgx", cfg.DatabaseURL.Reveal())
type Secret string

var secretType = reflect.TypeOf(Secret(""))

// Reveal returns the secret value.
func (s Secret) Reveal() string { return string(s) }

// String returns "***".
func (s Secret) String() string { return redacted }

// GoString returns docuconf.Secret("***").
func (s Secret) GoString() string { return `docuconf.Secret("***")` }

// Format prints "***" for every verb, so that a struct holding a Secret
// is safe to print with %v, %+v and %#v.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		io.WriteString(f, s.GoString())
		return
	}
	io.WriteString(f, redacted)
}

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON encodes the secret as "***".
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText encodes the secret as "***", for YAML, TOML and other
// text encoders.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Redacted returns the variables of configuration struct cfg (a struct or
// a pointer to one), keyed by environment variable name, with the value
// of every secret variable replaced by "***". It is for logging the
// configuration or serving it from a debug endpoint:
//
//	json.NewEncoder(w).Encode(docuconf.Redacted(cfg))
//
// It uses the same declaration as Parse, including the struct's
// DocuconfOptions, so a field marked secret:"true" is redacted even when
// its type is a plain string. Unset optional pointers are nil. It returns
// nil if cfg is not a configuration struct.
func Redacted(cfg any) map[string]any {
	rv := reflect.ValueOf(cfg)
	for rv.Kind() == reflect.Pointer && !rv.IsNil() {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	opts := typeOptions(rv.Type())
	d, err := declare(rv.Type(), declOptions{prefix: opts.Prefix, funcMap: opts.FuncMap})
	if d == nil && err != nil {
		return nil
	}
	out := make(map[string]any, len(d.vars))
	for _, v := range d.vars {
		if v.secret {
			out[v.name] = redacted
			continue
		}
		f, err := rv.FieldByIndexErr(v.index)
		if err != nil {
			out[v.name] = nil
			continue
		}
		if f.Kind() == reflect.Pointer {
			if f.IsNil() {
				out[v.name] = nil
				continue
			}
			f = f.Elem()
		}
		val := f.Interface()
		if s, ok := val.(fmt.Stringer); ok && v.typ == typeDuration {
			val = s.String()
		}
		out[v.name] = val
	}
	return out
}

// LogValue returns configuration struct cfg as a slog group of its
// variables, with secret values replaced by "***" (see Redacted):
//
//	slog.Info("config loaded", "config", docuconf.LogValue(cfg))
func LogValue(cfg any) slog.Value {
	m := Redacted(cfg)
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	slices.Sort(names)
	attrs := make([]slog.Attr, 0, len(names))
	for _, k := range names {
		attrs = append(attrs, slog.Any(k, m[k]))
	}
	return slog.GroupValue(attrs...)
}

// LogValue implements slog.LogValuer: the violations as a list of
// "INPUT: message (code)" strings, which never include secret values.
func (e *ValidationError) LogValue() slog.Value {
	lines := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		lines[i] = v.String()
	}
	return slog.GroupValue(
		slog.Int("count", len(e.Violations)),
		slog.String("violations", strings.Join(lines, "; ")),
	)
}

// errNotStruct explains a type parameter that is not a struct type.
func errNotStruct(fn string, t reflect.Type) error {
	if t != nil && t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct {
		return fmt.Errorf("docuconf.%s[T]: T must be a struct type, got %v; use %s[%v]", fn, t, fn, t.Elem())
	}
	return fmt.Errorf("docuconf.%s[T]: T must be a struct type, got %v", fn, t)
}
