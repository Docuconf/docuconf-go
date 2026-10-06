package platform

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/errors"
)

// translator turns CUE errors from #Validate into one readable line per
// problem (SPEC §7). Raw CUE messages for a failed disjunction are noisy
// ("8 errors in empty disjunction"), so problems with a variable are
// re-explained from the variable's type and constraints, and file checks
// from the name of the check that failed. CUE's own message is the
// fallback, and is withheld for secrets.
type translator struct {
	values, files cue.Value
	vars          map[string]cue.Value
	fileDefs      map[string]cue.Value
	secrets       []string // literal values to scrub from every line
	explained     map[string]bool
	out           []string
}

func newTranslator(c *Contract, values, files cue.Value) *translator {
	t := &translator{
		values:    values,
		files:     files,
		vars:      fields(c.Value.LookupPath(cue.ParsePath("vars"))),
		fileDefs:  fields(c.Value.LookupPath(cue.ParsePath("files"))),
		explained: map[string]bool{},
	}
	for name, v := range t.vars {
		x := t.value(name)
		if boolean(v, "secret") && !x.LookupPath(cue.ParsePath("secretKeyRef")).Exists() {
			collectStrings(x, &t.secrets) // a literal wrongly supplied for a secret
		}
	}
	for name, f := range t.fileDefs {
		if boolean(f, "secret") {
			collectStrings(t.source(name).LookupPath(cue.ParsePath("inline")), &t.secrets)
		}
	}
	return t
}

// declaredOnly reports names in v that are not in declared, and returns v
// without them.
func (t *translator) declaredOnly(ctx *cue.Context, v cue.Value, declared map[string]cue.Value, msg string) cue.Value {
	out := ctx.CompileString("{}")
	it, _ := v.Fields()
	for it != nil && it.Next() {
		name := selName(it.Selector())
		if _, ok := declared[name]; !ok {
			t.add("%s: %s", name, msg)
			continue
		}
		out = out.FillPath(cue.MakePath(cue.Str(name)), it.Value())
	}
	return out
}

func fields(v cue.Value) map[string]cue.Value {
	out := map[string]cue.Value{}
	it, _ := v.Fields()
	for it != nil && it.Next() {
		out[selName(it.Selector())] = it.Value()
	}
	return out
}

func collectStrings(v cue.Value, dst *[]string) {
	if !v.Exists() {
		return
	}
	switch v.Kind() {
	case cue.StringKind:
		if s, _ := v.String(); len(s) >= 3 {
			*dst = append(*dst, s)
		}
	case cue.StructKind:
		it, _ := v.Fields()
		for it != nil && it.Next() {
			collectStrings(it.Value(), dst)
		}
	case cue.ListKind:
		it, _ := v.List()
		for it.Next() {
			collectStrings(it.Value(), dst)
		}
	case cue.IntKind, cue.FloatKind, cue.NumberKind:
		*dst = append(*dst, fmt.Sprint(v))
	}
}

func (t *translator) value(name string) cue.Value {
	return t.values.LookupPath(cue.MakePath(cue.Str(name)))
}

func (t *translator) source(name string) cue.Value {
	return t.files.LookupPath(cue.MakePath(cue.Str(name)))
}

func (t *translator) add(format string, args ...any) {
	l := fmt.Sprintf(format, args...)
	if !slices.Contains(t.out, l) {
		t.out = append(t.out, l)
	}
}

// lines returns the problems, sorted, with any secret value scrubbed.
func (t *translator) lines() []string {
	out := dropDisjunctionNoise(t.out)
	for i, l := range out {
		for _, s := range t.secrets {
			l = strings.ReplaceAll(l, s, "<redacted>")
		}
		out[i] = l
	}
	slices.Sort(out)
	return out
}

// dropDisjunctionNoise removes the lines CUE adds when inline content fails
// its schema: inline content is a string, struct or list, so CUE also
// reports the branches the content never meant to take. They are dropped
// when the same input has a line about the content itself.
func dropDisjunctionNoise(lines []string) []string {
	const marker = ": inline content does not match its schema: "
	noise := func(l string) bool {
		return strings.Contains(l, "errors in empty disjunction") || strings.Contains(l, "(mismatched types ")
	}
	real := map[string]bool{}
	for _, l := range lines {
		if name, _, ok := strings.Cut(l, marker); ok && !noise(l) {
			real[name] = true
		}
	}
	var out []string
	for _, l := range lines {
		if name, _, ok := strings.Cut(l, marker); ok && noise(l) && real[name] {
			continue
		}
		out = append(out, l)
	}
	return out
}

