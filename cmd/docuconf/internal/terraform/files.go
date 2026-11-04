package terraform

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// File is one contract file input.
type File struct {
	Name   string
	Raw    map[string]any
	Type   string
	Secret bool
}

// Overlay is one contract config-file overlay.
type Overlay struct {
	Name string
	Raw  map[string]any
}

// Mode is a volume's defaultMode, as the kubernetes provider takes it.
func (f File) mode() string {
	if f.Secret {
		return "0400"
	}
	return "0444"
}

func (f File) path() string { return str(f.Raw, "path") }

// mountDir is where the file's volume is mounted: a tls input's path is a
// directory; any other input's path names the file.
func (f File) mountDir() string {
	if f.Type == "tls" {
		return f.path()
	}
	return path.Dir(f.path())
}

func (f File) fileName() string { return path.Base(f.path()) }

// ident is how a file's name appears in local names: identifiers may hold
// "-", but locals read better with "_".
func ident(name string) string { return strings.ReplaceAll(name, "-", "_") }

// fileSources lists a file argument's attributes (SPEC.md 4.6 and
// #CheckFile): a secret comes from a secret store, a certificate only fits
// a tls input, and binary content cannot be inline.
func fileSources(f File) []string {
	var s []string
	if !f.Secret && f.Type != "binary" {
		s = append(s, "inline")
	}
	if !f.Secret {
		s = append(s, "config_map")
	}
	s = append(s, "secret")
	if f.Type == "tls" {
		s = append(s, "certificate")
	}
	s = append(s, "csi")
	if !f.Secret {
		s = append(s, "image")
	}
	return append(s, "injected")
}

var allFileSources = []string{"inline", "config_map", "secret", "certificate", "csi", "image", "injected"}

func fileSourceType(f File, s string) string {
	switch s {
	case "inline":
		if f.Type == "config" {
			return "any" // text, or an object or list to encode in the file's format
		}
		return "string"
	case "config_map":
		return "object({ name = string, key = string })"
	case "secret":
		if f.Type == "tls" {
			return "object({ name = string, key = optional(string) })" // the whole kubernetes.io/tls Secret; key is rejected
		}
		return "object({ name = string, key = string })"
	case "certificate":
		return "object({ name = string, secret_name = string, dns_names = optional(list(string)), private_key = optional(object({ algorithm = optional(string) })), duration = optional(string), renew_before = optional(string) })"
	case "csi":
		return `object({ driver = optional(string, "secrets-store.csi.k8s.io"), secret_provider_class = string })`
	case "image":
		return "object({ reference = string, pull_policy = optional(string) })"
	}
	return "object({ provider = string, pod_annotations = optional(map(string), {}), pod_labels = optional(map(string), {}) })"
}

// fileHint explains an argument with nothing set: Terraform drops the
// attributes the object type leaves out, such as inline on a secret.
func fileHint(f File, srcs []string) string {
	var dropped []string
	for _, s := range allFileSources {
		if !has(srcs, s) {
			dropped = append(dropped, s)
		}
	}
	if len(dropped) == 0 {
		return ""
	}
	why := "a " + f.Type + " input cannot take them"
	if f.Secret {
		why = "a secret file comes from a secret store"
	}
	return fmt.Sprintf(" (Terraform ignores %s here: %s)", orList(dropped), why)
}

func describeFile(f File) string {
	var tags []string
	if boolean(f.Raw, "required") {
		tags = append(tags, "required")
	}
	if f.Secret {
		tags = append(tags, "secret")
	}
	tags = append(tags, f.Type+" file at "+f.path())
	return fmt.Sprintf("%s (%s)", str(f.Raw, "description"), strings.Join(tags, ", "))
}

// isString and isCollection tell an inline value's kind: HCL converts
// between strings, numbers and booleans freely, but jsonencode does not.
func isString(x string) string {
	return fmt.Sprintf(`substr(jsonencode(%s), 0, 1) == "\""`, x)
}

func isCollection(x string) string {
	return fmt.Sprintf(`contains(["{", "["], substr(jsonencode(%s), 0, 1))`, x)
}

