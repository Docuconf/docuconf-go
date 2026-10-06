package docuconf

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// jsonSchema is the subset of JSON Schema docuconf generates from Go
// types, for config files and json variables. The same schema is
// exported in the contract (the platform compiles it to CUE) and checked
// at boot, so both sides apply identical rules.
type jsonSchema struct {
	Type        string // object, array, string, integer, number, boolean, or "" for any
	Description string
	Format      string

	// object
	Properties []schemaProp
	Required   []string
	Closed     bool        // additionalProperties: false
	Additional *jsonSchema // map values

	// array
	Items              *jsonSchema
	MinItems, MaxItems *int

	// string
	Enum                 []string
	MinLength, MaxLength *int
	Pattern              *regexp.Regexp

	// number
	Minimum, Maximum string
}

type schemaProp struct {
	Name   string
	Schema *jsonSchema
}

var (
	timeType            = reflect.TypeOf(time.Time{})
	jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	rawMessageType      = reflect.TypeOf(json.RawMessage(nil))
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// schemaFor generates a JSON Schema for t, honouring json tags and
// docuconf constraint tags. docs, when set, supplies field descriptions.
func schemaFor(t reflect.Type, docs *docResolver) (*jsonSchema, error) {
	g := schemaGen{docs: docs, visiting: map[reflect.Type]bool{}}
	s := g.schema(t, nil)
	if len(g.problems) > 0 {
		return nil, fmt.Errorf("schema for %v: %s", t, strings.Join(g.problems, "; "))
	}
	return s, nil
}

type schemaGen struct {
	docs     *docResolver
	visiting map[reflect.Type]bool
	problems []string
}

func (g *schemaGen) schema(t reflect.Type, inline *structDocs) *jsonSchema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return &jsonSchema{Type: "string", Format: "date-time"}
	case t == rawMessageType:
		return &jsonSchema{}
	case reflect.PointerTo(t).Implements(jsonUnmarshalerType):
		return &jsonSchema{} // custom JSON form: accept anything
	case reflect.PointerTo(t).Implements(textUnmarshalerType) && t.Implements(textMarshalerType):
		return &jsonSchema{Type: "string"}
	}
	switch t.Kind() {
	case reflect.String:
		return &jsonSchema{Type: "string"}
	case reflect.Bool:
		return &jsonSchema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &jsonSchema{Type: "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return &jsonSchema{Type: "integer", Minimum: "0"}
	case reflect.Float32, reflect.Float64:
		return &jsonSchema{Type: "number"}
	case reflect.Interface:
		return &jsonSchema{}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 && t.Kind() == reflect.Slice {
			return &jsonSchema{Type: "string"} // base64, as encoding/json writes it
		}
		return &jsonSchema{Type: "array", Items: g.schema(t.Elem(), nil)}
	case reflect.Map:
		return &jsonSchema{Type: "object", Additional: g.schema(t.Elem(), nil)}
	case reflect.Struct:
		if g.visiting[t] {
			return &jsonSchema{} // recursive type: accept anything below this point
		}
		g.visiting[t] = true
		defer delete(g.visiting, t)
		s := &jsonSchema{Type: "object", Closed: true}
		g.fields(s, t, inline)
		return s
	}
	g.problems = append(g.problems, fmt.Sprintf("%v cannot be represented in JSON", t))
	return &jsonSchema{}
}

// fields adds t's fields to s, following encoding/json's rules for names,
// omitted fields and embedded structs.
func (g *schemaGen) fields(s *jsonSchema, t reflect.Type, inline *structDocs) {
	docs := inline
	if docs == nil && g.docs != nil {
		docs = g.docs.forType(t)
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts := splitTag(tag)
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				g.fields(s, ft, nil)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		var child *structDocs
		if docs != nil {
			child = docs.inline[f.Name]
		}
		fs := g.schema(f.Type, child)
		desc := f.Tag.Get("desc")
		if docs != nil && docs.docs[f.Name] != "" {
			desc = docs.docs[f.Name]
		}
		fs.Description = desc
		g.constraints(fs, f, t)
		optional := slices.Contains(opts, "omitempty") || slices.Contains(opts, "omitzero") || f.Type.Kind() == reflect.Pointer
		if !optional {
			s.Required = append(s.Required, name)
		}
		s.Properties = append(s.Properties, schemaProp{Name: name, Schema: fs})
	}
}

func (g *schemaGen) constraints(s *jsonSchema, f reflect.StructField, owner reflect.Type) {
	problem := func(format string, args ...any) {
		g.problems = append(g.problems, fmt.Sprintf("%v.%s: %s", owner, f.Name, fmt.Sprintf(format, args...)))
	}
	intTag := func(name string) *int {
		v, ok := f.Tag.Lookup(name)
		if !ok {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			problem("%s must be a non-negative integer", name)
			return nil
		}
		return &n
	}
	applies := func(name string, types ...string) bool {
		if _, ok := f.Tag.Lookup(name); !ok {
			return false
		}
		if !slices.Contains(types, s.Type) {
			problem("tag %s does not apply to a JSON %s", name, orAny(s.Type))
			return false
		}
		return true
	}
	if applies("minLength", "string") {
		s.MinLength = intTag("minLength")
	}
	if applies("maxLength", "string") {
		s.MaxLength = intTag("maxLength")
	}
	if applies("pattern", "string") {
		re, err := regexp.Compile(f.Tag.Get("pattern"))
		if err != nil {
			problem("pattern is not valid RE2: %v", err)
		}
		s.Pattern = re
	}
	if applies("values", "string") {
		s.Enum = splitList(f.Tag.Get("values"))
	}
	if applies("minItems", "array") {
		s.MinItems = intTag("minItems")
	}
	if applies("maxItems", "array") {
		s.MaxItems = intTag("maxItems")
	}
	for _, b := range []struct {
		tag string
		dst *string
	}{{"min", &s.Minimum}, {"max", &s.Maximum}} {
		if applies(b.tag, "integer", "number") {
			v := f.Tag.Get(b.tag)
			if _, ok := new(big.Float).SetString(v); !ok || (s.Type == "integer" && !isInteger(v)) {
				problem("%s must be a %s", b.tag, s.Type)
				continue
			}
			*b.dst = v
		}
	}
}

