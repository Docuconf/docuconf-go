package main

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// schema compares the JSON Schema of a json variable or config file
// structurally, keyword by keyword (SPEC §9). It understands the keywords
// SDKs' schema generators emit, and local $refs into $defs or
// definitions. Any other difference is reported as breaking: diff cannot
// prove it harmless.
func (d *differ) schema(name string, o, n obj) {
	os, okO := o["schema"]
	ns, okN := n["schema"]
	switch {
	case reflect.DeepEqual(os, ns):
		return
	case !okO:
		d.add(name, "schema-added", breaking, "schema added: existing content may not validate")
		return
	case !okN:
		d.add(name, "schema-removed", compatible, "schema removed")
		return
	}
	sd := &schemaDiffer{d: d, input: name, oldRoot: os, newRoot: ns, seen: map[[2]string]bool{}}
	sd.diff(os, ns, "", "#", "#")
	if sd.docs {
		d.add(name, "schema-docs-changed", compatible, "schema annotations changed (docs only)")
	}
}

type schemaDiffer struct {
	d                *differ
	input            string
	oldRoot, newRoot any
	seen             map[[2]string]bool // $ref pairs already compared
	docs             bool
}

func (s *schemaDiffer) tight(ptr, format string, args ...any) {
	s.d.add(s.input, "schema-tightened", breaking, "schema %s: %s", at(ptr), fmt.Sprintf(format, args...))
}

func (s *schemaDiffer) loose(ptr, format string, args ...any) {
	s.d.add(s.input, "schema-loosened", compatible, "schema %s: %s", at(ptr), fmt.Sprintf(format, args...))
}

func (s *schemaDiffer) note(ptr, format string, args ...any) {
	s.d.add(s.input, "schema-changed", notable, "schema %s: %s", at(ptr), fmt.Sprintf(format, args...))
}

func (s *schemaDiffer) unknown(ptr, keyword string) {
	s.d.add(s.input, "schema-unclassified", breaking, "schema %s: %s changed; schema changed in a way diff cannot classify", at(ptr), keyword)
}

func at(ptr string) string {
	if ptr == "" {
		return "/"
	}
	return ptr
}

// schemaAnnotations never affect what validates.
var schemaAnnotations = map[string]bool{
	"title": true, "description": true, "examples": true, "default": true, "$comment": true,
	"deprecated": true, "readOnly": true, "writeOnly": true, "$schema": true, "$id": true,
	"$anchor": true, "markdownDescription": true,
}

// Bounds: a lower bound tightens when raised, an upper one when lowered.
var schemaLower = []string{"minimum", "exclusiveMinimum", "minLength", "minItems", "minProperties"}
var schemaUpper = []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems", "maxProperties"}