// inlineContent is an HCL expression for the file an inline value becomes:
// text as it is, and an object or list encoded in the file's format, as
// #InlineContent does. TOML has no encoder in Terraform, so structured
// TOML is rejected by a validation and yields null here.
func inlineContent(f File, x string) string {
	if f.Type != "config" {
		return x
	}
	var enc string
	switch str(f.Raw, "format") {
	case "json":
		enc = compactJSON(x)
	case "yaml":
		enc = fmt.Sprintf("yamlencode(%s)", x)
	default:
		enc = "null"
	}
	return fmt.Sprintf("(%s ? tostring(%s) : %s)", isString(x), x, enc)
}

// byteLength counts a string's bytes: length() counts grapheme clusters,
// but base64 is ASCII, four characters per three bytes.
func byteLength(s string) string {
	b := fmt.Sprintf("base64encode(%s)", s)
	return fmt.Sprintf(`(length(%s) / 4 * 3 - length(regexall("=", %s)))`, b, b)
}

// fileChecks are a file argument's validations: #CheckFile, worded as vet
// words it.
func (g *gen) fileChecks(f File, ref string) []check {
	n := f.Name
	srcs := fileSources(f)
	out := []check{{
		cond: fmt.Sprintf("%s == null ? true : length([for k, s in %s : k if s != null]) == 1", ref, ref),
		msg:  hclString(fmt.Sprintf("%s: set exactly one of %s%s", n, orList(srcs), fileHint(f, srcs))),
	}}
	inj := ref + ".injected"
	out = append(out, check{
		cond: guard(ref, inj, fmt.Sprintf(`can(regex("^[a-z0-9]([-a-z0-9.]{0,61}[a-z0-9])?$", %s.provider))`, inj)),
		msg:  hclString(n + ": injected.provider must name the injector as a lowercase label, such as bank-vaults"),
	})
	if has(srcs, "inline") {
		in := ref + ".inline"
		content := inlineContent(f, in)
		if f.Type == "config" {
			out = append(out, check{
				cond: guard(ref, in, fmt.Sprintf("%s || %s", isString(in), isCollection(in))),
				msg:  hclString(n + ": inline content must be text, or an object or a list to write as " + str(f.Raw, "format")),
			})
			switch str(f.Raw, "format") {
			case "toml":
				out = append(out, check{
					cond: guard(ref, in, isString(in)),
					msg:  hclString(n + ": Terraform has no TOML encoder, so give a TOML file's inline content as text, such as a heredoc"),
				})
			case "json":
				out = append(out, check{
					cond: guard(ref, in, fmt.Sprintf("!(%s) ? true : can(jsondecode(%s))", isString(in), in)),
					msg:  hclString(n + ": inline content is not valid json"),
				})
			case "yaml":
				out = append(out, check{
					cond: guard(ref, in, fmt.Sprintf("!(%s) ? true : can(yamldecode(%s))", isString(in), in)),
					msg:  hclString(n + ": inline content is not valid yaml"),
				})
			}
		}
		// The checks below read the content, so they apply once it is valid.
		valid := fmt.Sprintf("!can(%s) ? true : %s == null ? true : ", content, content)
		if m, ok := num(f.Raw, "maxSize"); ok {
			out = append(out, check{
				cond: guard(ref, in, valid+fmt.Sprintf("%s <= %s", byteLength(content), m)),
				msg:  fmt.Sprintf(`"%s: inline content is ${%s} bytes, above maxSize %s"`, n, byteLength(content), m),
			})
		}
		if f.Type == "text" {
			if m, ok := num(f.Raw, "minLength"); ok {
				out = append(out, check{guard(ref, in, codePoints(in)+" >= "+m), fmt.Sprintf(`"%s: inline text is ${%s} characters, below minLength %s"`, n, codePoints(in), m)})
			}
			if m, ok := num(f.Raw, "maxLength"); ok {
				out = append(out, check{guard(ref, in, codePoints(in)+" <= "+m), fmt.Sprintf(`"%s: inline text is ${%s} characters, above maxLength %s"`, n, codePoints(in), m)})
			}
			if p := str(f.Raw, "pattern"); p != "" {
				out = append(out, check{guard(ref, in, fmt.Sprintf("can(regex(%s, %s))", hclString(p), in)), fmt.Sprintf(`"%s: inline text does not match pattern %s"`, n, lit(p))})
			}
		}
		if f.Type == "caBundle" {
			m := "1"
			if x, ok := num(f.Raw, "minCertificates"); ok {
				m = x
			}
			count := fmt.Sprintf(`length(regexall("-----BEGIN CERTIFICATE-----", %s))`, in)
			out = append(out, check{
				guard(ref, in, count+" >= "+m),
				fmt.Sprintf(`"%s: inline bundle holds ${%s} certificate${%s == 1 ? "" : "s"}, need at least %s"`, n, count, count, m),
			})
		}
	}
	if f.Type == "tls" {
		sec := ref + ".secret"
		out = append(out, check{
			guard(ref, sec, sec+".key == null"),
			hclString(n + ": a tls input mounts the whole Secret; remove secret.key"),
		})
	}
	if has(srcs, "certificate") {
		c := ref + ".certificate"
		for _, d := range strs(f.Raw, "dnsNames") {
			wildcard := ""
			if i := strings.Index(d, "."); i >= 0 {
				wildcard = "*" + d[i:]
			}
			out = append(out, check{
				guard(ref, c, fmt.Sprintf("%s.dns_names == null ? true : contains(%s.dns_names, %s) || contains(%s.dns_names, %s)", c, c, hclString(d), c, hclString(wildcard))),
				hclString(fmt.Sprintf("%s: certificate does not cover %s", n, d)),
			})
		}
		if algs := strs(f.Raw, "keyAlgorithms"); len(algs) > 0 {
			q := make([]string, len(algs))
			for i, a := range algs {
				q[i] = hclString(a)
			}
			out = append(out, check{
				guard(ref, c, fmt.Sprintf("try(%s.private_key.algorithm, null) == null ? true : contains([%s], %s.private_key.algorithm)", c, strings.Join(q, ", "), c)),
				fmt.Sprintf(`"%s: certificate key algorithm ${%s.private_key.algorithm} is not one of %s"`, n, c, lit(strings.Join(algs, ", "))),
			})
		}
		for _, field := range []string{"duration", "renew_before"} {
			x := c + "." + field
			out = append(out, check{
				guard(ref, c, fmt.Sprintf(`%s == null ? true : can(regex("^([0-9]+(ns|us|ms|s|m|h))+$", %s))`, x, x)),
				fmt.Sprintf(`"%s: certificate.%s ${jsonencode(%s)} is not a duration such as 1m30s"`, n, field, x),
			})
		}
		if m := str(f.Raw, "minRemaining"); m != "" {
			rb := c + ".renew_before"
			out = append(out, check{
				guard(ref, c, fmt.Sprintf(`%s == null ? true : !can(regex("^([0-9]+(ns|us|ms|s|m|h))+$", %s)) ? true : %s >= %s`, rb, rb, durationNanos(rb), goNanos(m))),
				fmt.Sprintf(`"%s: certificate renewBefore ${%s} is less than minRemaining %s, so the app could see a certificate with too little time left"`, n, rb, m),
			})
		}
	}
	if has(srcs, "image") {
		im := ref + ".image"
		out = append(out, check{
			guard(ref, im, fmt.Sprintf(`%s.pull_policy == null ? true : contains(["Always", "IfNotPresent", "Never"], %s.pull_policy)`, im, im)),
			fmt.Sprintf(`"%s: image.pull_policy ${jsonencode(%s.pull_policy)} is not one of Always, IfNotPresent, Never"`, n, im),
		})
	}
	for i := range out {
		out[i].msg = safeMessage(out[i].msg)
	}
	return out
}

