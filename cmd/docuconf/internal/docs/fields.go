package docs

import (
	"encoding/json"
	"slices"
	"strings"
)

// A json variable's or config file's JSON Schema, as a table of fields
// (SPEC §14.3): one row per property, with nested objects flattened to
// dotted paths (database.host) and the items of a list of objects under
// path[] (routes[].match). A subtree the table cannot express (anyOf,
// $ref, a map's additionalProperties schema, ...) is one row of type
// "see schema", which carries that subtree's raw schema.

// seeSchema is the type of a row whose subtree is shown as raw schema.
const seeSchema = "see schema"

// tableKeywords are the JSON Schema keywords a row can show. A subtree
// with any other keyword falls back to its raw schema.
var tableKeywords = []string{
	"type", "properties", "required", "additionalProperties", "items",
	"description", "default", "enum", "const",
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"minLength", "maxLength", "pattern", "format",
	"minItems", "maxItems", "uniqueItems",
	// Annotations, which change nothing a row shows.
	"title", "examples", "$schema", "$id", "$comment", "$defs", "definitions",
	"readOnly", "writeOnly", "deprecated",
}

// fieldRules phrase a row's constraints, as rules do for inputs, under
// the JSON Schema keywords they come from.
var fieldRules = []rule{
	bounds("range", "minimum", "maximum", always, "", nil),
	{"exclusiveRange", []string{"exclusiveMinimum", "exclusiveMaximum"}, always, phraseExclusive},
	bounds("length", "minLength", "maxLength", always, "", characters),
	{"pattern", []string{"pattern"}, always, phrasePattern},
	{"format", []string{"format"}, always, func(f fields) string { return "format " + code(f.str("format")) }},
	bounds("itemCount", "minItems", "maxItems", always, "", items),
	{"uniqueItems", []string{"uniqueItems"}, always, func(f fields) string {
		if f["uniqueItems"] != true {
			return ""
		}
		return "no two items equal"
	}},
}

func always(string, string) bool { return true }

// phraseExclusive phrases exclusive bounds: above 0, below 1, or above 0
// and below 1.
func phraseExclusive(f fields) string {
	var parts []string
	if v, ok := f["exclusiveMinimum"].(json.Number); ok {
		parts = append(parts, "above "+v.String())
	}
	if v, ok := f["exclusiveMaximum"].(json.Number); ok {
		parts = append(parts, "below "+v.String())
	}
	return strings.Join(parts, " and ")
}

// schemaFields returns the field table of a JSON Schema. The root object's
// properties are its top-level rows; a root that is not an object is one
// row with an empty path. Properties are sorted by name, so the rows are
// in path order. A secret input's rows carry no defaults.
func schemaFields(schema any, secret bool) []Field {
	s, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	w := &fieldWalker{secret: secret, out: []Field{}}
	if !fallsBack(s) && schemaType(s) == "object" {
		w.properties("", s)
	} else {
		w.node("", s, true)
	}
	return w.out
}

type fieldWalker struct {
	secret bool
	out    []Field
}

// properties adds a row for each property of an object schema, and the
// rows below it.
func (w *fieldWalker) properties(prefix string, s fields) {
	props, _ := s["properties"].(map[string]any)
	required := s.strs("required")
	for _, name := range sortedKeys(props) {
		p, ok := props[name].(map[string]any)
		if !ok {
			continue // a boolean schema: nothing to show
		}
		w.node(prefix+name, p, slices.Contains(required, name))
	}
}

// node adds the row for one schema at path, then the rows of its
// properties or of its items.
func (w *fieldWalker) node(path string, s fields, required bool) {
	if fallsBack(s) {
		raw := raw(map[string]any(s))
		w.out = append(w.out, Field{Path: path, Type: seeSchema, Required: required, Description: s.str("description"),
			Constraints: []Constraint{}, Schema: raw})
		return
	}
	f := Field{Path: path, Type: typeLabel(s), Required: required, Description: s.str("description"), Constraints: []Constraint{}}
	if d, ok := s["default"]; ok && !w.secret {
		f.Default = raw(d)
	}
	if c, ok := s["const"]; ok {
		f.Enum = []json.RawMessage{raw(c)}
	}
	if e, ok := s["enum"].([]any); ok {
		for _, x := range e {
			f.Enum = append(f.Enum, raw(x))
		}
	}
	for _, r := range fieldRules {
		params := map[string]json.RawMessage{}
		for _, k := range r.fields {
			if v, ok := s[k]; ok {
				params[k] = raw(v)
			}
		}
		if len(params) == 0 {
			continue
		}
		if text := r.phrase(s); text != "" {
			f.Constraints = append(f.Constraints, Constraint{Rule: r.name, Params: params, Text: text})
		}
	}
	w.out = append(w.out, f)

	switch schemaType(s) {
	case "object":
		w.properties(path+".", s)
	case "array":
		it, ok := s["items"].(map[string]any)
		if !ok {
			return
		}
		item := fields(it)
		switch {
		case !fallsBack(item) && schemaType(item) == "object":
			w.properties(path+"[].", item)
		case fallsBack(item) || hasDetail(item):
			// Each item, when there is more to say than its type.
			w.node(path+"[]", item, true)
		}
	}
}

// fallsBack reports whether a schema needs its raw form: it uses a
// keyword the table cannot show, a map's additionalProperties schema, or
// tuple items.
func fallsBack(s fields) bool {
	for k := range s {
		if !slices.Contains(tableKeywords, k) {
			return true
		}
	}
	if _, isSchema := s["additionalProperties"].(map[string]any); isSchema {
		return true
	}
	if it, ok := s["items"]; ok {
		if _, isSchema := it.(map[string]any); !isSchema {
			return true
		}
	}
	for _, k := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
		if v, ok := s[k]; ok {
			if _, isNum := v.(json.Number); !isNum {
				return true // draft 4's boolean form
			}
		}
	}
	return false
}

// hasDetail reports whether an item schema says more than its type.
func hasDetail(s fields) bool {
	for k := range s {
		if k != "type" && k != "title" {
			return true
		}
	}
	return false
}

// schemaType is a schema's single type, inferred from properties or
// items when it has no type; "" when it has several or none.
func schemaType(s fields) string {
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		return ""
	}
	switch {
	case s.has("properties"):
		return "object"
	case s.has("items"):
		return "array"
	}
	return ""
}

// typeLabel phrases a schema's type: "string", "integer", "list of
// objects", "string or null".
func typeLabel(s fields) string {
	if ts, ok := s["type"].([]any); ok {
		var labels []string
		for _, t := range ts {
			if t, ok := t.(string); ok {
				labels = append(labels, t)
			}
		}
		return strings.Join(labels, " or ")
	}
	switch t := schemaType(s); t {
	case "":
		return "any"
	case "array":
		it, ok := s["items"].(map[string]any)
		if !ok || fallsBack(it) {
			return "list"
		}
		switch schemaType(it) {
		case "object":
			return "list of objects"
		case "array":
			return "list of lists"
		case "string", "integer", "number", "boolean":
			return "list of " + schemaType(it) + "s"
		}
		return "list"
	default:
		return t
	}
}