// diff compares two subschemas at ptr. oref and nref are the $refs each
// was last reached through, to stop on recursive types.
func (s *schemaDiffer) diff(o, n any, ptr, oref, nref string) {
	or, oFollow := asObj(o)["$ref"].(string)
	nr, nFollow := asObj(n)["$ref"].(string)
	o, okO := s.resolve(o, s.oldRoot)
	n, okN := s.resolve(n, s.newRoot)
	if !okO || !okN {
		s.unknown(ptr, "$ref")
		return
	}
	if oFollow {
		oref = or
	}
	if nFollow {
		nref = nr
	}
	if oFollow || nFollow {
		// A recursive type refers back to itself: compare each pair of
		// definitions once.
		key := [2]string{oref, nref}
		if s.seen[key] {
			return
		}
		s.seen[key] = true
	}
	if reflect.DeepEqual(o, n) {
		return
	}
	// Boolean schemas: true accepts anything, false nothing. An empty
	// object is true.
	ob, oIsBool := boolSchema(o)
	nb, nIsBool := boolSchema(n)
	switch {
	case oIsBool && nIsBool:
		if ob && !nb {
			s.tight(ptr, "now accepts nothing")
		} else if !ob && nb {
			s.loose(ptr, "now accepts anything")
		}
		return
	case oIsBool && ob:
		s.tight(ptr, "now constrained")
		return
	case oIsBool:
		s.loose(ptr, "now accepts some values")
		return
	case nIsBool && nb:
		s.loose(ptr, "no longer constrained")
		return
	case nIsBool:
		s.tight(ptr, "now accepts nothing")
		return
	}
	om, nm := asObj(o), asObj(n)
	if om == nil || nm == nil {
		s.unknown(ptr, "schema")
		return
	}

	for _, k := range unionKeys(om, nm) {
		ov, okO := om[k]
		nv, okN := nm[k]
		if reflect.DeepEqual(ov, nv) {
			continue
		}
		switch {
		case schemaAnnotations[k]:
			s.docs = true
		case k == "$defs" || k == "definitions" || k == "$ref":
			// Compared where they are referenced.
		case k == "type":
			s.types(ptr, ov, nv)
		case k == "required":
			added, removed := setDiff(asList(ov), asList(nv))
			if len(added) > 0 {
				s.tight(ptr, "required property %s added", strings.Join(added, ", "))
			}
			if len(removed) > 0 {
				s.loose(ptr, "property %s no longer required", strings.Join(removed, ", "))
			}
		case k == "properties":
			s.properties(ptr, om, nm, oref, nref)
		case k == "additionalProperties":
			// Absent means true. Properties added or removed are handled
			// with properties; this compares the open part itself.
			s.diff(orTrue(ov, okO), orTrue(nv, okN), ptr+"/additionalProperties", oref, nref)
		case k == "items":
			if _, tuple := ov.([]any); tuple {
				s.unknown(ptr, "items")
				continue
			}
			if _, tuple := nv.([]any); tuple {
				s.unknown(ptr, "items")
				continue
			}
			s.diff(orTrue(ov, okO), orTrue(nv, okN), ptr+"/items", oref, nref)
		case k == "enum":
			switch {
			case !okO:
				s.tight(ptr, "enum %s added", show(nv))
			case !okN:
				s.loose(ptr, "enum removed")
			default:
				added, removed := setDiff(asList(ov), asList(nv))
				if len(removed) > 0 {
					s.tight(ptr, "enum values removed: %s", strings.Join(removed, ", "))
				}
				if len(added) > 0 {
					s.loose(ptr, "enum values added: %s", strings.Join(added, ", "))
				}
			}
		case k == "const":
			if okN {
				s.tight(ptr, "const %s", show(nv))
			} else {
				s.loose(ptr, "const removed")
			}
		case k == "pattern" || k == "format":
			switch {
			case !okO:
				s.tight(ptr, "%s %s added", k, show(nv))
			case !okN:
				s.loose(ptr, "%s %s removed", k, show(ov))
			default:
				s.tight(ptr, "%s changed from %s to %s", k, show(ov), show(nv))
			}
		case k == "uniqueItems":
			if isTrue(nv) {
				s.tight(ptr, "items must now be unique")
			} else {
				s.loose(ptr, "items need no longer be unique")
			}
		case slices.Contains(schemaLower, k) || slices.Contains(schemaUpper, k):
			s.bound(ptr, k, slices.Contains(schemaLower, k), ov, okO, nv, okN)
		default:
			s.unknown(ptr, k)
		}
	}
}

func (s *schemaDiffer) bound(ptr, k string, lower bool, ov any, okO bool, nv any, okN bool) {
	switch {
	case !okO:
		s.tight(ptr, "%s %s added", k, show(nv))
		return
	case !okN:
		s.loose(ptr, "%s %s removed", k, show(ov))
		return
	}
	c, ok := compare(ov, nv, false)
	if !ok {
		s.unknown(ptr, k) // draft-04 boolean exclusiveMinimum, for example
		return
	}
	if (c < 0) == lower {
		s.tight(ptr, "%s changed from %s to %s", k, show(ov), show(nv))
	} else {
		s.loose(ptr, "%s changed from %s to %s", k, show(ov), show(nv))
	}
}