// expandFile replaces a file input's placeholders in an HCL expression
// (#PodMetadata): {input}, {path}, {dir} and {file}.
func expandFile(f File, x string) string {
	for _, r := range [][2]string{{"{input}", f.Name}, {"{path}", f.path()}, {"{dir}", f.mountDir()}, {"{file}", f.fileName()}} {
		x = fmt.Sprintf("replace(%s, %q, %s)", x, r[0], hclString(r[1]))
	}
	return x
}

// volume is an HCL object in the kubernetes provider's volume block shape,
// with every kind present so that all volumes share one type.
func volume(name, cm, secret, csi, image string) string {
	or := func(s string) string {
		if s == "" {
			return "null"
		}
		return s
	}
	return fmt.Sprintf("{ name = %s, config_map = %s, secret = %s, csi = %s, image = %s }", hclString(name), or(cm), or(secret), or(csi), or(image))
}

// fileLocals writes a file input's render locals: its volumes, mounts,
// ConfigMaps, restart triggers, env and pod metadata (#RenderFile).
func (g *gen) fileLocals(b *strings.Builder, f File) {
	ref := "var." + f.Name
	id := ident(f.Name)
	srcs := fileSources(f)
	vol := "dc-" + f.Name
	fn := hclString(f.fileName())
	items := fmt.Sprintf("[{ key = %s, path = %s }]", fn, fn)
	fmt.Fprintf(b, "\n  # %s: %s input at %s\n", f.Name, f.Type, f.path())
	if has(srcs, "inline") {
		in := ref + ".inline"
		fmt.Fprintf(b, "  file_content_%s = %s == null ? null : (%s == null ? null : %s)\n", id, ref, in, inlineContent(f, in))
		fmt.Fprintf(b, "  file_config_map_%s = local.file_content_%s == null ? null : \"%s-%s-${substr(sha256(local.file_content_%s), 0, 10)}\"\n", id, id, g.c.Name, f.Name, id)
	}
	var arms []string
	for _, s := range srcs {
		var v string
		switch s {
		case "inline":
			v = volume(vol, fmt.Sprintf("{ name = local.file_config_map_%s, default_mode = %q, items = %s }", id, f.mode(), items), "", "", "")
		case "config_map":
			v = volume(vol, fmt.Sprintf("{ name = %s.config_map.name, default_mode = %q, items = [{ key = %s.config_map.key, path = %s }] }", ref, f.mode(), ref, fn), "", "", "")
		case "secret":
			it := fmt.Sprintf("[{ key = %s.secret.key, path = %s }]", ref, fn)
			if f.Type == "tls" {
				it = "[]"
			}
			v = volume(vol, "", fmt.Sprintf("{ secret_name = %s.secret.name, default_mode = %q, items = %s }", ref, f.mode(), it), "", "")
		case "certificate":
			v = volume(vol, "", fmt.Sprintf("{ secret_name = %s.certificate.secret_name, default_mode = %q, items = [] }", ref, f.mode()), "", "")
		case "csi":
			v = volume(vol, "", "", fmt.Sprintf("{ driver = %s.csi.driver, read_only = true, volume_attributes = { secretProviderClass = %s.csi.secret_provider_class } }", ref, ref), "")
		case "image":
			v = volume(vol, "", "", "", fmt.Sprintf("{ reference = %s.image.reference, pull_policy = %s.image.pull_policy }", ref, ref))
		default:
			continue // injected: the injector writes the file; nothing is mounted
		}
		arms = append(arms, fmt.Sprintf("%s.%s != null ? [%s]", ref, s, v))
	}
	fmt.Fprintf(b, "  file_volumes_%s = %s == null ? [] : (%s : [])\n", id, ref, strings.Join(arms, " : "))
	fmt.Fprintf(b, "  file_volume_mounts_%s = length(local.file_volumes_%s) == 0 ? [] : [{ name = %s, mount_path = %s, read_only = true }]\n",
		id, id, hclString(vol), hclString(f.mountDir()))
	if has(srcs, "inline") {
		fmt.Fprintf(b, "  file_config_maps_%s = local.file_content_%s == null ? {} : { (local.file_config_map_%s) = { data = { %s = local.file_content_%s }, immutable = true } }\n",
			id, id, id, fn, id)
	} else {
		fmt.Fprintf(b, "  file_config_maps_%s = {}\n", id)
	}
	// A restart trigger names the object whose change must roll the pods.
	var trig []string
	if str(f.Raw, "reload") != "watch" {
		for _, kv := range [][3]string{{"config_map", "ConfigMap", ".name"}, {"secret", "Secret", ".name"}, {"certificate", "Secret", ".secret_name"}, {"csi", "SecretProviderClass", ".secret_provider_class"}} {
			if has(srcs, kv[0]) {
				trig = append(trig, fmt.Sprintf("%s.%s != null ? [{ kind = %q, name = %s.%s%s }]", ref, kv[0], kv[1], ref, kv[0], kv[2]))
			}
		}
	}
	if len(trig) > 0 {
		fmt.Fprintf(b, "  file_restart_triggers_%s = %s == null ? [] : (%s : [])\n", id, ref, strings.Join(trig, " : "))
	} else {
		fmt.Fprintf(b, "  file_restart_triggers_%s = []\n", id)
	}
	if pe := str(f.Raw, "pathEnv"); pe != "" {
		fmt.Fprintf(b, "  file_env_%s = %s == null ? [] : [{ name = %s, value = %s, value_from = null }]\n", id, ref, hclString(pe), hclString(strings.ReplaceAll(f.path(), "$", "$$")))
	}
	for _, kind := range []string{"annotations", "labels"} {
		fmt.Fprintf(b, "  pod_%s_%s = try({ for k, x in %s.injected.pod_%s : %s => %s }, {})\n", kind, id, ref, kind, expandFile(f, "k"), expandFile(f, "x"))
	}
}

