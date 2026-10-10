package docuconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Profiles and config-file overlays (SPEC §4.4, §4.7) in contract-first
// mode. The Go host library reads only the environment, so a declared
// struct has neither; a contract written for another host may have both,
// and LoadContract layers them as that host would: the variable's
// default, then the selected profile's default, then an overlay, then the
// environment.

// profilesDecl is a contract's profiles block, with each profile default
// typed and checked against its variable.
type profilesDecl struct {
	selector string
	def      string
	defaults map[string]map[string]any // profile -> variable -> typed value
}

// overlayDecl is one entry of a contract's overlays.
type overlayDecl struct {
	name, format, path, keySeparator, reload string
}

// watchedOverlays returns a problem for each overlay declared reload
// "watch". LoadContract returns the values it read, once, so it cannot
// keep a promise to reload an overlay (SPEC §11.2, item 8).
func watchedOverlays(overlays []*overlayDecl) []string {
	var problems []string
	for _, ov := range overlays {
		if ov.reload == "watch" {
			problems = append(problems, fmt.Sprintf("overlay %s: reload watch is not supported in contract-first mode, which reads an overlay once; declare reload restart, or load the overlay with the host that reloads it", ov.name))
		}
	}
	return problems
}

// layer is a variable's value from below the environment: a profile
// default, already typed, or an overlay value, which is checked like an
// environment value.
type layer struct {
	source string
	typed  any  // a profile default
	fixed  bool // typed holds the value
	raw    string
	items  []string
	isList bool
	bad    bool // the overlay value was already reported
}

// contractProfiles parses a contract's profiles block (SPEC §4.4).
func contractProfiles(x any, vars []*varDecl, problemf func(string, ...any)) *profilesDecl {
	o, ok := x.(map[string]any)
	if !ok {
		problemf("profiles must be an object")
		return nil
	}
	p := &profilesDecl{defaults: map[string]map[string]any{}}
	for k := range o {
		if k != "selector" && k != "default" && k != "defaults" {
			problemf("profiles: unknown field %s", k)
		}
	}
	p.selector, _ = o["selector"].(string)
	if byName(vars, p.selector) == nil {
		problemf("profiles.selector %q must be a declared variable", p.selector)
	}
	var isStr bool
	if p.def, isStr = o["default"].(string); !isStr {
		problemf("profiles.default must be a string")
	}
	defs, _ := o["defaults"].(map[string]any)
	if _, ok := o["defaults"]; ok && defs == nil {
		problemf("profiles.defaults must be an object")
	}
	for name, m := range defs {
		values, ok := m.(map[string]any)
		if !ok {
			problemf("profiles.defaults.%s must be an object", name)
			continue
		}
		p.defaults[name] = map[string]any{}
		for n, val := range values {
			v := byName(vars, n)
			switch {
			case v == nil:
				problemf("profiles.defaults.%s: %s is not a declared variable", name, n)
				continue
			case v.secret:
				problemf("profiles.defaults.%s: %s is secret, and a secret has no value in a config file", name, n)
				continue
			}
			typed, msg := v.typedDefault(val)
			if msg != "" {
				problemf("profiles.defaults.%s: %s %s", name, n, msg)
				continue
			}
			p.defaults[name][n] = typed
		}
	}
	return p
}

// contractOverlays parses a contract's overlays (SPEC §4.7), sorted by
// name.
func contractOverlays(x any, problemf func(string, ...any)) []*overlayDecl {
	o, ok := x.(map[string]any)
	if !ok {
		problemf("overlays must be an object")
		return nil
	}
	var out []*overlayDecl
	for _, name := range slices.Sorted(maps.Keys(o)) {
		m, ok := o[name].(map[string]any)
		if !ok {
			problemf("overlay %s: must be an object", name)
			continue
		}
		fail := func(format string, args ...any) { problemf("overlay %s: %s", name, fmt.Sprintf(format, args...)) }
		if !inputNameRe.MatchString(name) {
			fail("name must be a DNS label matching %s", inputNameRe)
		}
		for k := range m {
			if !slices.Contains([]string{"name", "description", "format", "path", "keySeparator", "reload"}, k) {
				fail("unknown field %s", k)
			}
		}
		ov := &overlayDecl{name: name}
		ov.format, _ = m["format"].(string)
		if !slices.Contains([]string{"json", "yaml", "toml"}, ov.format) {
			fail("format must be json, yaml or toml")
		}
		ov.path, _ = m["path"].(string)
		if !absPathRe.MatchString(ov.path) || dotSegRe.MatchString(ov.path) ||
			strings.Contains(ov.path, "//") || strings.HasSuffix(ov.path, "/") {
			fail("path %q must be absolute and normalised", ov.path)
		}
		ov.keySeparator, _ = m["keySeparator"].(string)
		if ov.keySeparator != ":" && ov.keySeparator != "." {
			fail(`keySeparator must be ":" or "."`)
		}
		ov.reload = "restart"
		if r, ok := m["reload"]; ok {
			if r != "restart" && r != "watch" {
				fail("reload must be restart or watch")
			}
			ov.reload, _ = r.(string)
		}
		out = append(out, ov)
	}
	return out
}

func byName(vars []*varDecl, name string) *varDecl {
	for _, v := range vars {
		if v.name == name {
			return v
		}
	}
	return nil
}

