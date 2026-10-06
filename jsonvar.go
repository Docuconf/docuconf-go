package docuconf

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// JSON is a variable holding a structured value as compact JSON, such as
// a rate-limit object (contract type "json"). The contract carries a JSON
// Schema generated from T, so the platform checks the value against the
// type the app decodes into.
//
//	RateLimits docuconf.JSON[RateLimits] `env:"RATE_LIMITS"`
//
// T's fields may carry the same constraint tags as variables (min, max,
// minLength, maxLength, pattern, values, minItems, maxItems), and T may
// implement Validate() error for checks a schema cannot express.
type JSON[T any] struct {
	Value T
}

// UnmarshalText decodes the variable's JSON into Value. Unknown fields are
// rejected, matching the schema's additionalProperties: false.
func (j *JSON[T]) UnmarshalText(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		return err
	}
	j.Value = v
	return nil
}

func (JSON[T]) jsonValueType() reflect.Type { return reflect.TypeFor[T]() }

type jsonVar interface{ jsonValueType() reflect.Type }

var jsonVarType = reflect.TypeOf((*jsonVar)(nil)).Elem()

func isJSONType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.Implements(jsonVarType)
}

func jsonValueType(t reflect.Type) reflect.Type {
	return reflect.Zero(t).Interface().(jsonVar).jsonValueType()
}

// validator is implemented by types with checks a schema cannot express.
type validator interface{ Validate() error }

// validateValue decodes raw JSON into a new value of t and runs its
// Validate method, if it has one.
func validateValue(t reflect.Type, raw []byte) error {
	p := reflect.New(t)
	if _, ok := p.Interface().(validator); !ok {
		return nil
	}
	if err := json.Unmarshal(raw, p.Interface()); err != nil {
		return err
	}
	return p.Interface().(validator).Validate()
}