// overlayVar reports whether a variable can be written to an overlay, and
// if not, vet's reason.
func (g *gen) overlayVar(v Var, o Overlay) (bool, string) {
	on := o.Name
	switch {
	case v.Secret:
		return false, fmt.Sprintf("is secret, so it cannot go in overlay %s (a ConfigMap); supply it in the environment as a secretKeyRef or injected", on)
	case str(v.Raw, "configKey") == "":
		return false, fmt.Sprintf("has no configKey in the contract, so overlay %s has nowhere to put it", on)
	case g.c.Profiles != nil && g.c.Profiles.Selector == v.Name:
		return false, fmt.Sprintf("is the profile selector, which only the environment can set, not overlay %s", on)
	case len(keyParts(v, o)) > 8:
		return false, fmt.Sprintf("configKey %s is nested deeper than 8 levels", str(v.Raw, "configKey"))
	}
	return true, ""
}

func keyParts(v Var, o Overlay) []string {
	return strings.Split(str(v.Raw, "configKey"), str(o.Raw, "keySeparator"))
}

func overlayRef(o Overlay) string { return "var.overlay_" + o.Name }

// overlayValue is an HCL expression for one variable's value in an
// overlay argument, null when the overlay does not set it.
func overlayValue(o Overlay, v Var) string {
	return fmt.Sprintf("try(%s[%q], null)", overlayRef(o), v.Name)
}