// selected is the profile in effect: the selector's value when the
// environment sets it, as #Validate reads it, or profiles.default.
func (p *profilesDecl) selected(vars []*varDecl, environ map[string]string) string {
	raw, ok := environ[p.selector]
	if v := byName(vars, p.selector); ok && (raw != "" || v.typ == typeString) {
		return raw
	}
	return p.def
}

// loadLayers returns, for each variable a profile or an overlay sets, the
// value of the highest such layer, and the violations of the overlay
// files themselves. root is DOCUCONF_FILE_ROOT.
func loadLayers(c *contractDecl, environ map[string]string, root string, logger *slog.Logger) (map[string]layer, []Violation) {
	layers := map[string]layer{}
	if c.profiles != nil {
		name := c.profiles.selected(c.vars, environ)
		for n, typed := range c.profiles.defaults[name] {
			layers[n] = layer{source: "profile " + name, typed: typed, fixed: true}
		}
	}
	var viols []Violation
	fromOverlay := map[string]string{}
	for _, ov := range c.overlays {
		p := ov.path
		if root != "" {
			p = filepath.Join(root, p)
		}
		fail := func(code Code, format string, args ...any) {
			viols = append(viols, Violation{Input: ov.name, Code: code, Message: fmt.Sprintf(format, args...)})
		}
		data, err := os.ReadFile(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue // an overlay is optional
		case err != nil:
			fail(CodeFileUnreadable, "%s: %v", p, err)
			continue
		}
		data = bytes.TrimPrefix(data, utf8BOM)
		j, ferr := structuredToJSON(ov.format, data)
		if ferr != nil {
			fail(CodeFileMalformed, "%s %s: %v", p, ferr.Error(), ferr.Unwrap())
			continue
		}
		doc, err := decodeJSON(j)
		if err != nil {
			fail(CodeFileMalformed, "%s is not valid JSON: %v", p, err)
			continue
		}
		obj, ok := doc.(map[string]any)
		if !ok {
			fail(CodeFileMalformed, "%s does not hold an object", p)
			continue
		}
		for _, v := range c.vars {
			if v.configKey == "" || (c.profiles != nil && v.name == c.profiles.selector) {
				continue
			}
			val, found := lookupKey(obj, strings.Split(v.configKey, ov.keySeparator))
			if !found || val == nil {
				continue // null is unset
			}
			if prev, dup := fromOverlay[v.name]; dup {
				logger.Warn("docuconf: variable is set in two overlays; the first wins", "name", v.name, "overlay", prev, "ignored", ov.name)
				continue
			}
			fromOverlay[v.name] = ov.name
			if v.secret {
				// Never print it: the value is secret material in a ConfigMap.
				viols = append(viols, Violation{Input: v.name, Code: CodeInvalidType,
					Message: fmt.Sprintf("is secret, but overlay %s sets it at %s; supply secrets through the environment", ov.name, v.configKey)})
				layers[v.name] = layer{source: "overlay " + ov.name, bad: true}
				continue
			}
			l, msg := overlayValue(v, val)
			if msg != "" {
				viols = append(viols, Violation{Input: v.name, Code: CodeInvalidType, Message: fmt.Sprintf("overlay %s, at %s: %s", ov.name, v.configKey, msg)})
				layers[v.name] = layer{source: "overlay " + ov.name, bad: true}
				continue
			}
			l.source = "overlay " + ov.name
			layers[v.name] = l
		}
	}
	return layers, viols
}

// lookupKey finds the value at a key path in a decoded document. Keys
// match exactly, as #Render writes them.
func lookupKey(doc map[string]any, parts []string) (any, bool) {
	var cur any = doc
	for _, k := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// overlayValue converts a native overlay value to the wire string it
// stands for (SPEC §4.7), which is then checked like an environment
// value: a string as it is, a number in decimal, a bool as true or false,
// a list item by item, and a json variable's value as compact JSON.
func overlayValue(v *varDecl, val any) (layer, string) {
	switch v.typ {
	case typeJSON:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(val); err != nil {
			return layer{}, err.Error()
		}
		return layer{raw: strings.TrimSuffix(buf.String(), "\n")}, ""
	case typeList, typeKeySet:
		arr, ok := val.([]any)
		if !ok {
			return layer{}, fmt.Sprintf("is %s, not a list", jsonKind(val))
		}
		items := make([]string, len(arr))
		for i, x := range arr {
			s, ok := scalarText(x)
			if !ok {
				return layer{}, fmt.Sprintf("item %d is %s, not a scalar", i, jsonKind(x))
			}
			items[i] = s
		}
		return layer{items: items, isList: true}, ""
	}
	s, ok := scalarText(val)
	if !ok {
		return layer{}, fmt.Sprintf("is %s, not a scalar", jsonKind(val))
	}
	return layer{raw: s}, ""
}

// scalarText writes a native scalar as the env value it stands for. A
// number with an integral value is written as an integer (8080.0 is
// 8080), any other in shortest round-trip decimal.
func scalarText(x any) (string, bool) {
	switch x := x.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case json.Number:
		if n, ok := new(big.Int).SetString(string(x), 10); ok {
			return n.String(), true
		}
		f, err := x.Float64()
		if err != nil {
			return string(x), true // the parse reports it
		}
		if f == math.Trunc(f) && math.Abs(f) < 1<<63 {
			return strconv.FormatInt(int64(f), 10), true
		}
		return strconv.FormatFloat(f, 'g', -1, 64), true
	}
	return "", false
}