func errPath(e errors.Error) []string {
	p := e.Path()
	if len(p) > 0 && strings.HasPrefix(p[0], "#") {
		p = p[1:] // #Validate
	}
	out := make([]string, len(p))
	for i, s := range p {
		out[i] = unquote(s)
	}
	return out
}

func errMsg(e errors.Error) string {
	format, args := e.Msg()
	return fmt.Sprintf(format, args...)
}

func (t *translator) policy(err error) {
	for _, e := range errors.Errors(err) {
		p := errPath(e)
		if len(p) == 0 {
			t.add("policy: %s", errMsg(e))
			continue
		}
		name := p[0]
		if v, ok := t.vars[name]; ok && boolean(v, "secret") {
			t.add("%s: value is not allowed by policy", name)
			continue
		}
		t.add("%s: %s is not allowed by policy", name, describe(t.value(name)))
	}
}

func (t *translator) validate(err error) {
	for _, e := range errors.Errors(err) {
		p := errPath(e)
		if len(p) < 2 {
			t.add("%s", e.Error())
			continue
		}
		section, name := p[0], p[1]
		switch section {
		case "checks":
			t.varProblem(name, e)
		case "values":
			if _, ok := t.vars[name]; !ok {
				t.add("%s: is not declared in the contract (check the spelling)", name)
				continue
			}
			t.varProblem(name, e)
		case "fileChecks":
			t.fileProblem(name, p[2:], e)
		case "files":
			if _, ok := t.fileDefs[name]; !ok {
				t.add("%s: is not a file input declared in the contract", name)
				continue
			}
			t.add("%s: source is not valid: %s", name, errMsg(e))
		case "missingRequired":
			if _, ok := t.fileDefs[name]; ok {
				t.add("%s: is a required file input, and has no source", name)
			} else {
				t.add("%s: is required, and set neither by the platform nor by the selected profile", name)
			}
		case "contract":
			t.add("contract: %s", e.Error())
		default:
			t.add("%s", e.Error())
		}
	}
}

func (t *translator) varProblem(name string, e errors.Error) {
	if t.explained[name] {
		return
	}
	cv, ok := t.vars[name]
	if !ok {
		t.add("%s: %s", name, errMsg(e))
		return
	}
	if lines := explainVar(name, cv, t.value(name)); len(lines) > 0 {
		t.explained[name] = true
		for _, l := range lines {
			t.add("%s: %s", name, l)
		}
		return
	}
	if boolean(cv, "secret") {
		t.add("%s: value does not satisfy the contract (withheld: secret)", name)
		return
	}
	if str(cv, "type") == "json" {
		// Name the property inside the value: checks.NAME.literal.a.b
		p := errPath(e)
		if i := slices.Index(p, "literal"); i >= 0 && i+1 < len(p) {
			t.add("%s: does not match its schema: at %s: %s", name, strings.Join(p[i+1:], "."), errMsg(e))
			return
		}
		t.add("%s: does not match its schema: %s", name, errMsg(e))
		return
	}
	t.add("%s: %s", name, errMsg(e))
}

var durationRe = regexp.MustCompile(`^([0-9]+(ns|us|ms|s|m|h))+$`)
var urlRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+$`)

var refKinds = []string{"configMapKeyRef", "fieldRef", "resourceFieldRef", "secretKeyRef", "injected"}

var providerRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$`)

// explainInjected explains a malformed injected value (SPEC §4.5.1). The
// reference is not secret material, but it is never needed in the message.
func explainInjected(cv, x cue.Value) []string {
	var out []string
	if p, err := x.LookupPath(cue.ParsePath("injected.provider")).String(); err != nil || !providerRe.MatchString(p) {
		out = append(out, "injected.provider must name the injector as a lowercase label, such as bank-vaults")
	}
	ref := x.LookupPath(cue.ParsePath("injected.ref"))
	if ref.Exists() {
		if r, err := ref.String(); err != nil || r == "" {
			out = append(out, "injected.ref, when given, must be a non-empty string")
		} else if str(cv, "type") == "list" && str(cv, "encoding") == "indexed" {
			out = append(out, "an indexed list is spread over NAME__0, NAME__1, …, so one injected reference cannot carry it; let the injector set the variables, without ref")
		}
	}
	return out
}