// types compares type keywords as sets; integer is a subset of number.
func (s *schemaDiffer) types(ptr string, ov, nv any) {
	toSet := func(v any) []string {
		switch t := v.(type) {
		case string:
			return []string{t}
		case []any:
			var out []string
			for _, x := range t {
				out = append(out, str(x))
			}
			return out
		case nil:
			return []string{"array", "boolean", "null", "number", "object", "string"}
		}
		return nil
	}
	covers := func(set []string, t string) bool {
		return slices.Contains(set, t) || t == "integer" && slices.Contains(set, "number")
	}
	ot, nt := toSet(ov), toSet(nv)
	if ot == nil || nt == nil {
		s.unknown(ptr, "type")
		return
	}
	narrower, wider := false, false
	for _, t := range ot {
		if !covers(nt, t) {
			narrower = true
		}
	}
	for _, t := range nt {
		if !covers(ot, t) {
			wider = true
		}
	}
	if narrower {
		s.tight(ptr, "type narrowed from %s to %s", typeString(ov), typeString(nv))
	} else if wider {
		s.loose(ptr, "type widened from %s to %s", typeString(ov), typeString(nv))
	}
}

func typeString(v any) string {
	if v == nil {
		return "any"
	}
	return show(v)
}

// properties compares declared properties. A property only one side
// declares is compared with the other side's additionalProperties, which
// is what governed that key there.
func (s *schemaDiffer) properties(ptr string, om, nm obj, oref, nref string) {
	op, np := asObj(om["properties"]), asObj(nm["properties"])
	oAP, okOAP := om["additionalProperties"]
	nAP, okNAP := nm["additionalProperties"]
	oAP, nAP = orTrue(oAP, okOAP), orTrue(nAP, okNAP)
	for _, p := range unionKeys(op, np) {
		ov, okO := op[p]
		nv, okN := np[p]
		sub := ptr + "/properties/" + p
		switch {
		case okO && okN:
			s.diff(ov, nv, sub, oref, nref)
		case okN:
			// A new property. Where the old schema was closed, it is new
			// room; where it was open, values may already set the key in
			// some other shape, so the change is notable.
			if b, isBool := boolSchema(oAP); isBool && b {
				s.note(sub, "property %q added; values that already set it must now match its schema", p)
			} else {
				s.diff(oAP, nv, sub, oref, nref)
			}
		default:
			s.diff(ov, nAP, sub, oref, nref)
		}
	}
}

// resolve follows a local $ref ("#/$defs/x", "#/definitions/x") when it is
// the only validation keyword beside $defs (a root schema generated from a
// named type is often just a $ref and its $defs). It returns false for a $ref it cannot
// follow.
func (s *schemaDiffer) resolve(v any, root any) (any, bool) {
	for range 32 {
		m := asObj(v)
		ref, ok := m["$ref"].(string)
		if !ok {
			return v, true
		}
		for k := range m {
			if k != "$ref" && k != "$defs" && k != "definitions" && !schemaAnnotations[k] {
				return v, false
			}
		}
		if !strings.HasPrefix(ref, "#/") {
			return v, false
		}
		target := root
		for _, part := range strings.Split(ref[2:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			t, ok := asObj(target)[part]
			if !ok {
				return v, false
			}
			target = t
		}
		v = target
	}
	return v, false
}

func boolSchema(v any) (value, ok bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case obj:
		if len(t) == 0 {
			return true, true
		}
		for k := range t {
			if !schemaAnnotations[k] {
				return false, false
			}
		}
		return true, true
	}
	return false, false
}

func orTrue(v any, ok bool) any {
	if !ok {
		return true
	}
	return v
}
