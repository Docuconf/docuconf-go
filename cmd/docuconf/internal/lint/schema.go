package lint

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// Schema is terraform providers schema -json: the attribute types the
// linter checks an unknown value's origin against (rule
// origin_type_mismatch, spec/IAC.md 7.2).
type Schema struct {
	FormatVersion   string `json:"format_version"`
	ProviderSchemas map[string]struct {
		ResourceSchemas   map[string]struct{ Block schemaBlock } `json:"resource_schemas"`
		DataSourceSchemas map[string]struct{ Block schemaBlock } `json:"data_source_schemas"`
	} `json:"provider_schemas"`
}

type schemaBlock struct {
	Attributes map[string]schemaAttr `json:"attributes"`
	BlockTypes map[string]struct {
		NestingMode string      `json:"nesting_mode"`
		Block       schemaBlock `json:"block"`
	} `json:"block_types"`
}

type schemaAttr struct {
	Type       json.RawMessage `json:"type"`
	NestedType *struct {
		Attributes  map[string]schemaAttr `json:"attributes"`
		NestingMode string                `json:"nesting_mode"`
	} `json:"nested_type"`
}

// ReadSchema reads terraform providers schema -json.
func ReadSchema(file string) (*Schema, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var s Schema
	if err := json.Unmarshal(b, &s); err != nil || s.FormatVersion == "" || s.ProviderSchemas == nil {
		return nil, fmt.Errorf("%s: not a provider schema (terraform providers schema -json)", file)
	}
	return &s, nil
}

// Resource is a resource or data source declared in the configuration.
type Resource struct {
	Type, Mode, Provider string
}

// ctyType is a type in Terraform's JSON form: "string", ["list", T],
// ["object", {k: T}], ...
type ctyType struct {
	kind string // string, number, bool, dynamic, list, set, map, tuple, object
	elem json.RawMessage
}

func parseType(raw json.RawMessage) (ctyType, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return ctyType{kind: s}, true
	}
	var pair []json.RawMessage
	if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
		return ctyType{}, false
	}
	if json.Unmarshal(pair[0], &s) != nil {
		return ctyType{}, false
	}
	return ctyType{kind: s, elem: pair[1]}, true
}

// collection reports whether the type holds several values.
func (t ctyType) collection() bool {
	switch t.kind {
	case "list", "set", "map", "tuple", "object":
		return true
	}
	return false
}

// name is the type as a message names it.
func (t ctyType) name() string { return t.kind }

var refSegment = regexp.MustCompile(`\[[^\]]*\]|[^.\[]+`)

var kubeName = regexp.MustCompile(`\.metadata(\[[^\]]*\])?\.name$`)

// resolve finds the type of a reference such as aws_db_instance.orders.port
// or data.aws_ssm_parameter.p.value, declared in scope.
func (p *Plan) resolve(scope, ref string) (ctyType, string, bool) {
	if p.Schema == nil {
		return ctyType{}, "", false
	}
	segs := refSegment.FindAllString(ref, -1)
	mode := "managed"
	if len(segs) > 0 && segs[0] == "data" {
		mode, segs = "data", segs[1:]
	}
	if len(segs) < 3 {
		return ctyType{}, "", false
	}
	switch segs[0] {
	case "var", "local", "module", "each", "count", "path", "terraform", "self":
		return ctyType{}, "", false
	}
	typ, name, rest := segs[0], segs[1], segs[2:]
	if len(rest) > 0 && strings.HasPrefix(rest[0], "[") {
		rest = rest[1:] // the instance key of count or for_each
	}
	if len(rest) == 0 {
		return ctyType{}, "", false
	}
	addr := typ + "." + name
	if mode == "data" {
		addr = "data." + addr
	}
	r, ok := p.Resources[join(scope, addr)]
	if !ok {
		return ctyType{}, "", false
	}
	ps, ok := p.Schema.ProviderSchemas[r.Provider]
	if !ok {
		return ctyType{}, "", false
	}
	var block schemaBlock
	if mode == "data" {
		s, ok := ps.DataSourceSchemas[typ]
		if !ok {
			return ctyType{}, "", false
		}
		block = s.Block
	} else {
		s, ok := ps.ResourceSchemas[typ]
		if !ok {
			return ctyType{}, "", false
		}
		block = s.Block
	}
	t, ok := blockPath(block, rest)
	return t, addr + "." + strings.Join(rest, "."), ok
}

func blockPath(b schemaBlock, path []string) (ctyType, bool) {
	if len(path) == 0 {
		return ctyType{kind: "object"}, true
	}
	if a, ok := b.Attributes[path[0]]; ok {
		return attrPath(a, path[1:])
	}
	bt, ok := b.BlockTypes[path[0]]
	if !ok {
		return ctyType{}, false
	}
	rest := path[1:]
	if bt.NestingMode != "single" && bt.NestingMode != "group" {
		if len(rest) == 0 || !strings.HasPrefix(rest[0], "[") {
			kind := bt.NestingMode
			if kind != "set" && kind != "map" {
				kind = "list"
			}
			return ctyType{kind: kind}, true
		}
		rest = rest[1:]
	}
	return blockPath(bt.Block, rest)
}