// explainVar re-checks a value against its variable and explains what is
// wrong, mirroring #Check. It returns nothing if it finds no problem, so
// the caller falls back to CUE's message.
func explainVar(name string, cv, x cue.Value) []string {
	typ := str(cv, "type")
	secret := boolean(cv, "secret")
	ref := ""
	if x.Kind() == cue.StructKind {
		for _, k := range refKinds {
			if x.LookupPath(cue.MakePath(cue.Str(k))).Exists() {
				ref = k
			}
		}
	}
	if ref == "injected" {
		return explainInjected(cv, x)
	}
	if secret {
		if ref != "secretKeyRef" {
			return []string{"is secret, so it must come from a secretKeyRef or an injector, never a literal or another reference"}
		}
		return nil
	}
	switch ref {
	case "secretKeyRef":
		return []string{"is not secret; supply a literal or a configMapKeyRef, not a secretKeyRef"}
	case "fieldRef":
		if typ != "string" {
			return []string{fmt.Sprintf("a fieldRef always yields a string, but %s is %s", name, article(typ))}
		}
		return []string{fmt.Sprintf("fieldRef.fieldPath %s is not a Downward API field", describe(x.LookupPath(cue.ParsePath("fieldRef.fieldPath"))))}
	case "resourceFieldRef":
		if typ != "int" {
			return []string{fmt.Sprintf("a resourceFieldRef always yields an integer, but %s is %s", name, article(typ))}
		}
		return []string{fmt.Sprintf("resourceFieldRef.resource %s is not a container resource", describe(x.LookupPath(cue.ParsePath("resourceFieldRef.resource"))))}
	case "configMapKeyRef":
		if typ == "list" {
			return []string{"a list cannot come from a configMapKeyRef; supply it as a list"}
		}
		return nil
	}

	switch typ {
	case "string":
		s, err := x.String()
		if err != nil {
			return []string{"expected a string, got " + describe(x) + yamlHint(x)}
		}
		return explainLength(cv, s)
	case "int":
		if x.Kind() != cue.IntKind {
			return []string{"expected an integer, got " + describe(x)}
		}
		n, _ := x.Int(nil)
		if m, ok := bigInt(cv, "min"); ok && n.Cmp(m) < 0 {
			return []string{fmt.Sprintf("%s is below min %s", n, m)}
		}
		if m, ok := bigInt(cv, "max"); ok && n.Cmp(m) > 0 {
			return []string{fmt.Sprintf("%s is above max %s", n, m)}
		}
	case "float":
		if x.Kind() != cue.IntKind && x.Kind() != cue.FloatKind {
			return []string{"expected a number, got " + describe(x)}
		}
		f, _ := x.Float64()
		if m, err := cv.LookupPath(cue.ParsePath("min")).Float64(); err == nil && f < m {
			return []string{fmt.Sprintf("%s is below min %s", describe(x), fmt.Sprint(cv.LookupPath(cue.ParsePath("min"))))}
		}
		if m, err := cv.LookupPath(cue.ParsePath("max")).Float64(); err == nil && f > m {
			return []string{fmt.Sprintf("%s is above max %s", describe(x), fmt.Sprint(cv.LookupPath(cue.ParsePath("max"))))}
		}
	case "bool":
		if x.Kind() != cue.BoolKind {
			return []string{"expected true or false, got " + describe(x) + yamlHint(x)}
		}
	case "duration":
		s, err := x.String()
		if err != nil || !durationRe.MatchString(s) {
			return []string{describe(x) + " is not a duration such as 1m30s"}
		}
		d, _ := time.ParseDuration(s)
		if enc := str(cv, "encoding"); enc != "go" && d%time.Millisecond != 0 {
			return []string{fmt.Sprintf("%s is finer than a millisecond, which the %s encoding cannot carry", s, enc)}
		}
		if m := str(cv, "min"); m != "" {
			if md, _ := time.ParseDuration(m); d < md {
				return []string{fmt.Sprintf("%s is below min %s", s, m)}
			}
		}
		if m := str(cv, "max"); m != "" {
			if md, _ := time.ParseDuration(m); d > md {
				return []string{fmt.Sprintf("%s is above max %s", s, m)}
			}
		}
	case "url":
		s, err := x.String()
		if err != nil {
			return []string{"expected a URL string, got " + describe(x)}
		}
		if !urlRe.MatchString(s) {
			return []string{describe(x) + " is not a URL of the form scheme://..."}
		}
		if schemes := strs(cv, "schemes"); len(schemes) > 0 {
			scheme := s[:strings.Index(s, "://")]
			if !slices.Contains(schemes, scheme) {
				return []string{fmt.Sprintf("scheme %q is not one of %s", scheme, strings.Join(schemes, ", "))}
			}
		}
	case "enum":
		values := strs(cv, "values")
		s, err := x.String()
		if err != nil || !slices.Contains(values, s) {
			return []string{fmt.Sprintf("%s is not one of %s", describe(x), strings.Join(values, ", "))}
		}
	case "list":
		if x.Kind() != cue.ListKind {
			return []string{"expected a list, got " + describe(x)}
		}
		items := str(cv, "items")
		it, _ := x.List()
		n := 0
		for it.Next() {
			e := it.Value()
			if (items == "int" && e.Kind() != cue.IntKind) || (items == "string" && e.Kind() != cue.StringKind) {
				return []string{fmt.Sprintf("item %d: expected %s, got %s", n, article(items), describe(e))}
			}
			n++
		}
		if m, ok := bigInt(cv, "minItems"); ok && int64(n) < m.Int64() {
			return []string{fmt.Sprintf("has %s, below minItems %s", plural(n, "item"), m)}
		}
		if m, ok := bigInt(cv, "maxItems"); ok && int64(n) > m.Int64() {
			return []string{fmt.Sprintf("has %s, above maxItems %s", plural(n, "item"), m)}
		}
	}
	return nil
}