func orAny(t string) string {
	if t == "" {
		return "value of any type"
	}
	return t
}

func isInteger(s string) bool {
	_, ok := new(big.Int).SetString(s, 10)
	return ok
}

// toValue converts the schema to ordered data for the contract.
func (s *jsonSchema) toValue() obj {
	var o obj
	if s.Type != "" {
		o = o.add("type", s.Type)
	}
	if s.Description != "" {
		o = o.add("description", s.Description)
	}
	if s.Format != "" {
		o = o.add("format", s.Format)
	}
	if len(s.Enum) > 0 {
		o = o.add("enum", stringsToList(s.Enum))
	}
	if s.MinLength != nil {
		o = o.add("minLength", int64(*s.MinLength))
	}
	if s.MaxLength != nil {
		o = o.add("maxLength", int64(*s.MaxLength))
	}
	if s.Pattern != nil {
		o = o.add("pattern", s.Pattern.String())
	}
	if s.Minimum != "" {
		o = o.add("minimum", json.Number(s.Minimum))
	}
	if s.Maximum != "" {
		o = o.add("maximum", json.Number(s.Maximum))
	}
	if s.Items != nil {
		o = o.add("items", s.Items.toValue())
	}
	if s.MinItems != nil {
		o = o.add("minItems", int64(*s.MinItems))
	}
	if s.MaxItems != nil {
		o = o.add("maxItems", int64(*s.MaxItems))
	}
	if s.Type == "object" && s.Additional == nil {
		if len(s.Required) > 0 {
			o = o.add("required", stringsToList(s.Required))
		}
		o = o.add("additionalProperties", !s.Closed)
		var props obj
		for _, p := range s.Properties {
			props = props.add(p.Name, p.Schema.toValue())
		}
		o = o.add("properties", props)
	}
	if s.Additional != nil {
		o = o.add("additionalProperties", s.Additional.toValue())
	}
	return o
}

func stringsToList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// validate checks a decoded JSON document (numbers as json.Number) and
// returns one message per problem. Messages never include values, since
// the document may be secret.
func (s *jsonSchema) validate(doc any) []string {
	var out []string
	s.check(doc, "", &out)
	return out
}

func (s *jsonSchema) check(v any, at string, out *[]string) {
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if at != "" {
			msg = "at " + at + ": " + msg
		}
		*out = append(*out, msg)
	}
	switch s.Type {
	case "":
		return
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			fail("expected an object, got %s", jsonKind(v))
			return
		}
		for _, r := range s.Required {
			if _, ok := m[r]; !ok {
				fail("missing required property %q", r)
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if s.Additional != nil {
				s.Additional.check(m[k], joinPath(at, k), out)
				continue
			}
			i := slices.IndexFunc(s.Properties, func(p schemaProp) bool { return p.Name == k })
			if i < 0 {
				if s.Closed {
					fail("property %q is not allowed", k)
				}
				continue
			}
			s.Properties[i].Schema.check(m[k], joinPath(at, k), out)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			fail("expected an array, got %s", jsonKind(v))
			return
		}
		if s.MinItems != nil && len(a) < *s.MinItems {
			fail("has %s, below minItems %d", plural(len(a), "item"), *s.MinItems)
		}
		if s.MaxItems != nil && len(a) > *s.MaxItems {
			fail("has %s, above maxItems %d", plural(len(a), "item"), *s.MaxItems)
		}
		for i, x := range a {
			s.Items.check(x, fmt.Sprintf("%s[%d]", at, i), out)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			fail("expected a string, got %s", jsonKind(v))
			return
		}
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, str) {
			fail("is not one of %s", strings.Join(s.Enum, ", "))
		}
		n := len([]rune(str))
		if s.MinLength != nil && n < *s.MinLength {
			fail("is %d characters, below minLength %d", n, *s.MinLength)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			fail("is %d characters, above maxLength %d", n, *s.MaxLength)
		}
		if s.Pattern != nil && !s.Pattern.MatchString(str) {
			fail("does not match pattern %s", s.Pattern)
		}
	case "integer", "number":
		num, ok := v.(json.Number)
		if !ok {
			fail("expected %s, got %s", article(s.Type), jsonKind(v))
			return
		}
		f, ok := new(big.Float).SetString(num.String())
		if !ok {
			fail("is not a number")
			return
		}
		if s.Type == "integer" && !f.IsInt() {
			fail("expected an integer, got a fraction")
			return
		}
		if s.Minimum != "" {
			lo, _ := new(big.Float).SetString(s.Minimum)
			if f.Cmp(lo) < 0 {
				fail("is below minimum %s", s.Minimum)
			}
		}
		if s.Maximum != "" {
			hi, _ := new(big.Float).SetString(s.Maximum)
			if f.Cmp(hi) > 0 {
				fail("is above maximum %s", s.Maximum)
			}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			fail("expected a boolean, got %s", jsonKind(v))
		}
	}
}

func joinPath(at, k string) string {
	if at == "" {
		return k
	}
	return at + "." + k
}

func article(t string) string {
	if t == "integer" {
		return "an integer"
	}
	return "a number"
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	}
	return fmt.Sprintf("%T", v)
}
