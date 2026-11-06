package lint

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// Levels of a finding (spec/IAC.md 7.2): errors fail the lint.
const (
	Error   = "error"
	Warning = "warning"
	Notice  = "notice"
)

// Finding is one problem or note.
type Finding struct {
	Code    string `json:"code"`
	Level   string `json:"level"`
	Module  string `json:"module,omitempty"`
	Where   string `json:"where,omitempty"` // workload and container
	Input   string `json:"input,omitempty"`
	Message string `json:"message"`
}

// Docs are the values, files and overlays documents the linter rebuilt for
// one bound container, for vet's engine to check.
type Docs struct {
	Module   *Module
	Where    string
	Values   map[string]any
	Files    map[string]any
	Overlays map[string]any
	// Deferred names the inputs left out because their value is unknown
	// or sensitive at plan.
	Deferred map[string]bool
	// Refs are references in their target's form, which only the policy
	// sees (spec/IAC.md 7.3): {awsSecretsManager: {account: …}}.
	Refs     map[string]any
	Bindings []Binding
}

// Binding is one row of -explain.
type Binding struct {
	Input, Source, Origin, AtPlan string
}

// Check applies the rules of spec/IAC.md 7.2 that need only the plan, and
// returns the documents to check with vet's engine, one per bound
// container (or one per module that no workload takes).
func Check(p *Plan) ([]Finding, []Docs) {
	var out []Finding
	var docs []Docs
	// A workload binds to the docuconf modules its configuration
	// references. The plan does not show references made in dynamic
	// blocks, so a workload that references none binds by the env names
	// its containers set, when exactly one module's contract matches.
	bound := map[*Module][]*Workload{}
	for _, w := range p.Workloads {
		var refd, matches []*Module
		for _, m := range p.Modules {
			if w.Scope != m.Scope || w.Target != m.Target() {
				continue
			}
			if ok, _ := refersTo(w.Config, "module."+m.Name); ok {
				refd = append(refd, m)
			} else if appContainer(w, m.Contract) >= 0 {
				matches = append(matches, m)
			}
		}
		if p.Pulumi && w.Unknown && len(refd) == 0 {
			// A preview hides the whole container list when anything in it
			// is secret or unknown, so no env names match: such a workload
			// binds to the one component for its target, if there is one.
			for _, m := range p.Modules {
				if m.Target() == w.Target {
					matches = append(matches, m)
				}
			}
		}
		switch {
		case len(refd) > 0:
			for _, m := range refd {
				bound[m] = append(bound[m], w)
			}
		case len(matches) == 1:
			bound[matches[0]] = append(bound[matches[0]], w)
		case len(matches) > 1:
			var names []string
			for _, m := range matches {
				names = append(names, m.Addr)
			}
			out = append(out, Finding{Code: "binding_ambiguous", Level: Notice, Where: w.Addr,
				Message: fmt.Sprintf("%s sets env from one of %s, and the %s does not say which, so it is not checked; %s", w.Addr, strings.Join(names, ", "), p.noun(), bindHint(p))})
		}
	}
	for _, m := range p.Modules {
		c := m.Contract
		if t := m.Target(); t != "kubernetes" && t != "ecs" && t != "cloudrun" {
			out = append(out, Finding{Code: "target_unchecked", Level: Notice, Module: m.Addr,
				Message: fmt.Sprintf("the linter does not check the %s target yet; the %s's own checks still run", t, m.unit())})
			continue
		}
		out = append(out, Finding{Code: "image_unverified", Level: Notice, Module: m.Addr,
			Message: fmt.Sprintf("the %s records no image digest, so the linter cannot check that the bound container runs the image the contract describes", m.unit())})
		for _, w := range bound[m] {
			_, attrs := refersTo(w.Config, "module."+m.Name)
			if w.Unknown {
				// What the containers get is known only during apply: the
				// module's preconditions check it then; here, its arguments.
				msg := fmt.Sprintf("%s's container definitions are unknown at plan, so the linter checks only %s's arguments; the module's preconditions check the values at apply", w.Addr, m.Addr)
				if m.Pulumi {
					msg = fmt.Sprintf("%s's containers are unknown or secret at preview, so the linter checks only %s's arguments; the values are checked during the update, by the component's checks", w.Addr, m.Addr)
				}
				out = append(out, Finding{Code: "deferred", Level: Notice, Module: m.Addr, Where: w.Addr, Message: msg})
				docs = append(docs, constantDocs(m, w.Addr))
				continue
			}
			app := appContainer(w, c)
			if app < 0 {
				if len(renderedNames(m)) > 0 {
					output := m.out("kubernetes_env") + " output in the app container's env"
					switch m.Target() {
					case "ecs":
						output = m.out("ecs_environment") + " and " + m.out("ecs_secrets") + " outputs in the app's container definition"
					case "cloudrun":
						output = m.out("cloudrun_env") + " output in the app container's env"
					}
					out = append(out, Finding{Code: "unbound_container", Level: Error, Module: m.Addr, Where: w.Addr,
						Message: fmt.Sprintf("%s reads %s, but none of its containers takes the env the %s renders; use its %s", w.Addr, m.Addr, m.unit(), output)})
				}
				continue
			}
			ct := w.Containers[app]
			where := fmt.Sprintf("%s, container %q", w.Addr, ct.Name)
			if ct.Name == "" {
				where = fmt.Sprintf("%s, container %d", w.Addr, app+1) // Cloud Run containers may be unnamed
			}
			f, d := checkContainer(m, w, ct, where)
			out = append(out, f...)
			if w.Target == "kubernetes" {
				out = append(out, checkPodMeta(m, w, where, attrs)...)
			}
			docs = append(docs, d)
		}
		if len(bound[m]) == 0 {
			out = append(out, Finding{Code: "module_unused", Level: Warning, Module: m.Addr,
				Message: fmt.Sprintf("no workload in the %s takes %s's outputs, so only its arguments are checked", p.noun(), m.Addr)})
			docs = append(docs, constantDocs(m, m.Addr))
		}
	}
	return out, docs
}