// typeCheck is the condition that an overlay value has its variable's type,
// and vet's message when it has not.
func typeCheck(v Var, x string) (string, string) {
	isNumber := fmt.Sprintf(`can(tonumber(%s)) && jsonencode(%s) == jsonencode(tonumber(%s))`, x, x, x)
	got := fmt.Sprintf("${jsonencode(%s)}", x)
	switch v.Type {
	case "int":
		return isNumber + fmt.Sprintf(" && floor(%s) == %s", x, x), "expected an integer, got " + got
	case "float":
		return isNumber, "expected a number, got " + got
	case "bool":
		return fmt.Sprintf(`contains(["true", "false"], jsonencode(%s))`, x), "expected true or false, got " + got
	case "list":
		item := isString("i")
		if str(v.Raw, "items") == "int" {
			item = `can(tonumber(i)) && jsonencode(i) == jsonencode(tonumber(i)) && floor(i) == i`
		}
		return fmt.Sprintf(`substr(jsonencode(%s), 0, 1) == "[" && alltrue([for i in %s : %s])`, x, x, item), fmt.Sprintf("expected a list of %ss, got %s", str(v.Raw, "items"), got)
	case "json":
		return "true", ""
	case "url":
		return isString(x), "expected a URL string, got " + got
	}
	return isString(x), "expected a string, got " + got
}

var refKinds = `["configMapKeyRef", "fieldRef", "resourceFieldRef", "secretKeyRef", "injected"]`

