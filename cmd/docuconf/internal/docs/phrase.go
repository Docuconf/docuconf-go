package docs

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Every sentence the model carries is phrased here, so that every
// renderer, and every third party reading docs.json, words a fact the same
// way. Phrases are CommonMark inline text: literal values are code spans.

// fields is one contract input, decoded with json.Number for numbers.
type fields map[string]any

func (f fields) str(k string) string {
	s, _ := f[k].(string)
	return s
}

func (f fields) has(k string) bool {
	_, ok := f[k]
	return ok
}

func (f fields) strs(k string) []string {
	xs, _ := f[k].([]any)
	var out []string
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ----- constraints -----

// A constraint rule turns contract fields into one Constraint. The table
// below is the whole phrasing of SPEC §14.3: a new bound is one row.
type rule struct {
	name    string
	fields  []string                    // the contract fields it reads
	applies func(kind, typ string) bool // which inputs carry it
	phrase  func(f fields) string       // called when any of fields is set
}

var (
	numeric = only(KindVar, "int", "float", "duration")
	strText = func(kind, typ string) bool {
		return kind == KindVar && typ == "string" || kind == KindFile && typ == "text"
	}
	// minLength applies to strings and text files; maxLength also bounds a
	// url and a json value's wire string.
	lengthed = func(kind, typ string) bool {
		return strText(kind, typ) || kind == KindVar && (typ == "url" || typ == "json")
	}
	urls     = only(KindVar, "url")
	enums    = only(KindVar, "enum")
	lists    = only(KindVar, "list")
	schemaed = func(kind, typ string) bool {
		return kind == KindVar && typ == "json" || kind == KindFile && typ == "config"
	}
	anyFile  = func(kind, _ string) bool { return kind == KindFile }
	tlsFiles = only(KindFile, "tls")
)

var rules = []rule{
	bounds("range", "min", "max", numeric, "", nil),
	bounds("length", "minLength", "maxLength", lengthed, "", characters),
	{"pattern", []string{"pattern"}, strText, phrasePattern},
	{"schemes", []string{"schemes"}, urls, func(f fields) string { return oneOf(f.strs("schemes")) + " URL" }},
	{"values", []string{"values"}, enums, func(f fields) string { return "one of " + oneOf(f.strs("values")) }},
	bounds("itemCount", "minItems", "maxItems", lists, "", items),
	bounds("itemRange", "itemMin", "itemMax", lists, "each item ", nil),
	bounds("itemLength", "itemMinLength", "itemMaxLength", lists, "each item ", characters),
	{"schema", []string{"schema"}, schemaed, func(fields) string { return "matches the JSON Schema in the contract" }},
	{"maxSize", []string{"maxSize"}, anyFile, func(f fields) string { return "at most " + byteSize(f["maxSize"]) }},
	{"dnsNames", []string{"dnsNames"}, tlsFiles, func(f fields) string { return "the certificate covers " + allOf(f.strs("dnsNames")) }},
	{"keyAlgorithms", []string{"keyAlgorithms"}, tlsFiles, func(f fields) string {
		if len(f.strs("keyAlgorithms")) == 0 {
			return ""
		}
		return "key algorithm " + oneOf(f.strs("keyAlgorithms"))
	}},
	{"minRemaining", []string{"minRemaining"}, tlsFiles, func(f fields) string {
		return "at least " + durationPhrase(f.str("minRemaining")) + " of validity left"
	}},
	{"requireCA", []string{"requireCA"}, tlsFiles, func(f fields) string {
		if f["requireCA"] != true {
			return ""
		}
		return "includes `ca.crt`, and the certificate chains to it"
	}},
	{"minCertificates", []string{"minCertificates"}, only(KindFile, "caBundle"), func(f fields) string {
		return "at least " + count(f["minCertificates"], "CA certificate", "CA certificates")
	}},
	{"passwordVar", []string{"passwordVar"}, only(KindFile, "keystore"), func(f fields) string {
		return "opens with the password in " + code(f.str("passwordVar"))
	}},
}

func only(kind string, types ...string) func(string, string) bool {
	return func(k, t string) bool {
		if k != kind {
			return false
		}
		for _, x := range types {
			if x == t {
				return true
			}
		}
		return false
	}
}

// unit pluralises a bound's unit after the number it follows.
type unit func(n any) string

func characters(n any) string {
	return plural(n, "character", "characters") + " (Unicode code points)"
}

func items(n any) string { return plural(n, "item", "items") }

// bounds phrases a pair of inclusive bounds:
//
//	between 1 and 65535 · at least 1 · at most 64 · exactly 3 items
func bounds(name, lo, hi string, applies func(string, string) bool, prefix string, u unit) rule {
	return rule{name, []string{lo, hi}, applies, func(f fields) string {
		l, hasL := f[lo]
		h, hasH := f[hi]
		suffix := func(n any) string {
			if u == nil {
				return ""
			}
			return " " + u(n)
		}
		switch {
		case hasL && hasH && scalar(l) == scalar(h):
			return prefix + "exactly " + scalar(l) + suffix(l)
		case hasL && hasH:
			return prefix + "between " + scalar(l) + " and " + scalar(h) + suffix(h)
		case hasL:
			return prefix + "at least " + scalar(l) + suffix(l)
		default:
			return prefix + "at most " + scalar(h) + suffix(h)
		}
	}}
}

func phrasePattern(f fields) string {
	p := f.str("pattern")
	if strings.HasPrefix(p, "^") && strings.HasSuffix(p, "$") && !strings.HasSuffix(p, `\$`) {
		return "matches the RE2 pattern " + code(p)
	}
	// SPEC §4.3: a pattern matches anywhere unless anchored.
	return "contains a match for the RE2 pattern " + code(p)
}

// constraints returns an input's constraints in table order.
func constraints(kind, typ string, f fields) []Constraint {
	out := []Constraint{}
	for _, r := range rules {
		if !r.applies(kind, typ) {
			continue
		}
		params := map[string]json.RawMessage{}
		for _, k := range r.fields {
			if v, ok := f[k]; ok {
				params[k] = raw(v)
			}
		}
		if len(params) == 0 {
			continue
		}
		text := r.phrase(f)
		if text == "" {
			continue
		}
		out = append(out, Constraint{Rule: r.name, Params: params, Text: text})
	}
	return out
}

// ----- types and wire formats -----

func varTypeLabel(typ string, f fields) string {
	switch typ {
	case "int":
		return "integer"
	case "float":
		return "number"
	case "bool":
		return "boolean"
	case "url":
		return "URL"
	case "enum":
		return "enum"
	case "list":
		if f.str("items") == "int" {
			return "list of integers"
		}
		return "list of strings"
	case "json":
		return "JSON value"
	}
	return typ // string, duration
}

func fileTypeLabel(typ string, f fields) string {
	switch typ {
	case "config":
		return strings.ToUpper(f.str("format")) + " config file"
	case "tls":
		return "TLS key pair"
	case "caBundle":
		return "CA bundle"
	case "keystore":
		if f.str("format") == "jks" {
			return "JKS keystore"
		}
		return "PKCS#12 keystore"
	case "text":
		return "text file"
	}
	return "binary file"
}

// wire phrases how a variable is written (SPEC §5).
func wire(name, typ string, f fields) *Wire {
	w := wireFormat(name, typ, f)
	if f["secret"] == true {
		w.Platform = "a `secretKeyRef` or `injected` reference, never the value"
	}
	return w
}

func wireFormat(name, typ string, f fields) *Wire {
	w := &Wire{}
	switch typ {
	case "string":
		w.Text, w.Platform = "the text as is; it is never trimmed", "a string"
	case "int":
		w.Text, w.Platform = "a base-10 integer with no leading `+` or zeros, such as `8080`", "an integer"
	case "float":
		w.Text, w.Platform = "a decimal number with `.` as the decimal point, such as `0.5`", "a number"
	case "bool":
		w.Text, w.Platform = "`true` or `false`", "a boolean"
	case "url":
		w.Text, w.Platform = "an absolute URL, `scheme://...`, as is", "a string"
	case "enum":
		w.Text, w.Platform = "one of the values, exactly as listed", "a string"
	case "json":
		w.Text, w.Platform = "compact JSON on one line", "a JSON value (written as YAML or JSON in the values file)"
	case "duration":
		w.Encoding = f.str("encoding")
		w.Platform = "a duration in Go syntax, such as `90s` or `1m30s`; docuconf writes it in the wire format"
		switch w.Encoding {
		case "iso8601":
			w.Text = "an ISO 8601 duration, such as `PT90S` for 90 seconds"
		case "seconds":
			w.Text = "a number of seconds, such as `90` or `1.5`"
		case "timespan":
			w.Text = "a .NET TimeSpan, `[d.]hh:mm:ss[.fff]`, such as `00:01:30` for 90 seconds"
		default:
			w.Text = "a Go duration, such as `1m30s` (units `h`, `m`, `s`, `ms`, `us`, `ns`)"
		}
	case "list":
		w.Encoding = f.str("encoding")
		item, a, b := "strings", "a", "b"
		if f.str("items") == "int" {
			item, a, b = "integers", "1", "2"
		}
		w.Platform = "a list of " + item + ", such as `[" + a + ", " + b + "]`; docuconf writes it in the wire format"
		switch w.Encoding {
		case "json":
			ex := `["a","b"]`
			if item == "integers" {
				ex = "[1,2]"
			}
			w.Text = "a compact JSON array, such as " + code(ex)
		case "indexed":
			w.Text = "one variable per item, " + code(name+"__0") + ", " + code(name+"__1") + " and so on, numbered from 0 with no gaps"
		default:
			w.Separator = f.str("separator")
			w.Text = "the items joined by " + code(w.Separator) + " with nothing around it, such as " + code(a+w.Separator+b)
		}
	}
	return w
}

// fileContents phrases what a file input holds.
func fileContents(typ string, f fields) string {
	switch typ {
	case "config":
		return "a " + strings.ToUpper(f.str("format")) + " file, read at the path"
	case "tls":
		s := "a directory holding `tls.crt` and `tls.key` (PEM), as a `kubernetes.io/tls` Secret lays them out"
		if f["requireCA"] == true {
			s += ", and `ca.crt`"
		}
		return s
	case "caBundle":
		return "a PEM file of one or more CA certificates"
	case "keystore":
		if f.str("format") == "jks" {
			return "a JKS keystore file"
		}
		return "a PKCS#12 keystore file"
	case "text":
		return "a UTF-8 text file"
	}
	return "opaque bytes"
}

func reloadText(reload string) string {
	if reload == "watch" {
		return "the app reloads the file when it changes"
	}
	return "the app reads the file at startup; a changed source needs a rollout"
}

// ----- sources -----

var sourceText = map[string]string{
	"literal":          "a literal value in the platform's values file",
	"configMapKeyRef":  "a key of a ConfigMap (`configMapKeyRef`), checked at boot",
	"fieldRef":         "a pod field from the Downward API (`fieldRef`), such as `metadata.namespace`",
	"resourceFieldRef": "a container resource limit or request (`resourceFieldRef`), such as `limits.memory`",
	"secretKeyRef":     "a key of a Kubernetes Secret (`secretKeyRef`)",
	"injected":         "supplied when the container starts by an injector (`injected`), such as Bank-Vaults, `op run` or the Vault Agent injector; checked at boot",
	"inline":           "content in the platform repository (`inline`), rendered as a content-hashed ConfigMap",
	"configMap":        "a ConfigMap (`configMap`), one key per file",
	"secret":           "a Kubernetes Secret (`secret`), one key per file",
	"certificate":      "a cert-manager Certificate (`certificate`)",
	"csi":              "the Secrets Store CSI driver (`csi`)",
	"image":            "an OCI image volume (`image`), for content too large for a ConfigMap",
	"overlay":          "a config-file overlay (`overlay`): the platform writes the value into a file the app loads, at the variable's config key, instead of the environment",
}

func src(kind string) Source { return Source{Kind: kind, Text: sourceText[kind]} }

// varSources lists where the platform may get a variable's value
// (SPEC §4.5, §4.7, §6).
func varSources(typ string, f fields, overlays []Overlay, selector bool) []Source {
	if f["secret"] == true {
		return []Source{src("secretKeyRef"), src("injected")}
	}
	out := []Source{src("literal")}
	if typ != "list" {
		out = append(out, src("configMapKeyRef"))
	}
	if typ == "string" {
		out = append(out, src("fieldRef"))
	}
	if typ == "int" {
		out = append(out, src("resourceFieldRef"))
	}
	inj := src("injected")
	if typ == "list" && f.str("encoding") == "indexed" {
		inj.Note = "without a `ref`: one reference cannot carry an indexed list"
	}
	out = append(out, inj)
	if key := f.str("configKey"); key != "" && !selector {
		for _, o := range overlays {
			s := src("overlay")
			s.Overlay = o.Name
			s.Note = "the " + code(o.Name) + " overlay, at key " + code(key)
			out = append(out, s)
		}
	}
	return out
}

// fileSources lists where the platform may get a file input's content
// (SPEC §4.6.1, §6).
func fileSources(typ string, secret bool) []Source {
	var out []Source
	if !secret && typ != "binary" {
		out = append(out, src("inline"))
	}
	if !secret {
		out = append(out, src("configMap"))
	}
	s := src("secret")
	if typ == "tls" {
		s.Note = "the whole `kubernetes.io/tls` Secret, with no key"
	}
	out = append(out, s)
	if typ == "tls" {
		out = append(out, src("certificate"))
	}
	out = append(out, src("csi"))
	if !secret {
		out = append(out, src("image"))
	}
	inj := src("injected")
	inj.Note = "the injector writes the file at the path"
	out = append(out, inj)
	return out
}

// ----- boot errors -----

// errorCatalog explains every boot error code, in the order of SPEC §11.2
// item 5.
var errorCatalog = []ErrorInfo{
	{"missing_required", "A required input is not set, and has no default.", "Set it through one of its allowed sources."},
	{"invalid_type", "The value does not parse as the input's type in its wire format, or a secret still holds an unresolved injector reference (`vault:`, `op://`, `ref+`).", "Write the value in the input's wire format. For an injected secret, make sure the injector runs."},
	{"out_of_range", "A number, duration, length or list item is outside the input's bounds.", "Use a value within the input's constraints."},
	{"pattern_mismatch", "The value does not match the input's pattern.", "Use a value that matches the pattern."},
	{"not_in_enum", "The value is not one of the allowed values.", "Use one of the listed values, spelled exactly as listed."},
	{"invalid_scheme", "The URL's scheme is not one of the allowed schemes.", "Use a URL with an allowed scheme."},
	{"too_few_items", "The list has fewer items than its minimum.", "Add items."},
	{"too_many_items", "The list has more items than its maximum.", "Remove items."},
	{"file_missing", "The file is not at its path.", "Give the input a source, and check that it is mounted at the declared path (or that its path variable points at it)."},
	{"file_unreadable", "The file exists but cannot be read.", "Check the mount, the file mode and the user the app runs as."},
	{"file_too_large", "The file is larger than its maximum size.", "Shrink the content, or raise `maxSize` in the app's declaration."},
	{"file_malformed", "The file does not parse in its format, is a directory, is not UTF-8 text, or holds too few certificates.", "Fix the content so it parses in the declared format."},
	{"schema_mismatch", "The value or file parses, but does not match its JSON Schema.", "Fix the content to match the schema; `docuconf vet` checks inline content before deploy."},
	{"certificate_invalid", "A certificate does not parse, has expired or is not yet valid, uses a key algorithm that is not allowed, or does not chain to `ca.crt`.", "Issue a valid certificate that meets the input's constraints."},
	{"certificate_expiring", "The certificate has less validity left than `minRemaining`.", "Renew it earlier: with cert-manager, set `renewBefore` to at least `minRemaining`."},
	{"certificate_name_mismatch", "The certificate does not cover every name in `dnsNames`.", "Reissue it with every name as a subject alternative name."},
	{"key_mismatch", "`tls.key` does not match the certificate in `tls.crt`.", "Supply the key and certificate from the same issuance."},
	{"keystore_unreadable", "The keystore does not open with its password, or is corrupt.", "Check the keystore file and the secret variable that holds its password."},
}

// varErrors returns the codes the SDK may report for a variable.
func varErrors(typ string, f fields) []string {
	var c codes
	c.add("missing_required", f["required"] == true)
	c.add("invalid_type", typ != "string" && typ != "enum" || f["secret"] == true)
	switch typ {
	case "string":
		c.add("out_of_range", f.has("minLength") || f.has("maxLength"))
		c.add("pattern_mismatch", f.has("pattern"))
	case "int":
		c.add("out_of_range", true) // at least the 64-bit range
	case "float", "duration":
		c.add("out_of_range", f.has("min") || f.has("max"))
	case "url":
		c.add("out_of_range", f.has("maxLength"))
		c.add("invalid_scheme", f.has("schemes"))
	case "enum":
		c.add("not_in_enum", true)
	case "list":
		c.add("out_of_range", f.str("items") == "int" || f.has("itemMinLength") || f.has("itemMaxLength"))
		c.add("too_few_items", f.has("minItems"))
		c.add("too_many_items", f.has("maxItems"))
	case "json":
		c.add("out_of_range", f.has("maxLength"))
		c.add("schema_mismatch", f.has("schema"))
	}
	return c.sorted()
}

// fileErrors returns the codes the SDK may report for a file input.
func fileErrors(typ string, f fields) []string {
	var c codes
	c.add("file_missing", f["required"] == true || f.has("pathEnv"))
	c.add("file_unreadable", true)
	c.add("file_too_large", f.has("maxSize"))
	c.add("file_malformed", true)
	switch typ {
	case "config":
		c.add("schema_mismatch", f.has("schema"))
	case "tls":
		c.add("certificate_invalid", true)
		c.add("certificate_expiring", f.has("minRemaining"))
		c.add("certificate_name_mismatch", f.has("dnsNames"))
		c.add("key_mismatch", true)
	case "caBundle":
		c.add("certificate_invalid", true)
	case "keystore":
		c.add("keystore_unreadable", true)
		c.add("certificate_invalid", true)
	case "text":
		c.add("pattern_mismatch", f.has("pattern"))
		c.add("out_of_range", f.has("minLength") || f.has("maxLength"))
	}
	return c.sorted()
}

type codes map[string]bool

func (c *codes) add(code string, ok bool) {
	if *c == nil {
		*c = codes{}
	}
	if ok {
		(*c)[code] = true
	}
}

func (c codes) sorted() []string {
	out := []string{}
	for _, e := range errorCatalog {
		if c[e.Code] {
			out = append(out, e.Code)
		}
	}
	return out
}

// ----- small phrasing helpers -----

// code writes s as a CommonMark code span, with a fence longer than any
// run of backticks in s.
func code(s string) string {
	if s == "" {
		return "`\"\"`"
	}
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || (strings.HasPrefix(s, " ") && strings.HasSuffix(s, " ") && strings.TrimSpace(s) != "") {
		s = " " + s + " "
	}
	return fence + s + fence
}

// oneOf joins values as code spans: `a`, `b` or `c`.
func oneOf(xs []string) string { return join(xs, "or") }

// allOf joins values as code spans: `a`, `b` and `c`.
func allOf(xs []string) string { return join(xs, "and") }

func join(xs []string, last string) string {
	cs := make([]string, len(xs))
	for i, x := range xs {
		cs[i] = code(x)
	}
	switch len(cs) {
	case 0:
		return ""
	case 1:
		return cs[0]
	}
	return strings.Join(cs[:len(cs)-1], ", ") + " " + last + " " + cs[len(cs)-1]
}

// scalar writes a number or string bound as it appears in the contract.
func scalar(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return x
	}
	return fmt.Sprint(v)
}

func plural(n any, one, many string) string {
	if scalar(n) == "1" {
		return one
	}
	return many
}

func count(n any, one, many string) string { return scalar(n) + " " + plural(n, one, many) }

// byteSize phrases a size: "64 KiB (65536 bytes)", or "1000 bytes".
func byteSize(v any) string {
	n, ok := new(big.Int).SetString(scalar(v), 10)
	if !ok {
		return scalar(v) + " bytes"
	}
	bytes := count(v, "byte", "bytes")
	for _, u := range []struct {
		name string
		size int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		q, r := new(big.Int).QuoRem(n, big.NewInt(u.size), new(big.Int))
		if r.Sign() == 0 && q.Sign() > 0 {
			return q.String() + " " + u.name + " (" + bytes + ")"
		}
	}
	return bytes
}

// durationPhrase writes a contract duration, adding days when whole:
// "720h (30 days)".
func durationPhrase(s string) string {
	d, err := time.ParseDuration(s)
	if err != nil || d < 24*time.Hour || d%(24*time.Hour) != 0 {
		return s
	}
	days := int64(d / (24 * time.Hour))
	return fmt.Sprintf("%s (%s)", s, count(json.Number(fmt.Sprint(days)), "day", "days"))
}