// appContainer is the container that takes the module's env: the one
// setting the most of the contract's variables. -1 when none sets any.
func appContainer(w *Workload, c *terraform.Contract) int {
	best, n := -1, 0
	for i, ct := range w.Containers {
		k := 0
		for _, e := range ct.Env {
			if varOf(c, e.Name) != nil {
				k++
			}
		}
		if k > n {
			best, n = i, k
		}
	}
	return best
}

var indexedName = regexp.MustCompile(`^(.+)__([0-9]+)$`)

// varOf finds the variable an env name belongs to: its own name, or
// NAME__i of an indexed list or key set.
func varOf(c *terraform.Contract, name string) *terraform.Var {
	for i, v := range c.Vars {
		if v.Name == name {
			return &c.Vars[i]
		}
	}
	if m := indexedName.FindStringSubmatch(name); m != nil {
		for i, v := range c.Vars {
			if v.Name == m[1] && str(v.Raw, "encoding") == "indexed" {
				return &c.Vars[i]
			}
		}
	}
	return nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// source is the attribute a constant argument sets: value, secret, … .
func source(x Expr) string {
	m, ok := x.Constant.(map[string]any)
	if !x.IsConstant || !ok {
		return ""
	}
	for k, v := range m {
		if v != nil {
			return k
		}
	}
	return ""
}

// renderedNames are the env names the module certainly renders: from
// constant arguments whose source reaches the env.
func renderedNames(m *Module) []string {
	var out []string
	for _, v := range m.Contract.Vars {
		x, ok := m.Args[v.Name]
		if !ok {
			continue
		}
		switch src := source(x); src {
		case "":
			continue
		case "injected":
			if at(x.Constant, "injected", "ref") == nil {
				continue // the injector sets the variable itself
			}
		case "value":
			if str(v.Raw, "encoding") == "indexed" {
				if l, _ := at(x.Constant, "value").([]any); len(l) > 0 {
					out = append(out, v.Name+"__0")
				}
				continue
			}
		}
		out = append(out, v.Name)
	}
	return out
}

func boolArg(m *Module, path ...any) bool {
	x := m.Args["docuconf_options"]
	b, _ := at(x.Constant, path...).(bool)
	return x.IsConstant && b
}

func checkContainer(m *Module, w *Workload, ct Container, where string) ([]Finding, Docs) {
	c := m.Contract
	var out []Finding
	add := func(code, level, input, msg string) {
		out = append(out, Finding{Code: code, Level: level, Module: m.Addr, Where: where, Input: input, Message: msg})
	}
	declared := map[string]bool{}
	for _, f := range c.Files {
		if pe := str(f.Raw, "pathEnv"); pe != "" {
			declared[pe] = true
		}
	}
	if x := m.Args["docuconf_options"]; x.IsConstant {
		if l, ok := at(x.Constant, "unmanaged_env").([]any); ok {
			for _, n := range l {
				if s, ok := n.(string); ok {
					declared[s] = true
				}
			}
		}
	}
	seen := map[string]int{}
	env := map[string]Env{}
	for _, e := range ct.Env {
		seen[e.Name]++
		env[e.Name] = e
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if seen[n] > 1 {
			add("duplicate_env", Error, n, fmt.Sprintf("%s: is set %d times in the container's env; Kubernetes keeps the last, so set it once, through the %s", n, seen[n], m.unit()))
		}
		v := varOf(c, n)
		if v == nil && !declared[n] {
			msg := fmt.Sprintf("%s: is not declared in the contract, and the %s did not render it", n, m.unit())
			if s := closest(c, n); s != "" {
				msg += fmt.Sprintf("; did you mean %s?", s)
			} else {
				msg += "; if the platform sets it on purpose, list it in " + m.option("unmanaged_env")
			}
			add("undeclared_input", Error, n, msg)
			continue
		}
		if v != nil && v.Secret {
			e := env[n]
			if _, set := m.Args[v.Name]; !set && e.Value != nil && e.ValueFrom == nil {
				add("secret_as_value", Error, n, fmt.Sprintf("%s: is secret, but the container sets it to a plain value; take it from the %s, as a secret reference", n, m.unit()))
			}
		}
	}
	for _, n := range renderedNames(m) {
		if seen[n] == 0 {
			from := m.Addr + "." + m.out("kubernetes_env")
			switch w.Target {
			case "ecs":
				from = m.Addr + "." + m.out("ecs_environment") + " and ." + m.out("ecs_secrets")
			case "cloudrun":
				from = m.Addr + "." + m.out("cloudrun_env")
			}
			add("unbound_container", Error, strings.TrimSuffix(n, "__0"), fmt.Sprintf("%s: %s renders it, but the container does not set it; take the container's env from %s", strings.TrimSuffix(n, "__0"), m.Addr, from))
		}
	}
	if len(ct.EnvFrom) > 0 {
		level := Error
		if boolArg(m, "allow_env_from") {
			level = Warning
		}
		if w.Target == "ecs" {
			add("env_from_unchecked", Error, "", "the container takes environmentFiles, whose keys the linter cannot see before apply; set each input through the "+m.unit()+" instead")
		} else {
			add("env_from_unchecked", level, "", "the container takes "+m.term("env_from")+" a whole ConfigMap or Secret, whose keys the linter cannot see before apply; set each input through the "+m.unit()+" instead, or set "+m.option("allow_env_from"))
		}
	}
	if w.Target == "kubernetes" || w.Target == "cloudrun" {
		out = append(out, checkMounts(m, w, ct)...)
	}
	for i := range out {
		out[i].Where = where
	}
	d := containerDocs(m, w, ct, where)
	for _, n := range sortedKeys(d.Deferred) {
		origin := "an unknown value"
		if x, ok := m.Args[n]; ok && len(x.References) > 0 {
			origin = x.References[0]
		}
		checked := "at boot"
		if _, ok := m.Args[n]; ok {
			checked = m.when() + ", by " + m.applyChecks() + ", and at boot"
		}
		why := "is unknown at plan"
		if m.Pulumi {
			why = "is unknown at preview"
		}
		if e, ok := env[n]; ok && e.Sensitive && !e.Unknown {
			why = "is " + m.sensitive() + ", so the linter does not read it"
		}
		out = append(out, Finding{Code: "deferred", Level: Notice, Module: m.Addr, Where: where, Input: n,
			Message: fmt.Sprintf("%s: %s (from %s); it is checked %s", n, why, origin, checked)})
	}
	return out, d
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkMounts checks that each file input and overlay the module renders
// a volume for is mounted where the contract says, read-only and whole
// (SPEC.md 4.6).
func checkMounts(m *Module, w *Workload, ct Container) []Finding {
	var out []Finding
	vols := map[string]bool{}
	for _, v := range w.Volumes {
		if n, ok := v["name"].(string); ok {
			vols[n] = true
		}
	}
	volumes, mounts := m.out("kubernetes_volumes"), m.out("kubernetes_volume_mounts")
	if w.Target == "cloudrun" {
		volumes, mounts = m.out("cloudrun_volumes"), m.out("cloudrun_volume_mounts")
	}
	check := func(input, vol, dir string) {
		add := func(msg string) {
			out = append(out, Finding{Code: "mount_mismatch", Level: Error, Module: m.Addr, Input: input, Message: input + ": " + msg})
		}
		if !vols[vol] {
			add(fmt.Sprintf("the pod has no volume %s; take its volumes from %s.%s", vol, m.Addr, volumes))
		}
		var mt *Mount
		for i := range ct.Mounts {
			if ct.Mounts[i].Name == vol {
				mt = &ct.Mounts[i]
			}
		}
		switch {
		case mt == nil:
			add(fmt.Sprintf("the container does not mount volume %s; take its mounts from %s.%s", vol, m.Addr, mounts))
		case mt.MountPath != dir:
			add(fmt.Sprintf("volume %s is mounted at %s, but the app reads it from %s", vol, mt.MountPath, dir))
		case mt.SubPath != "":
			add(fmt.Sprintf("volume %s is mounted with %s %s, which the kubelet never updates; mount the directory", vol, m.term("sub_path"), mt.SubPath))
		case !mt.ReadOnly:
			add(fmt.Sprintf("volume %s is mounted writable; mount it read-only", vol))
		}
	}
	for _, f := range m.Contract.Files {
		x, ok := m.Args[f.Name]
		if src := source(x); !ok || src == "" || src == "injected" {
			continue
		}
		check(f.Name, "dc-"+f.Name, mountDir(f))
	}
	for _, o := range m.Contract.Overlays {
		x, ok := m.Args["overlay_"+o.Name]
		if !ok || !x.IsConstant || x.Constant == nil {
			continue
		}
		p, _ := o.Raw["path"].(string)
		check("overlay "+o.Name, "dc-overlay-"+o.Name, path.Dir(p))
	}
	return out
}

// checkPodMeta checks that the module's pod metadata is on the pod
// template, not the workload, and that what the module's constant
// arguments ask for is there.
func checkPodMeta(m *Module, w *Workload, where string, attrs []string) []Finding {
	var out []Finding
	if templatePath(w.Type) != nil {
		if _, a := refersTo(at(w.Config, "metadata"), "module."+m.Name); len(a) > 0 {
			for _, r := range a {
				if strings.HasSuffix(r, ".pod_annotations") || strings.HasSuffix(r, ".pod_labels") {
					out = append(out, Finding{Code: "pod_metadata_misplaced", Level: Error, Module: m.Addr, Where: where,
						Message: fmt.Sprintf("%s is on the workload's metadata, where injectors never look; put it on the pod template's metadata", r)})
				}
			}
		}
	}
	ann, lab := expectedPodMeta(m)
	if w.PodMeta.Unknown {
		return out
	}
	for _, kv := range []struct {
		kind string
		want map[string]string
		got  map[string]any
	}{{"annotation", ann, w.PodMeta.Annotations}, {"label", lab, w.PodMeta.Labels}} {
		for _, k := range sortedStrings(kv.want) {
			if g, _ := kv.got[k].(string); g != kv.want[k] {
				out = append(out, Finding{Code: "pod_metadata_missing", Level: Error, Module: m.Addr, Where: where,
					Message: fmt.Sprintf("the pod template lacks %s %s=%s, which an injector needs; set its metadata from %s.%s", kv.kind, k, kv.want[k], m.Addr, m.out("pod_"+kv.kind+"s"))})
			}
		}
	}
	_ = attrs
	return out
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// expectedPodMeta is the pod metadata the module's constant arguments ask
// for, placeholders expanded (SPEC.md 4.5.2).
func expectedPodMeta(m *Module) (map[string]string, map[string]string) {
	ann, lab := map[string]string{}, map[string]string{}
	put := func(dst map[string]string, src any, r *strings.Replacer) {
		mm, _ := src.(map[string]any)
		for k, v := range mm {
			if s, ok := v.(string); ok {
				dst[r.Replace(k)] = r.Replace(s)
			}
		}
	}
	none := strings.NewReplacer()
	if x := m.Args["docuconf_options"]; x.IsConstant {
		put(ann, at(x.Constant, "pod_annotations"), none)
		put(lab, at(x.Constant, "pod_labels"), none)
	}
	for _, v := range m.Contract.Vars {
		if x := m.Args[v.Name]; source(x) == "injected" {
			r := strings.NewReplacer("{input}", v.Name)
			put(ann, at(x.Constant, "injected", "pod_annotations"), r)
			put(lab, at(x.Constant, "injected", "pod_labels"), r)
		}
	}
	for _, f := range m.Contract.Files {
		if x := m.Args[f.Name]; source(x) == "injected" {
			p, _ := f.Raw["path"].(string)
			r := strings.NewReplacer("{input}", f.Name, "{path}", p, "{dir}", mountDir(f), "{file}", path.Base(p))
			put(ann, at(x.Constant, "injected", "pod_annotations"), r)
			put(lab, at(x.Constant, "injected", "pod_labels"), r)
		}
	}
	return ann, lab
}

// closest suggests a declared variable for a misspelt env name.
func closest(c *terraform.Contract, name string) string {
	best, bd := "", 3
	for _, v := range c.Vars {
		if d := distance(strings.ToUpper(name), v.Name); d < bd {
			best, bd = v.Name, d
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// camelKeys turns a module argument's attribute names into the values
// documents' (config_map → configMap), except inside data: inline
// content and pod metadata.
func camelKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if y == nil {
				continue
			}
			ck := camel(k)
			switch k {
			case "inline", "pod_annotations", "pod_labels", "value":
				out[ck] = y
			default:
				out[ck] = camelKeys(y)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = camelKeys(y)
		}
		return out
	}
	return v
}

func camel(k string) string {
	parts := strings.Split(k, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// argValue is a variable's entry in the values document, from a constant
// argument: the literal, or the reference in the document's form.
func argValue(x Expr) (any, bool) {
	src := source(x)
	if src == "" {
		return nil, false
	}
	a := at(x.Constant, src)
	switch src {
	case "value":
		return a, true
	case "secret":
		return map[string]any{"secretKeyRef": a}, true
	case "config_map":
		return map[string]any{"configMapKeyRef": a}, true
	case "field":
		return map[string]any{"fieldRef": camelKeys(a)}, true
	case "resource_field":
		return map[string]any{"resourceFieldRef": camelKeys(a)}, true
	case "injected":
		return map[string]any{"injected": camelKeys(a)}, true
	}
	return nil, false
}

// constantDocs builds the documents from the module's constant arguments
// alone, when no workload takes its outputs.
func constantDocs(m *Module, where string) Docs {
	d := Docs{Module: m, Where: where, Values: map[string]any{}, Files: map[string]any{}, Overlays: map[string]any{}, Deferred: map[string]bool{}, Refs: map[string]any{}}
	for _, v := range m.Contract.Vars {
		x, ok := m.Args[v.Name]
		if !ok {
			continue
		}
		if m.Target() == "cloudrun" && source(x) == "secret" {
			if sec, ok := at(x.Constant, "secret", "secret").(string); ok {
				ver, _ := at(x.Constant, "secret", "version").(string)
				d.gcpSecret(v.Name, sec, ver)
				continue
			}
		}
		if m.Target() == "ecs" && source(x) == "secret" {
			if _, ok := at(x.Constant, "secret", "value_from").(string); ok {
				d.ecsSecret(v.Name, ecsValueFrom(x.Constant))
				continue
			}
		}
		if val, ok := argValue(x); ok {
			d.Values[v.Name] = val
		} else if !x.IsConstant {
			d.Deferred[v.Name] = true
		}
	}
	d.fillShared(m)
	return d
}

// fillShared adds what only the module's arguments hold: shared pod
// metadata, file sources and overlays.
func (d *Docs) fillShared(m *Module) {
	if x := m.Args["docuconf_options"]; x.IsConstant {
		if a := at(x.Constant, "pod_annotations"); a != nil {
			d.Values["podAnnotations"] = a
		}
		if l := at(x.Constant, "pod_labels"); l != nil {
			d.Values["podLabels"] = l
		}
	}
	for _, f := range m.Contract.Files {
		x, ok := m.Args[f.Name]
		if !ok {
			continue
		}
		if !x.IsConstant {
			d.Deferred[f.Name] = true
			continue
		}
		if x.Constant == nil {
			continue
		}
		if m.Target() == "cloudrun" {
			// A Secret Manager secret, in the files document's secret form,
			// which #CheckFile accepts for a file input.
			if sec, ok := at(x.Constant, "secret", "secret").(string); ok {
				ver, _ := at(x.Constant, "secret", "version").(string)
				if ver == "" {
					ver = "latest"
				}
				d.Files[f.Name] = map[string]any{"secret": map[string]any{"name": sec, "key": ver}}
				continue
			}
		}
		d.Files[f.Name] = camelKeys(x.Constant)
	}
	for _, o := range m.Contract.Overlays {
		x, ok := m.Args["overlay_"+o.Name]
		if !ok {
			continue
		}
		if !x.IsConstant {
			d.Deferred["overlay "+o.Name] = true
			continue
		}
		if x.Constant != nil {
			d.Overlays[o.Name] = x.Constant
		}
	}
	d.Bindings = bindings(m, d)
}

// containerDocs builds the documents from what the container receives:
// each variable's env entry, read back from its wire form (SPEC.md 5), or
// the module's constant argument where that says more (an injected source).
func containerDocs(m *Module, w *Workload, ct Container, where string) Docs {
	d := Docs{Module: m, Where: where, Values: map[string]any{}, Files: map[string]any{}, Overlays: map[string]any{}, Deferred: map[string]bool{}, Refs: map[string]any{}}
	unescape := unescape
	if w.Target != "kubernetes" {
		unescape = func(s string) string { return s } // only the kubelet expands $(VAR)
	}
	env := map[string]Env{}
	for _, e := range ct.Env {
		env[e.Name] = e
	}
	for _, v := range m.Contract.Vars {
		if x, ok := m.Args[v.Name]; ok && source(x) == "injected" {
			val, _ := argValue(x)
			d.Values[v.Name] = val
			continue
		}
		if str(v.Raw, "encoding") == "indexed" {
			var items []Env
			for i := 0; ; i++ {
				e, ok := env[fmt.Sprintf("%s__%d", v.Name, i)]
				if !ok {
					break
				}
				items = append(items, e)
			}
			if len(items) == 0 {
				continue
			}
			var l []any
			for _, e := range items {
				if e.Unknown || e.Sensitive || e.Value == nil {
					d.Deferred[v.Name] = true
					break
				}
				l = append(l, itemValue(v, unescape(*e.Value)))
			}
			if !d.Deferred[v.Name] {
				d.Values[v.Name] = l
			}
			continue
		}
		e, ok := env[v.Name]
		if !ok {
			if m.Unknown[v.Name] {
				d.Deferred[v.Name] = true
			}
			continue
		}
		switch {
		case e.Unknown, e.Sensitive && e.ValueFrom == nil:
			d.Deferred[v.Name] = true
		case e.ValueFrom != nil && e.ValueFrom["gcp"] != nil:
			r, _ := e.ValueFrom["gcp"].(map[string]any)
			sec, _ := r["secret"].(string)
			ver, _ := r["version"].(string)
			d.gcpSecret(v.Name, sec, ver)
		case e.ValueFrom != nil && e.ValueFrom["ecs"] != nil:
			vf, _ := e.ValueFrom["ecs"].(string)
			d.ecsSecret(v.Name, vf)
		case e.ValueFrom != nil:
			if r := reference(e.ValueFrom); r != nil {
				d.Values[v.Name] = r
			}
		case e.Value != nil && v.Secret:
			// secret_as_value reports it; vet would only say it again.
		case e.Value != nil:
			d.Values[v.Name] = fromWire(v, unescape(*e.Value))
		}
	}
	d.fillShared(m)
	return d
}

// unescape is what the kubelet does to an env value: $$ becomes $.
func unescape(s string) string { return strings.ReplaceAll(s, "$$", "$") }

// reference reads a planned value_from block as the values document's
// reference.
func reference(vf map[string]any) map[string]any {
	first := func(k string) map[string]any {
		m, _ := at(vf, k, 0).(map[string]any)
		return m
	}
	if r := first("secret_key_ref"); r != nil {
		return map[string]any{"secretKeyRef": map[string]any{"name": r["name"], "key": r["key"]}}
	}
	if r := first("config_map_key_ref"); r != nil {
		return map[string]any{"configMapKeyRef": map[string]any{"name": r["name"], "key": r["key"]}}
	}
	if r := first("field_ref"); r != nil {
		return map[string]any{"fieldRef": map[string]any{"fieldPath": r["field_path"]}}
	}
	if r := first("resource_field_ref"); r != nil {
		out := map[string]any{"resource": r["resource"]}
		if s, _ := r["divisor"].(string); s != "" {
			out["divisor"] = s
		}
		if s, _ := r["container_name"].(string); s != "" {
			out["containerName"] = s
		}
		return map[string]any{"resourceFieldRef": out}
	}
	return nil
}

// fromWire reads an env value back into the value the platform set, in
// the variable's type (SPEC.md 5). A value that does not parse is kept as
// text, so vet reports it.
func fromWire(v terraform.Var, s string) any {
	switch v.Type {
	case "int", "float":
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return json.Number(strconv.FormatFloat(n, 'g', -1, 64))
		}
	case "bool":
		if b, err := strconv.ParseBool(s); err == nil && (s == "true" || s == "false") {
			return b
		}
	case "json":
		var x any
		if json.Unmarshal([]byte(s), &x) == nil {
			return x
		}
	case "duration":
		if d, ok := goDuration(s, str(v.Raw, "encoding")); ok {
			return d
		}
	case "list":
		if str(v.Raw, "encoding") == "json" {
			var x []any
			if json.Unmarshal([]byte(s), &x) == nil {
				return x
			}
			return s
		}
		sep := ","
		if x, ok := v.Raw["separator"].(string); ok {
			sep = x
		}
		var l []any
		if s != "" {
			for _, p := range strings.Split(s, sep) {
				l = append(l, itemValue(v, p))
			}
		}
		if l == nil {
			l = []any{}
		}
		return l
	}
	return s
}

func itemValue(v terraform.Var, s string) any {
	if str(v.Raw, "items") == "int" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	return s
}

var (
	isoDuration      = regexp.MustCompile(`^PT([0-9]+)(?:\.([0-9]{1,3}))?S$`)
	timespanDuration = regexp.MustCompile(`^(?:([0-9]+)\.)?([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,3}))?$`)
	secondsDuration  = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]{1,3}))?$`)
)

// goDuration reads a duration in its wire encoding back into Go syntax,
// in milliseconds when it has a fraction.
func goDuration(s, enc string) (string, bool) {
	if enc == "" || enc == "go" {
		return s, true
	}
	frac := func(f string) int64 {
		if f == "" {
			return 0
		}
		n, _ := strconv.ParseInt((f + "00")[:3], 10, 64)
		return n
	}
	var secs, ms int64
	switch enc {
	case "seconds":
		m := secondsDuration.FindStringSubmatch(s)
		if m == nil {
			return "", false
		}
		secs, _ = strconv.ParseInt(m[1], 10, 64)
		ms = frac(m[2])
	case "iso8601":
		m := isoDuration.FindStringSubmatch(s)
		if m == nil {
			return "", false
		}
		secs, _ = strconv.ParseInt(m[1], 10, 64)
		ms = frac(m[2])
	case "timespan":
		m := timespanDuration.FindStringSubmatch(s)
		if m == nil {
			return "", false
		}
		var days, h, mi, se int64
		if m[1] != "" {
			days, _ = strconv.ParseInt(m[1], 10, 64)
		}
		h, _ = strconv.ParseInt(m[2], 10, 64)
		mi, _ = strconv.ParseInt(m[3], 10, 64)
		se, _ = strconv.ParseInt(m[4], 10, 64)
		secs = days*86400 + h*3600 + mi*60 + se
		ms = frac(m[5])
	default:
		return "", false
	}
	if ms == 0 {
		return fmt.Sprintf("%ds", secs), true
	}
	return fmt.Sprintf("%dms", secs*1000+ms), true
}

// bindings are the -explain rows: how each input is set, from where, and
// what the linter could check at plan.
func bindings(m *Module, d *Docs) []Binding {
	var out []Binding
	row := func(name string, x Expr, set bool, isSecret bool) {
		b := Binding{Input: name, Source: "—", Origin: "unset", AtPlan: "—"}
		if set {
			b.Source = source(x)
			switch {
			case x.Constant == nil && x.References == nil:
				// Not a module argument: written into the container by hand.
				b.Source = "by hand"
				b.Origin = "in the container, not through the module"
			case !x.IsConstant:
				// The source the container got, read back from its env.
				b.Source = "value"
				if r, ok := d.Values[name].(map[string]any); ok {
					for k, s := range map[string]string{"secretKeyRef": "secret", "configMapKeyRef": "config_map", "fieldRef": "field", "resourceFieldRef": "resource_field", "injected": "injected"} {
						if r[k] != nil {
							b.Source = s
						}
					}
				}
				b.Origin = "an expression"
				if len(x.References) > 0 {
					b.Origin = x.References[0]
				}
			case b.Source == "injected":
				p, _ := at(x.Constant, "injected", "provider").(string)
				b.Origin = p
			case b.Source == "value" && !isSecret:
				j, _ := json.Marshal(at(x.Constant, "value"))
				b.Origin = "literal " + string(j)
			default:
				j, _ := json.Marshal(at(x.Constant, b.Source))
				b.Origin = string(j)
			}
		}
		if raw, ok := m.Raw[name].(map[string]any); ok && set && b.Source != "by hand" {
			// In the component's words: secretKeyRef, not secret.
			for k, v := range raw {
				b.Source = k
				if !x.IsConstant {
					break
				}
				if k != "value" || isSecret {
					j, _ := json.Marshal(v)
					b.Origin = string(j)
				}
				if k == "injected" {
					p, _ := at(v, "provider").(string)
					b.Origin = p
				}
			}
		}
		_, inDoc := d.Values[name]
		if !inDoc {
			_, inDoc = d.Files[name]
		}
		switch {
		case d.Deferred[name]:
			b.AtPlan = "unknown: checked at apply"
			if m.Pulumi {
				b.AtPlan = "unknown: checked during the update"
			}
		case b.Source == "injected":
			b.AtPlan = "checked at boot"
		case inDoc:
			b.AtPlan = "known, checked"
		}
		out = append(out, b)
	}
	for _, v := range m.Contract.Vars {
		x, ok := m.Args[v.Name]
		_, inDoc := d.Values[v.Name]
		row(v.Name, x, ok || inDoc, v.Secret)
	}
	if m.Target() != "ecs" {
		for _, f := range m.Contract.Files {
			if m.Target() == "cloudrun" && f.Type == "tls" {
				continue
			}
			x, ok := m.Args[f.Name]
			row(f.Name, x, ok, f.Secret)
		}
	}
	return out
}

// ecsSecret records an ECS secret: in the values document as a secret
// reference, which #Validate accepts for a secret input, and in Refs in
// its own form, for the policy.
func (d *Docs) ecsSecret(name, valueFrom string) {
	d.Values[name] = map[string]any{"secretKeyRef": map[string]any{"name": valueFrom, "key": ""}}
	d.Refs[name] = ParseValueFrom(valueFrom)
}

// ecsValueFrom is a constant secret argument's valueFrom, as the module
// renders it.
func ecsValueFrom(arg any) string {
	vf, _ := at(arg, "secret", "value_from").(string)
	sel := func(k string) string { s, _ := at(arg, "secret", k).(string); return s }
	if sel("json_key") == "" && sel("version_stage") == "" && sel("version_id") == "" {
		return vf
	}
	return vf + ":" + sel("json_key") + ":" + sel("version_stage") + ":" + sel("version_id")
}

var (
	smValueFrom  = regexp.MustCompile(`^(arn:(aws[a-z-]*):secretsmanager:([a-z0-9-]+):([0-9]{12}):secret:([^:]+))(?::([^:]*):([^:]*):([^:]*))?$`)
	ssmValueFrom = regexp.MustCompile(`^arn:(aws[a-z-]*):ssm:([a-z0-9-]+):([0-9]{12}):parameter(/.+)$`)
	smSuffix     = regexp.MustCompile(`-[A-Za-z0-9]{6}$`)
)

// ParseValueFrom reads an ECS secrets[].valueFrom into the policy's form
// (spec/IAC.md 7.3): a Secrets Manager secret or a Parameter Store
// parameter, with its partition, region, account and name. A bare name is
// a parameter in the task's own account and region, which the plan does
// not record, so only its name is known.
func ParseValueFrom(vf string) map[string]any {
	if m := smValueFrom.FindStringSubmatch(vf); m != nil {
		f := map[string]any{"arn": m[1], "partition": m[2], "region": m[3], "account": m[4], "name": smSuffix.ReplaceAllString(m[5], "")}
		for i, k := range []string{"jsonKey", "versionStage", "versionId"} {
			if m[6+i] != "" {
				f[k] = m[6+i]
			}
		}
		return map[string]any{"awsSecretsManager": f}
	}
	if m := ssmValueFrom.FindStringSubmatch(vf); m != nil {
		return map[string]any{"awsSsmParameter": map[string]any{"arn": vf, "partition": m[1], "region": m[2], "account": m[3], "name": m[4]}}
	}
	return map[string]any{"awsSsmParameter": map[string]any{"name": vf}}
}

// gcpSecret records a Secret Manager secret version: in the values
// document as a secret reference, and in Refs in its policy form.
func (d *Docs) gcpSecret(name, secret, version string) {
	if version == "" {
		version = "latest"
	}
	d.Values[name] = map[string]any{"secretKeyRef": map[string]any{"name": secret, "key": version}}
	d.Refs[name] = ParseGCPSecret(secret, version)
}

var gcpSecretName = regexp.MustCompile(`^projects/([^/]+)/secrets/([^/]+)$`)

// ParseGCPSecret reads a Cloud Run secret_key_ref into the policy's form
// (spec/IAC.md 7.3). A bare secret ID is in the service's own project,
// which the plan does not record, so only the secret and version are
// known.
func ParseGCPSecret(secret, version string) map[string]any {
	f := map[string]any{"secret": secret, "version": version}
	if m := gcpSecretName.FindStringSubmatch(secret); m != nil {
		f["project"], f["secret"] = m[1], m[2]
	}
	return map[string]any{"gcpSecretManager": f}
}

// The words of a message, for a Terraform module or a Pulumi component.

func (m *Module) unit() string {
	if m.Pulumi {
		return "component"
	}
	return "module"
}

// out is an output's name: kubernetes_env, or the component's
// kubernetesEnv (KubernetesEnv in Go).
func (m *Module) out(tf string) string {
	if !m.Pulumi || m.Lang == "python" {
		return tf // Python's outputs are snake_case, as a module's
	}
	name := camel(tf)
	if m.Lang == "go" {
		name = strings.ToUpper(name[:1]) + name[1:]
		name = strings.Replace(name, "Cloudrun", "CloudRun", 1)
	}
	return name
}

// option is where a docuconf_options setting goes: a component has none,
// so docuconf lint pulumi takes it as a flag.
func (m *Module) option(name string) string {
	if !m.Pulumi {
		return "docuconf_options." + name
	}
	return "docuconf lint pulumi -" + strings.ReplaceAll(name, "_", "-")
}

// term is a Kubernetes field as the platform writes it.
func (m *Module) term(tf string) string {
	if !m.Pulumi {
		return tf
	}
	return camel(tf)
}

func (m *Module) applyChecks() string {
	if m.Pulumi {
		return "the component's checks"
	}
	return "the module's preconditions"
}

// when is when an unknown value is checked: at apply, or during the
// Pulumi update.
func (m *Module) when() string {
	if m.Pulumi {
		return "during the update"
	}
	return "at apply"
}

func (m *Module) sensitive() string {
	if m.Pulumi {
		return "a Pulumi secret"
	}
	return "sensitive in the plan"
}

func bindHint(p *Plan) string {
	if p.Pulumi {
		return "give each workload's containers the env of one component"
	}
	return "reference the module outside a dynamic block, for example in the pod template's annotations"
}

func (p *Plan) noun() string {
	if p.Pulumi {
		return "preview"
	}
	return "plan"
}