// overlayChecks are an overlay argument's validations: only variables the
// overlay can carry, each a literal of its type that meets its constraints.
func (g *gen) overlayChecks(o Overlay) []check {
	ref := overlayRef(o)
	on := o.Name
	var elig, secret, noKey, deep []string
	for _, v := range g.c.Vars {
		q := hclString(v.Name)
		switch ok, _ := g.overlayVar(v, o); {
		case ok:
			elig = append(elig, q)
		case v.Secret:
			secret = append(secret, q)
		case str(v.Raw, "configKey") == "":
			noKey = append(noKey, q)
		case g.c.Profiles == nil || g.c.Profiles.Selector != v.Name:
			deep = append(deep, q)
		}
	}
	selector := `""`
	if g.c.Profiles != nil {
		selector = hclString(g.c.Profiles.Selector)
	}
	inList := func(l []string) string { return "[" + strings.Join(l, ", ") + "]" }
	// The first variable the overlay cannot carry, with vet's reason.
	reason := fmt.Sprintf(`contains(%s, k) ? %s : contains(%s, k) ? %s : k == %s ? %s : contains(%s, k) ? "has a configKey nested deeper than 8 levels" : %s`,
		inList(secret), hclString(fmt.Sprintf("is secret, so it cannot go in overlay %s (a ConfigMap); supply it in the environment as a secretKeyRef or injected", on)),
		inList(noKey), hclString(fmt.Sprintf("has no configKey in the contract, so overlay %s has nowhere to put it", on)),
		selector, hclString(fmt.Sprintf("is the profile selector, which only the environment can set, not overlay %s", on)),
		inList(deep), hclString(fmt.Sprintf("is not declared in the contract (check the spelling; in overlay %s)", on)))
	out := []check{
		{
			cond: fmt.Sprintf("%s == null ? true : can(keys(%s))", ref, ref),
			msg:  hclString(fmt.Sprintf("overlay_%s: must be an object of variable names and values", on)),
		},
		{
			cond: fmt.Sprintf("%s == null ? true : !can(keys(%s)) ? true : alltrue([for k in keys(%s) : contains(%s, k)])", ref, ref, ref, inList(elig)),
			// A bare expression, already null-safe, rather than a template.
			msg: fmt.Sprintf(`try([for k in keys(%s) : "${k}: ${%s}" if !contains(%s, k)][0], "")`, ref, reason, inList(elig)),
		},
	}
	suffix := " (in overlay " + on + ")"
	toml := str(o.Raw, "format") == "toml"
	for _, v := range g.c.Vars {
		if ok, _ := g.overlayVar(v, o); !ok {
			continue
		}
		x := overlayValue(o, v)
		set := func(cond string) string {
			return fmt.Sprintf("%s == null ? true : (%s == null ? true : %s)", ref, x, cond)
		}
		out = append(out, check{
			set(fmt.Sprintf("!can(keys(%s)) ? true : length(setintersection(keys(%s), %s)) == 0", x, x, refKinds)),
			hclString(fmt.Sprintf("%s: overlay %s holds values, not references; give a literal, or supply the reference in the environment", v.Name, on)),
		})
		typed, typeMsg := typeCheck(v, x)
		if typeMsg != "" {
			out = append(out, check{set(typed), fmt.Sprintf(`"%s: %s%s"`, v.Name, typeMsg, suffix)})
		}
		if toml && v.Type == "json" {
			out = append(out, check{
				set(fmt.Sprintf(`!strcontains(jsonencode(%s), "{")`, x)),
				hclString(fmt.Sprintf("%s: Terraform has no TOML encoder for objects, so a json value with objects cannot go in TOML overlay %s", v.Name, on)),
			})
		}
		for _, c := range valueChecks(v, x) {
			out = append(out, check{set(fmt.Sprintf("!(%s) ? true : %s", typed, c.cond)), strings.TrimSuffix(c.msg, `"`) + suffix + `"`})
		}
	}
	for i := range out {
		if strings.HasPrefix(out[i].msg, `"`) {
			out[i].msg = safeMessage(out[i].msg)
		}
	}
	return out
}