func explainLength(cv cue.Value, s string) []string {
	var out []string
	n := len([]rune(s))
	if m, ok := bigInt(cv, "minLength"); ok && int64(n) < m.Int64() {
		out = append(out, fmt.Sprintf("%s is %d characters, below minLength %s", strconv.Quote(s), n, m))
	}
	if m, ok := bigInt(cv, "maxLength"); ok && int64(n) > m.Int64() {
		out = append(out, fmt.Sprintf("%s is %d characters, above maxLength %s", strconv.Quote(s), n, m))
	}
	if p := str(cv, "pattern"); p != "" {
		if re, err := regexp.Compile(p); err == nil && !re.MatchString(s) {
			out = append(out, fmt.Sprintf("%s does not match pattern %s", strconv.Quote(s), p))
		}
	}
	return out
}

// yamlHint explains YAML 1.1's implicit typing, which turns NO into false
// and 1.10 into 1.1.
func yamlHint(x cue.Value) string {
	switch x.Kind() {
	case cue.BoolKind, cue.IntKind, cue.FloatKind:
		return " (quote the value in YAML to keep it a string)"
	}
	return ""
}

func (t *translator) fileProblem(name string, rest []string, e errors.Error) {
	f := t.fileDefs[name]
	src := t.source(name)
	check := ""
	if len(rest) > 0 {
		check = rest[0]
	}
	kind := sourceKind(src)
	switch check {
	case "secretFromSecretStore":
		t.add("%s: is secret, so it must come from a secret, certificate, csi or injected source, not %s", name, kind)
	case "certificateOnlyForTLS":
		t.add("%s: a certificate source only fits a tls input, and this is a %s input", name, str(f, "type"))
	case "binaryCannotBeInline":
		t.add("%s: binary content cannot be inline; use an image, configMap, secret or csi source", name)
	case "keyRequired":
		t.add("%s: the %s source needs a key naming the file to mount", name, kind)
	case "wholeSecretForTLS":
		t.add("%s: a tls input mounts the whole Secret; remove secret.key", name)
	case "tlsSecretType":
		t.add("%s: Secret %s has type %s, want kubernetes.io/tls", name, str(src, "secret.name"), str(src, "secret.type"))
	case "hasCertAndKey":
		t.add("%s: Secret %s lacks tls.crt or tls.key", name, str(src, "secret.name"))
	case "hasCA":
		t.add("%s: Secret %s lacks ca.crt, which requireCA needs", name, str(src, "secret.name"))
	case "coversDNSName":
		if len(rest) > 1 {
			t.add("%s: certificate does not cover %s", name, rest[1])
		} else {
			t.add("%s: certificate does not cover every name in dnsNames", name)
		}
	case "allowedKeyAlgorithm":
		t.add("%s: certificate key algorithm %s is not one of %s", name,
			str(src, "certificate.privateKey.algorithm"), strings.Join(strs(f, "keyAlgorithms"), ", "))
	case "renewsBeforeMinRemaining":
		t.add("%s: certificate renewBefore %s is less than minRemaining %s, so the app could see a certificate with too little time left",
			name, str(src, "certificate.renewBefore"), str(f, "minRemaining"))
	case "withinMaxSize":
		content := str(src, "inline")
		t.add("%s: inline content is %d bytes, above maxSize %s", name, len(content), fmt.Sprint(f.LookupPath(cue.ParsePath("maxSize"))))
	case "wellFormed":
		t.add("%s: inline content is not valid %s", name, str(f, "format"))
	case "matchesSchema":
		t.add("%s: inline content does not match its schema: %s", name, schemaMsg(rest[1:], e))
	case "text":
		lines := explainLength(f, str(src, "inline"))
		if len(lines) == 0 {
			t.add("%s: inline text does not satisfy the input's constraints", name)
		}
		for _, l := range lines {
			// Text content may be long; describe it rather than echo it.
			l = l[strings.Index(l, `" `)+2:]
			t.add("%s: inline text %s", name, l)
		}
	case "enoughCertificates":
		n := strings.Count(str(src, "inline"), "-----BEGIN CERTIFICATE-----")
		t.add("%s: inline bundle holds %s, need at least %s", name, plural(n, "certificate"), fmt.Sprint(f.LookupPath(cue.ParsePath("minCertificates"))))
	default:
		if boolean(f, "secret") {
			t.add("%s: source does not satisfy the contract", name)
		} else {
			t.add("%s: %s", name, errMsg(e))
		}
	}
}

