// Package diff classifies the changes between two versions of a contract
// (spec/SPEC.md section 9): breaking, when values or file sources the
// platform sets today may no longer validate; notable, when nothing
// fails but the platform should know (a deprecation, a new default, a
// mount that moved inside the image); compatible otherwise.
package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The classes of change, most severe first.
const (
	Breaking   = "breaking"
	Notable    = "notable"
	Compatible = "compatible"
)

// Change is one difference between the contracts.
type Change struct {
	Class string `json:"class"`
	// Kind is what changed: var, file, overlay, profile or contract.
	Kind string `json:"kind"`
	// Input names the variable, file input, overlay or profile; empty for
	// the contract itself.
	Input string `json:"input,omitempty"`
	// Field is the field that changed; empty when the input was added or
	// removed.
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type obj = map[string]any

type differ struct{ out []Change }

func (d *differ) add(class, kind, input, field, format string, args ...any) {
	d.out = append(d.out, Change{Class: class, Kind: kind, Input: input, Field: field, Message: fmt.Sprintf(format, args...)})
}

// Contracts compares two contract documents, as JSON (platform.ContractJSON).
func Contracts(oldJSON, newJSON []byte) ([]Change, error) {
	// Numbers stay exact: an int64 bound does not fit a float64.
	decode := func(what string, data []byte) (obj, error) {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var x obj
		if err := dec.Decode(&x); err != nil {
			return nil, fmt.Errorf("the %s contract: %v", what, err)
		}
		return x, nil
	}
	o, err := decode("old", oldJSON)
	if err != nil {
		return nil, err
	}
	n, err := decode("new", newJSON)
	if err != nil {
		return nil, err
	}
	d := &differ{}
	d.contract(o, n)
	d.inputs("var", m(o["vars"]), m(n["vars"]), d.variable)
	d.inputs("file", m(o["files"]), m(n["files"]), d.file)
	d.overlays(m(o["overlays"]), m(n["overlays"]), m(n["vars"]))
	d.profiles(m(o["profiles"]), m(n["profiles"]), m(n["vars"]))
	rank := map[string]int{Breaking: 0, Notable: 1, Compatible: 2}
	kinds := map[string]int{"contract": 0, "var": 1, "file": 2, "overlay": 3, "profile": 4}
	sort.SliceStable(d.out, func(i, j int) bool {
		a, b := d.out[i], d.out[j]
		if rank[a.Class] != rank[b.Class] {
			return rank[a.Class] < rank[b.Class]
		}
		if kinds[a.Kind] != kinds[b.Kind] {
			return kinds[a.Kind] < kinds[b.Kind]
		}
		return a.Input < b.Input
	})
	return d.out, nil
}

func m(v any) obj {
	x, _ := v.(obj)
	return x
}

func s(v any) string {
	x, _ := v.(string)
	return x
}

func b(v any) bool {
	x, _ := v.(bool)
	return x
}

func show(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	j, _ := json.Marshal(v)
	return string(j)
}

func (d *differ) contract(o, n obj) {
	if a, b := s(m(o["metadata"])["name"]), s(m(n["metadata"])["name"]); a != b {
		d.add(Notable, "contract", "", "metadata.name", "the service is renamed from %s to %s: modules, components and policy generated for %s no longer match it", a, b, a)
	}
	if a, b := s(o["apiVersion"]), s(n["apiVersion"]); a != b {
		d.add(Notable, "contract", "", "apiVersion", "apiVersion changes from %s to %s (docs/VERSIONING.md)", a, b)
	}
}

func names(a, b obj) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// inputs compares the variables or the file inputs: added, removed, and
// changed ones.
func (d *differ) inputs(kind string, old, new obj, changed func(name string, o, n obj)) {
	what := map[string]string{"var": "variable", "file": "file input"}[kind]
	for _, name := range names(old, new) {
		o, n := m(old[name]), m(new[name])
		switch {
		case o == nil && b(n["required"]):
			d.add(Breaking, kind, name, "", "a required %s is added: the platform must supply it before deploying this version", what)
		case o == nil && condition(n) != "":
			d.add(Breaking, kind, name, "", "a %s required when %s is added: the platform must supply it wherever that holds before deploying this version", what, condition(n))
		case o == nil:
			d.add(Compatible, kind, name, "", "an optional %s is added", what)
		case n == nil:
			msg := fmt.Sprintf("the %s is removed: values that still set it fail the unknown-input check", what)
			if dep := m(o["deprecated"]); dep != nil {
				msg += "; it was deprecated (" + s(dep["message"]) + "), so a platform that heeded the warning is unaffected"
			} else {
				msg += "; deprecate it in a release first (SPEC.md section 4.2)"
			}
			d.add(Breaking, kind, name, "", "%s", msg)
		default:
			changed(name, o, n)
		}
	}
}

// condition is an input's requiredIf (SPEC.md section 4.2.1) as messages
// write it, "TLS_ENABLED is true", or "" when it has none.
func condition(in obj) string {
	for c, x := range m(in["requiredIf"]) {
		v, _ := json.Marshal(x)
		return fmt.Sprintf("%s is %s", c, v)
	}
	return ""
}

// common compares what variables and file inputs share.
func (d *differ) common(kind, name string, o, n obj) {
	oc, nc := condition(o), condition(n)
	switch {
	case !b(o["required"]) && b(n["required"]):
		d.add(Breaking, kind, name, "required", "becomes required: the platform must supply it")
	case b(o["required"]) && nc != "":
		d.add(Compatible, kind, name, "requiredIf", "becomes required only when %s", nc)
	case b(o["required"]) && !b(n["required"]):
		d.add(Compatible, kind, name, "required", "becomes optional")
	case oc == nc:
	case oc == "":
		d.add(Breaking, kind, name, "requiredIf", "becomes required when %s: the platform must supply it wherever that holds", nc)
	case nc == "":
		d.add(Compatible, kind, name, "requiredIf", "is no longer required when %s", oc)
	default:
		d.add(Breaking, kind, name, "requiredIf", "is now required when %s, instead of when %s: the platform must supply it wherever that holds", nc, oc)
	}
	switch od, nd := m(o["deprecated"]), m(n["deprecated"]); {
	case od == nil && nd != nil:
		msg := "is deprecated: the platform should stop setting it (" + s(nd["message"]) + ")"
		if r := s(nd["replacedBy"]); r != "" {
			msg += "; it is replaced by " + r
		}
		d.add(Notable, kind, name, "deprecated", "%s", msg)
	case od != nil && nd == nil:
		d.add(Compatible, kind, name, "deprecated", "is no longer deprecated")
	case !reflect.DeepEqual(od, nd):
		d.add(Notable, kind, name, "deprecated", "its deprecation notice changes to %s", show(nd["message"]))
	}
	for _, f := range []string{"description", "details", "group", "examples", "configKey"} {
		if f == "configKey" && kind == "file" {
			continue
		}
		if !reflect.DeepEqual(o[f], n[f]) {
			d.add(Compatible, kind, name, f, "its %s changes (docs only)", f)
		}
	}
}

func (d *differ) variable(name string, o, n obj) {
	const k = "var"
	if ot, nt := s(o["type"]), s(n["type"]); ot != nt {
		msg := fmt.Sprintf("its type changes from %s to %s: the value's shape changes", ot, nt)
		if ot == "list" && nt == "keySet" && s(o["items"]) == "string" && b(o["secret"]) {
			msg = "its type changes from a secret list of strings to keySet: the wire format and the Secret that holds the keys stay the same, but the app's declaration changes (SPEC.md section 6.1)"
		}
		d.add(Breaking, k, name, "type", "%s", msg)
		d.common(k, name, o, n)
		return
	}
	if !b(o["secret"]) && b(n["secret"]) {
		d.add(Breaking, k, name, "secret", "becomes secret: it must now come from a secret reference, never a literal value")
	} else if b(o["secret"]) && !b(n["secret"]) {
		d.add(Breaking, k, name, "secret", "is no longer secret: its value's sources change")
	}
	d.common(k, name, o, n)
	if !reflect.DeepEqual(o["default"], n["default"]) {
		switch {
		case o["default"] == nil:
			d.add(Notable, k, name, "default", "gets a default, %s: where the platform does not set it, the app's behaviour changes", show(n["default"]))
		case n["default"] == nil:
			d.add(Notable, k, name, "default", "loses its default, %s: where the platform does not set it, the app gets no value", show(o["default"]))
		default:
			d.add(Notable, k, name, "default", "its default changes from %s to %s: where the platform does not set it, the app's behaviour changes", show(o["default"]), show(n["default"]))
		}
	}
	typ := s(n["type"])
	cmp := numCmp
	if typ == "duration" {
		cmp = durCmp
	}
	d.lower(k, name, o, n, "min", cmp)
	d.upper(k, name, o, n, "max", cmp)
	d.lower(k, name, o, n, "minLength", numCmp)
	d.upper(k, name, o, n, "maxLength", numCmp)
	d.lower(k, name, o, n, "minItems", numCmp)
	d.upper(k, name, o, n, "maxItems", numCmp)
	d.lower(k, name, o, n, "itemMin", numCmp)
	d.upper(k, name, o, n, "itemMax", numCmp)
	d.lower(k, name, o, n, "itemMinLength", numCmp)
	d.upper(k, name, o, n, "itemMaxLength", numCmp)
	d.lower(k, name, o, n, "minKeys", numCmp)
	d.upper(k, name, o, n, "maxKeys", numCmp)
	d.lower(k, name, o, n, "keyMinLength", numCmp)
	d.upper(k, name, o, n, "keyMaxLength", numCmp)
	d.pattern(k, name, o, n)
	d.set(k, name, o, n, "values", "allowed values")
	d.set(k, name, o, n, "schemes", "allowed schemes")
	if !reflect.DeepEqual(o["items"], n["items"]) {
		d.add(Breaking, k, name, "items", "its items change from %s to %s", s(o["items"]), s(n["items"]))
	}
	for _, f := range []string{"encoding", "separator"} {
		if !reflect.DeepEqual(o[f], n[f]) && o[f] != nil && n[f] != nil {
			d.add(Notable, k, name, f, "its %s changes from %s to %s: the platform renders the new wire form, so values need no change, but an env set outside docuconf does", f, show(o[f]), show(n[f]))
		}
	}
	d.schema(k, name, m(o["schema"]), m(n["schema"]), o["schema"] != nil, n["schema"] != nil)
}

func (d *differ) file(name string, o, n obj) {
	const k = "file"
	if !b(o["secret"]) && b(n["secret"]) {
		d.add(Breaking, k, name, "secret", "becomes secret: its content must now come from a secret store, never inline or a ConfigMap")
	} else if b(o["secret"]) && !b(n["secret"]) {
		d.add(Compatible, k, name, "secret", "is no longer secret")
	}
	d.common(k, name, o, n)
	for _, f := range []string{"type", "path", "format"} {
		if !reflect.DeepEqual(o[f], n[f]) {
			d.add(Notable, k, name, f, "its %s changes from %s to %s: breaking for the app image only, since the platform renders the mount from the contract (SPEC.md section 9)", f, show(o[f]), show(n[f]))
		}
	}
	if !reflect.DeepEqual(o["pathEnv"], n["pathEnv"]) {
		d.add(Notable, k, name, "pathEnv", "the variable that carries its path changes from %s to %s", show(o["pathEnv"]), show(n["pathEnv"]))
	}
	switch or, nr := s(o["reload"]), s(n["reload"]); {
	case or == "restart" && nr == "watch":
		d.add(Compatible, k, name, "reload", "is now reloaded by the app (watch)")
	case or == "watch" && nr == "restart":
		d.add(Notable, k, name, "reload", "is no longer reloaded by the app: a changed source now needs a rollout (restart)")
	}
	d.upper(k, name, o, n, "maxSize", numCmp)
	d.lower(k, name, o, n, "minLength", numCmp)
	d.upper(k, name, o, n, "maxLength", numCmp)
	d.lower(k, name, o, n, "minCertificates", numCmp)
	d.lower(k, name, o, n, "minRemaining", durCmp)
	d.pattern(k, name, o, n)
	d.set(k, name, o, n, "keyAlgorithms", "allowed key algorithms")
	// dnsNames are names the certificate must cover: more is tighter.
	for _, x := range strList(n["dnsNames"]) {
		if !contains(strList(o["dnsNames"]), x) {
			d.add(Breaking, k, name, "dnsNames", "the certificate must also cover %s: the existing one may not", x)
		}
	}
	if !b(o["requireCA"]) && b(n["requireCA"]) {
		d.add(Breaking, k, name, "requireCA", "now requires ca.crt in the key pair")
	}
	if !reflect.DeepEqual(o["passwordVar"], n["passwordVar"]) {
		d.add(Notable, k, name, "passwordVar", "the variable that holds its password changes from %s to %s", show(o["passwordVar"]), show(n["passwordVar"]))
	}
	d.schema(k, name, m(o["schema"]), m(n["schema"]), o["schema"] != nil, n["schema"] != nil)
}

func (d *differ) overlays(old, new, vars obj) {
	for _, name := range names(old, new) {
		o, n := m(old[name]), m(new[name])
		switch {
		case o == nil:
			d.add(Compatible, "overlay", name, "", "an overlay is added")
		case n == nil:
			d.add(Breaking, "overlay", name, "", "the overlay is removed: values the platform writes into it are no longer accepted")
		default:
			for _, f := range []string{"path", "format", "keySeparator"} {
				if !reflect.DeepEqual(o[f], n[f]) {
					d.add(Notable, "overlay", name, f, "its %s changes from %s to %s: the platform renders the new file, so values need no change", f, show(o[f]), show(n[f]))
				}
			}
			switch or, nr := s(o["reload"]), s(n["reload"]); {
			case or == "restart" && nr == "watch":
				d.add(Compatible, "overlay", name, "reload", "is now reloaded by the app (watch)")
			case or == "watch" && nr == "restart":
				d.add(Notable, "overlay", name, "reload", "is no longer reloaded by the app: a change now rolls the pods")
			}
		}
	}
}

func (d *differ) profiles(o, n, vars obj) {
	if o == nil && n == nil {
		return
	}
	if o == nil {
		d.add(Notable, "profile", "", "", "profiles are added: values baked into the image now apply, selected by %s", s(n["selector"]))
		return
	}
	if n == nil {
		d.add(Notable, "profile", "", "", "profiles are removed: the image's per-environment values no longer apply")
		o = obj{"defaults": o["defaults"]}
		n = obj{}
	}
	if s(o["selector"]) != s(n["selector"]) && n["selector"] != nil {
		d.add(Breaking, "profile", "", "selector", "the profile is now selected by %s instead of %s: set the new variable where the old one was set", s(n["selector"]), s(o["selector"]))
	}
	if s(o["default"]) != s(n["default"]) && n["default"] != nil {
		d.add(Notable, "profile", "", "default", "the profile in effect when none is selected changes from %s to %s", s(o["default"]), s(n["default"]))
	}
	od, nd := m(o["defaults"]), m(n["defaults"])
	for _, p := range names(od, nd) {
		ov, nv := m(od[p]), m(nd[p])
		for _, v := range names(ov, nv) {
			_, had := ov[v]
			_, has := nv[v]
			switch {
			case had && !has && b(m(vars[v])["required"]):
				d.add(Breaking, "profile", p, v, "%s no longer supplies %s, which is required: the platform must now set it where it selects %s", p, v, p)
			case had && !has:
				d.add(Notable, "profile", p, v, "%s no longer supplies %s", p, v)
			case !had && has:
				d.add(Notable, "profile", p, v, "%s now supplies %s = %s", p, v, show(nv[v]))
			case !reflect.DeepEqual(ov[v], nv[v]):
				d.add(Notable, "profile", p, v, "%s's %s changes from %s to %s", p, v, show(ov[v]), show(nv[v]))
			}
		}
	}
}

// cmp compares two bounds: <0, 0, >0. ok is false when they cannot be
// compared.
type cmp func(a, b any) (int, bool)

func numCmp(a, b any) (int, bool) {
	x, ok1 := num(a)
	y, ok2 := num(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return x.Cmp(y), true
}

func num(v any) (*big.Float, bool) {
	switch x := v.(type) {
	case float64:
		return big.NewFloat(x), true
	case json.Number:
		f, ok := new(big.Float).SetString(string(x))
		return f, ok
	}
	return nil, false
}

func durCmp(a, b any) (int, bool) {
	x, err1 := time.ParseDuration(s(a))
	y, err2 := time.ParseDuration(s(b))
	if err1 != nil || err2 != nil {
		return 0, false
	}
	switch {
	case x < y:
		return -1, true
	case x > y:
		return 1, true
	}
	return 0, true
}

// lower compares a lower bound: raising or adding it is breaking.
func (d *differ) lower(kind, name string, o, n obj, f string, c cmp) {
	ov, nv := o[f], n[f]
	switch {
	case reflect.DeepEqual(ov, nv):
	case ov == nil:
		d.add(Breaking, kind, name, f, "gains %s %s: values below it no longer validate", f, show(nv))
	case nv == nil:
		d.add(Compatible, kind, name, f, "loses %s %s", f, show(ov))
	default:
		r, ok := c(ov, nv)
		switch {
		case !ok || r < 0:
			d.add(Breaking, kind, name, f, "its %s rises from %s to %s: values in between no longer validate", f, show(ov), show(nv))
		case r > 0:
			d.add(Compatible, kind, name, f, "its %s falls from %s to %s", f, show(ov), show(nv))
		}
	}
}

// upper compares an upper bound: lowering or adding it is breaking.
func (d *differ) upper(kind, name string, o, n obj, f string, c cmp) {
	ov, nv := o[f], n[f]
	switch {
	case reflect.DeepEqual(ov, nv):
	case ov == nil:
		d.add(Breaking, kind, name, f, "gains %s %s: values above it no longer validate", f, show(nv))
	case nv == nil:
		d.add(Compatible, kind, name, f, "loses %s %s", f, show(ov))
	default:
		r, ok := c(ov, nv)
		switch {
		case !ok || r > 0:
			d.add(Breaking, kind, name, f, "its %s falls from %s to %s: values in between no longer validate", f, show(ov), show(nv))
		case r < 0:
			d.add(Compatible, kind, name, f, "its %s rises from %s to %s", f, show(ov), show(nv))
		}
	}
}

func (d *differ) pattern(kind, name string, o, n obj) {
	op, np := s(o["pattern"]), s(n["pattern"])
	switch {
	case op == np:
	case op == "":
		d.add(Breaking, kind, name, "pattern", "gains pattern %s: values that do not match it no longer validate", strconv.Quote(np))
	case np == "":
		d.add(Compatible, kind, name, "pattern", "loses pattern %s", strconv.Quote(op))
	default:
		// Whether one RE2 pattern accepts all another does is not decidable
		// here; a changed pattern may reject what the old one accepted.
		d.add(Breaking, kind, name, "pattern", "its pattern changes from %s to %s: values the old one accepted may not match", strconv.Quote(op), strconv.Quote(np))
	}
}

// set compares a list of allowed things: removing one, or setting the
// list where there was none, is breaking.
func (d *differ) set(kind, name string, o, n obj, f, what string) {
	ol, nl := strList(o[f]), strList(n[f])
	if o[f] == nil && n[f] == nil {
		return
	}
	if o[f] == nil {
		d.add(Breaking, kind, name, f, "its %s are now limited to %s", what, strings.Join(nl, ", "))
		return
	}
	if n[f] == nil {
		d.add(Compatible, kind, name, f, "its %s are no longer limited", what)
		return
	}
	var removed, added []string
	for _, x := range ol {
		if !contains(nl, x) {
			removed = append(removed, x)
		}
	}
	for _, x := range nl {
		if !contains(ol, x) {
			added = append(added, x)
		}
	}
	if len(removed) > 0 {
		d.add(Breaking, kind, name, f, "%s no longer allowed: values that use it no longer validate", strings.Join(removed, ", ")+map[bool]string{true: " is", false: " are"}[len(removed) == 1])
	}
	if len(added) > 0 {
		d.add(Compatible, kind, name, f, "%s now allowed", strings.Join(added, ", ")+map[bool]string{true: " is", false: " are"}[len(added) == 1])
	}
}

func strList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func contains(l []string, x string) bool {
	for _, y := range l {
		if x == y {
			return true
		}
	}
	return false
}

// schema compares two JSON Schemas structurally (SPEC.md section 9): a
// schema that accepts less is breaking. What diff cannot compare is
// breaking too, saying so.
func (d *differ) schema(kind, name string, o, n obj, hadSchema, hasSchema bool) {
	switch {
	case !hadSchema && !hasSchema:
		return
	case !hadSchema:
		d.add(Breaking, kind, name, "schema", "gains a schema: values that do not match it no longer validate")
		return
	case !hasSchema:
		d.add(Compatible, kind, name, "schema", "loses its schema")
		return
	}
	var why []string
	tightened(o, n, "", &why)
	for _, w := range why {
		d.add(Breaking, kind, name, "schema", "its schema accepts less: %s", w)
	}
	if len(why) == 0 && !reflect.DeepEqual(o, n) {
		d.add(Compatible, kind, name, "schema", "its schema accepts more, or the same")
	}
}

var pointerEscape = strings.NewReplacer("~", "~0", "/", "~1")

// tightened lists how schema n accepts less than o, at path p.
func tightened(o, n obj, p string, why *[]string) {
	at := p
	if at == "" {
		at = "/"
	}
	report := func(format string, args ...any) { *why = append(*why, at+": "+fmt.Sprintf(format, args...)) }
	if reflect.DeepEqual(o, n) {
		return
	}
	// The type: a narrower set of types accepts less.
	if ot, nt := types(o["type"]), types(n["type"]); nt != nil {
		for _, t := range ot {
			if !contains(nt, t) && !(t == "integer" && contains(nt, "number")) {
				report("type %s is no longer accepted", t)
			}
		}
		if ot == nil {
			report("its type is now limited to %s", strings.Join(nt, ", "))
		}
	}
	for _, r := range strList(n["required"]) {
		if !contains(strList(o["required"]), r) {
			report("property %s is now required", r)
		}
	}
	oe, ne := o["enum"], n["enum"]
	if ne != nil {
		ol, nl := jsonList(oe), jsonList(ne)
		for _, x := range ol {
			if !contains(nl, x) {
				report("%s is no longer one of the allowed values", x)
			}
		}
		if oe == nil {
			report("its values are now limited to an enum")
		}
	}
	if !reflect.DeepEqual(o["const"], n["const"]) && n["const"] != nil {
		report("its value is now fixed to %s", show(n["const"]))
	}
	for _, f := range []string{"minimum", "exclusiveMinimum", "minLength", "minItems", "minProperties"} {
		if n[f] == nil {
			continue
		}
		if r, ok := numCmp(o[f], n[f]); o[f] == nil || !ok || r < 0 {
			report("%s rises to %s", f, show(n[f]))
		}
	}
	for _, f := range []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems", "maxProperties"} {
		if n[f] == nil {
			continue
		}
		if r, ok := numCmp(o[f], n[f]); o[f] == nil || !ok || r > 0 {
			report("%s falls to %s", f, show(n[f]))
		}
	}
	if op, np := s(o["pattern"]), s(n["pattern"]); np != "" && op != np {
		report("pattern %s", strconv.Quote(np))
	}
	if !b(o["uniqueItems"]) && b(n["uniqueItems"]) {
		report("items must now be unique")
	}
	// Properties: a closed object rejects the ones it no longer lists.
	op, np := m(o["properties"]), m(n["properties"])
	closed := func(x obj) bool { a, ok := x["additionalProperties"].(bool); return ok && !a }
	if closed(n) && !closed(o) {
		report("properties it does not list are no longer allowed")
	}
	for _, k := range names(op, np) {
		sub := p + "/properties/" + pointerEscape.Replace(k)
		switch ov, nv := op[k], np[k]; {
		case nv == nil && closed(n):
			*why = append(*why, sub+": the property is no longer allowed")
		case ov == nil && nv != nil && op != nil:
			// A new property constrains values that set it, where the old
			// schema allowed anything under that name.
			if !closed(o) {
				if x, ok := nv.(obj); ok && len(x) > 0 {
					tightened(obj{}, x, sub, why)
				}
			}
		case ov != nil && nv != nil:
			oo, _ := ov.(obj)
			no, _ := nv.(obj)
			if oo != nil && no != nil {
				tightened(oo, no, sub, why)
			} else if !reflect.DeepEqual(ov, nv) {
				*why = append(*why, sub+": changed in a way diff cannot compare")
			}
		}
	}
	if oi, ni := m(o["items"]), m(n["items"]); ni != nil {
		if oi == nil {
			oi = obj{}
		}
		tightened(oi, ni, p+"/items", why)
	}
	if oa, na := m(o["additionalProperties"]), m(n["additionalProperties"]); na != nil {
		if oa == nil {
			oa = obj{}
		}
		tightened(oa, na, p+"/additionalProperties", why)
	}
	// Keywords the comparison does not model: any change may accept less.
	known := map[string]bool{"type": true, "required": true, "enum": true, "const": true, "minimum": true, "exclusiveMinimum": true,
		"minLength": true, "minItems": true, "minProperties": true, "maximum": true, "exclusiveMaximum": true, "maxLength": true,
		"maxItems": true, "maxProperties": true, "pattern": true, "uniqueItems": true, "properties": true, "items": true,
		"additionalProperties": true, "description": true, "title": true, "default": true, "examples": true, "$schema": true,
		"$comment": true, "deprecated": true, "readOnly": true, "writeOnly": true, "format": true}
	for _, k := range names(o, n) {
		if !known[k] && !reflect.DeepEqual(o[k], n[k]) {
			report("%s changes, which diff cannot compare", k)
		}
	}
}

func types(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		return strList(x)
	}
	return nil
}

func jsonList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		j, _ := json.Marshal(x)
		out = append(out, string(j))
	}
	return out
}