// overlayNative is a variable's value as the overlay writes it
// (#OverlayData): native types, with a seconds duration as a number and
// any other duration in its encoding, because the host parses it as text.
func overlayNative(v Var, x string) string {
	if v.Type != "duration" {
		return x
	}
	enc := str(v.Raw, "encoding")
	if enc == "seconds" {
		ms := fmt.Sprintf("floor(%s / 1000000)", durationNanos(x))
		return fmt.Sprintf("%s == null ? null : (%s %% 1000 == 0 ? floor(%s / 1000) : %s / 1000)", x, ms, ms, ms)
	}
	if enc == "" || enc == "go" {
		return x
	}
	return fmt.Sprintf("%s == null ? null : %s", x, renderDuration(x, enc))
}

// keyTree is an overlay's configKeys as a tree: leaves are variables.
type keyTree struct {
	leaf     *Var
	children map[string]*keyTree
}

func (g *gen) overlayTree(o Overlay) (*keyTree, error) {
	root := &keyTree{children: map[string]*keyTree{}}
	for i := range g.c.Vars {
		v := g.c.Vars[i]
		if ok, _ := g.overlayVar(v, o); !ok {
			continue
		}
		t := root
		parts := keyParts(v, o)
		for j, p := range parts {
			if t.leaf != nil {
				return nil, fmt.Errorf("overlay %s: configKey %s of %s is inside %s's", o.Name, str(v.Raw, "configKey"), v.Name, t.leaf.Name)
			}
			c := t.children[p]
			if c == nil {
				c = &keyTree{children: map[string]*keyTree{}}
				t.children[p] = c
			}
			t = c
			if j == len(parts)-1 {
				if t.leaf != nil || len(t.children) > 0 {
					return nil, fmt.Errorf("overlay %s: %s and another variable share configKey %s", o.Name, v.Name, str(v.Raw, "configKey"))
				}
				t.leaf = &v
			}
		}
	}
	return root, nil
}

func sortedKeys(m map[string]*keyTree) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(parts []string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		if bareKey.MatchString(p) {
			q[i] = p
		} else {
			q[i] = strconv.Quote(p)
		}
	}
	return strings.Join(q, ".")
}