func attrPath(a schemaAttr, path []string) (ctyType, bool) {
	if a.NestedType != nil {
		nb := schemaBlock{Attributes: a.NestedType.Attributes}
		mode := a.NestedType.NestingMode
		if mode != "single" {
			if len(path) == 0 {
				return ctyType{kind: mode}, mode == "list" || mode == "set" || mode == "map"
			}
			if strings.HasPrefix(path[0], "[") || mode == "map" {
				path = path[1:]
			}
		}
		return blockPath(nb, path)
	}
	t, ok := parseType(a.Type)
	if !ok {
		return ctyType{}, false
	}
	return typePath(t, path)
}

func typePath(t ctyType, path []string) (ctyType, bool) {
	if len(path) == 0 || t.kind == "dynamic" {
		return t, true
	}
	switch t.kind {
	case "list", "set", "map":
		e, ok := parseType(t.elem)
		if !ok {
			return ctyType{}, false
		}
		return typePath(e, path[1:]) // an index, or a map key
	case "object":
		var attrs map[string]json.RawMessage
		if json.Unmarshal(t.elem, &attrs) != nil {
			return ctyType{}, false
		}
		e, ok := parseType(attrs[path[0]])
		if !ok {
			return ctyType{}, false
		}
		return typePath(e, path[1:])
	}
	return ctyType{kind: "dynamic"}, true
}

// originType is the type of the one attribute an argument's expression
// refers to; ok is false when the linter cannot tell: no schema, no
// attribute, or several (an interpolation of two references, whose type
// is a string the references do not give).
func (m *Module) originType(name string) (ctyType, string, bool) {
	if m.plan == nil || m.plan.Schema == nil {
		return ctyType{}, "", false
	}
	x, ok := m.Args[name]
	if !ok || x.IsConstant {
		return ctyType{}, "", false
	}
	var best []string
	for _, r := range x.References {
		segs := refSegment.FindAllString(r, -1)
		switch {
		case len(segs) > len(best) && hasPrefix(segs, best):
			best = segs
		case hasPrefix(best, segs):
		default:
			return ctyType{}, "", false // two unrelated references
		}
	}
	if best == nil {
		return ctyType{}, "", false
	}
	ref := strings.ReplaceAll(strings.Join(best, "."), ".[", "[")
	// A Kubernetes object's name is what a config_map or secret source
	// refers to, never a value: the plan does not say which source the
	// argument uses, so such an origin is not typed.
	if kubeName.MatchString(ref) {
		return ctyType{}, "", false
	}
	t, at, ok := m.plan.resolve(m.Scope, ref)
	if !ok || t.kind == "dynamic" {
		return ctyType{}, "", false
	}
	return t, at, true
}

func hasPrefix(s, prefix []string) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// typeMismatch says why a value of type t cannot, or may not, satisfy v;
// "" when it can.
func typeMismatch(v terraform.Var, t ctyType) string {
	list := v.Type == "list"
	switch {
	case v.Type == "json":
		return ""
	case list && t.collection():
		return ""
	case list:
		return fmt.Sprintf("a %s cannot be a list", t.name())
	case t.collection():
		return fmt.Sprintf("a %s cannot be a %s", t.name(), v.Type)
	}
	switch v.Type {
	case "int", "float":
		switch t.kind {
		case "string":
			return "a string may hold a number, which only the apply-time precondition decides"
		case "bool":
			return "a bool cannot be a number"
		}
	case "bool":
		if t.kind == "string" {
			return "a string may hold true or false, which only the apply-time precondition decides"
		}
		if t.kind == "number" {
			return "a number cannot be a bool"
		}
	case "url", "duration":
		if t.kind != "string" {
			return fmt.Sprintf("a %s cannot be a %s", t.kind, v.Type)
		}
	case "enum":
		if t.kind == "string" {
			return ""
		}
		var want []string
		if t.kind == "bool" {
			want = []string{"true", "false"}
		}
		values := strs(v.Raw, "values")
		for _, w := range want {
			if !contains(values, w) {
				return fmt.Sprintf("a bool is %s, which is not one of %s", w, strings.Join(values, ", "))
			}
		}
		if t.kind == "number" {
			for _, x := range values {
				if regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`).MatchString(x) {
					return ""
				}
			}
			return fmt.Sprintf("a number is not one of %s", strings.Join(values, ", "))
		}
	}
	return ""
}

func strs(m map[string]any, k string) []string {
	l, _ := m[k].([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// checkOriginType is origin_type_mismatch for an input whose value is
// unknown at plan.
func checkOriginType(m *Module, v terraform.Var, where string) []Finding {
	t, at, ok := m.originType(v.Name)
	if !ok {
		return nil
	}
	why := typeMismatch(v, t)
	if why == "" {
		return nil
	}
	return []Finding{{Code: "origin_type_mismatch", Level: Warning, Module: m.Addr, Where: where, Input: v.Name,
		Message: fmt.Sprintf("%s: its value comes from %s, a %s attribute in the provider schema, and %s", v.Name, at, t.name(), why)}}
}