func schemaMsg(path []string, e errors.Error) string {
	msg := errMsg(e)
	if len(path) > 0 {
		return strings.Join(path, ".") + ": " + msg
	}
	return msg
}

func sourceKind(src cue.Value) string {
	for _, k := range []string{"inline", "configMap", "secret", "certificate", "csi", "image", "injected"} {
		if src.LookupPath(cue.MakePath(cue.Str(k))).Exists() {
			return k
		}
	}
	return "an unknown source"
}

// describe shows a value in a message: strings quoted, others as CUE.
func describe(v cue.Value) string {
	if !v.Exists() {
		return "nothing"
	}
	switch v.Kind() {
	case cue.StringKind:
		s, _ := v.String()
		if len(s) > 64 {
			s = s[:61] + "..."
		}
		return strconv.Quote(s)
	case cue.StructKind:
		return "a struct " + truncate(jsonOf(v))
	case cue.ListKind:
		return "a list " + truncate(jsonOf(v))
	case cue.BottomKind:
		return "an invalid value"
	}
	return fmt.Sprint(v)
}

func truncate(s string) string {
	if len(s) > 64 {
		return s[:61] + "..."
	}
	return s
}

func article(typ string) string {
	switch typ {
	case "int":
		return "an integer"
	case "enum", "url":
		return "an " + typ
	}
	return "a " + typ
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func str(v cue.Value, p string) string {
	s, _ := v.LookupPath(cue.ParsePath(p)).String()
	return s
}

func boolean(v cue.Value, p string) bool {
	b, _ := v.LookupPath(cue.ParsePath(p)).Bool()
	return b
}

func strs(v cue.Value, p string) []string {
	var out []string
	it, err := v.LookupPath(cue.ParsePath(p)).List()
	if err != nil {
		return nil
	}
	for it.Next() {
		s, _ := it.Value().String()
		out = append(out, s)
	}
	return out
}

func bigInt(v cue.Value, p string) (*big.Int, bool) {
	n, err := v.LookupPath(cue.ParsePath(p)).Int(nil)
	return n, err == nil
}