// overlayLocals writes an overlay's render locals (#RenderOverlay): its
// data at each variable's configKey, the file in the overlay's format, and
// the ConfigMap, volume and mount.
func (g *gen) overlayLocals(b *strings.Builder, o Overlay) error {
	tree, err := g.overlayTree(o)
	if err != nil {
		return err
	}
	ref := overlayRef(o)
	id := ident(o.Name)
	format := str(o.Raw, "format")
	fmt.Fprintf(b, "\n  # overlay %s: %s at %s\n", o.Name, format, str(o.Raw, "path"))
	// Tables are built bottom-up, dropping unset values and empty tables,
	// as #OverlayData writes only the values the platform gives.
	count := 0
	var table func(t *keyTree) string
	table = func(t *keyTree) string {
		var fields []string
		for _, k := range sortedKeys(t.children) {
			c := t.children[k]
			var e string
			if c.leaf != nil {
				e = overlayNative(*c.leaf, overlayValue(o, *c.leaf))
			} else {
				name := table(c)
				e = fmt.Sprintf("length(local.%s) == 0 ? null : local.%s", name, name)
			}
			fields = append(fields, fmt.Sprintf("%s = %s", hclString(k), e))
		}
		name := fmt.Sprintf("overlay_%s_t%d", id, count)
		count++
		fmt.Fprintf(b, "  %s = { for k, x in { %s } : k => x if x != null }\n", name, strings.Join(fields, ", "))
		return name
	}
	data := table(tree)
	var content string
	switch format {
	case "json":
		content = fmt.Sprintf(`"${%s}\n"`, compactJSON("local."+data))
	case "yaml":
		content = fmt.Sprintf("yamlencode(local.%s)", data)
	default:
		// TOML, one dotted key per value, which needs no tables.
		var lines []string
		var walk func(t *keyTree, parts []string)
		walk = func(t *keyTree, parts []string) {
			for _, k := range sortedKeys(t.children) {
				c := t.children[k]
				p := append(append([]string{}, parts...), k)
				if c.leaf != nil {
					x := overlayValue(o, *c.leaf)
					lines = append(lines, fmt.Sprintf(`%s == null ? null : "%s = ${%s}"`, x, lit(tomlKey(p)), compactJSON(overlayNative(*c.leaf, x))))
				} else {
					walk(c, p)
				}
			}
		}
		walk(tree, nil)
		content = fmt.Sprintf(`join("", [for l in compact([%s]) : "${l}\n"])`, strings.Join(lines, ", "))
	}
	fn := hclString(path.Base(str(o.Raw, "path")))
	fmt.Fprintf(b, "  overlay_content_%s = %s == null ? null : %s\n", id, ref, content)
	name := fmt.Sprintf(`"%s-overlay-%s-${substr(sha256(local.overlay_content_%s), 0, 10)}"`, g.c.Name, o.Name, id)
	immutable := "true"
	if str(o.Raw, "reload") == "watch" {
		name = hclString(g.c.Name + "-overlay-" + o.Name)
		immutable = "false"
	}
	fmt.Fprintf(b, "  overlay_config_map_%s = %s == null ? null : %s\n", id, ref, name)
	vol := "dc-overlay-" + o.Name
	fmt.Fprintf(b, "  overlay_volumes_%s = %s == null ? [] : [%s]\n", id, ref,
		volume(vol, fmt.Sprintf("{ name = local.overlay_config_map_%s, default_mode = \"0444\", items = [{ key = %s, path = %s }] }", id, fn, fn), "", "", ""))
	fmt.Fprintf(b, "  overlay_volume_mounts_%s = %s == null ? [] : [{ name = %s, mount_path = %s, read_only = true }]\n", id, ref, hclString(vol), hclString(path.Dir(str(o.Raw, "path"))))
	fmt.Fprintf(b, "  overlay_config_maps_%s = %s == null ? {} : { (local.overlay_config_map_%s) = { data = { %s = local.overlay_content_%s }, immutable = %s } }\n",
		id, ref, id, fn, id, immutable)
	return nil
}

// inOverlays is an HCL condition that some overlay sets the variable.
func (g *gen) inOverlays(v Var) []string {
	var out []string
	for _, o := range g.c.Overlays {
		if ok, _ := g.overlayVar(v, o); ok {
			out = append(out, fmt.Sprintf("(%s == null ? false : %s != null)", overlayRef(o), overlayValue(o, v)))
		}
	}
	return out
}

// suppliedChecks are the preconditions that need several arguments at
// once: a variable set in one place only, and a required variable set
// somewhere (#Validate's _suppliedBy and missingRequired).
func (g *gen) suppliedChecks() []check {
	var out []check
	for _, v := range g.c.Vars {
		ov := g.inOverlays(v)
		if len(ov) == 0 {
			continue
		}
		for _, o := range g.c.Overlays {
			if ok, _ := g.overlayVar(v, o); !ok {
				continue
			}
			out = append(out, check{
				fmt.Sprintf("var.%s == null ? true : !(%s == null ? false : %s != null)", v.Name, overlayRef(o), overlayValue(o, v)),
				hclString(fmt.Sprintf("%s: is set both in the environment and in overlay %s; set it in one place (the environment would win)", v.Name, o.Name)),
			})
		}
		if len(ov) > 1 {
			out = append(out, check{
				fmt.Sprintf("length([for s in [%s] : s if s]) <= 1", strings.Join(ov, ", ")),
				hclString(fmt.Sprintf("%s: is set in more than one overlay; set it in one place", v.Name)),
			})
		}
		if boolean(v.Raw, "required") && !g.profileSupplies(v) {
			out = append(out, check{
				fmt.Sprintf("var.%s != null || %s", v.Name, strings.Join(ov, " || ")),
				hclString(fmt.Sprintf("%s: is required, and set neither by the platform nor by the selected profile", v.Name)),
			})
		}
	}
	return out
}

func (g *gen) profileSupplies(v Var) bool {
	if p := g.c.Profiles; p != nil {
		for _, m := range p.Defaults {
			if _, ok := m[v.Name]; ok {
				return true
			}
		}
	}
	return false
}
