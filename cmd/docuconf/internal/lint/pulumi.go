package lint

// Reading a Pulumi preview (pulumi preview --json): the docuconf
// components in it, with the arguments each records as its inputs, and
// the workloads, with the inputs Pulumi plans for them (spec/IAC.md 6.3).
// Both become the model the Terraform rules check, so the rules, and
// vet's engine, apply to Pulumi as they do to Terraform.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// Pulumi writes these in a preview's inputs for a value that is unknown
// until the update runs, and for one that is secret.
const (
	pulumiUnknown = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"
	pulumiSecret  = "[secret]"
)

// PulumiOptions are what a Terraform module's docuconf_options say, which
// a component has no argument for: given to docuconf lint pulumi instead.
type PulumiOptions struct {
	UnmanagedEnv []string
	AllowEnvFrom bool
}

type previewJSON struct {
	Steps *[]struct {
		Op       string `json:"op"`
		URN      string `json:"urn"`
		NewState *struct {
			Type   string         `json:"type"`
			Custom bool           `json:"custom"`
			Inputs map[string]any `json:"inputs"`
		} `json:"newState"`
	} `json:"steps"`
}

var componentType = regexp.MustCompile(`^docuconf:(.+):Config$`)

// ReadPreview reads a preview's JSON. dir is the program's directory,
// where the generated components (their docuconf.json) are found.
func ReadPreview(previewFile, dir string, opt PulumiOptions) (*Plan, error) {
	b, err := os.ReadFile(previewFile)
	if err != nil {
		return nil, err
	}
	var pj previewJSON
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&pj); err != nil || pj.Steps == nil {
		return nil, fmt.Errorf("%s: not a preview's JSON (pulumi preview --json)", previewFile)
	}
	comps, err := findComponents(dir)
	if err != nil {
		return nil, err
	}
	p := &Plan{Pulumi: true}
	for _, s := range *pj.Steps {
		ns := s.NewState
		if ns == nil || s.Op == "delete" || s.Op == "delete-replaced" || s.Op == "discard" || s.Op == "read" {
			continue
		}
		name := s.URN[strings.LastIndex(s.URN, "::")+2:]
		if m := componentType.FindStringSubmatch(ns.Type); m != nil && !ns.Custom {
			mod, err := previewModule(name, m[1], ns.Inputs, comps, opt)
			if err != nil {
				return nil, err
			}
			p.Modules = append(p.Modules, mod)
			continue
		}
		if !ns.Custom {
			continue
		}
		var w *Workload
		switch {
		case pulumiPodTemplate[ns.Type] != nil:
			w = pulumiWorkload(ns.Type, ns.Inputs)
		case ns.Type == "aws:ecs/taskDefinition:TaskDefinition":
			s := ns.Inputs["containerDefinitions"]
			w = readTaskDefinition(map[string]any{"container_definitions": s}, map[string]any{"container_definitions": hidden(s)})
		case ns.Type == "gcp:cloudrunv2/service:Service" || ns.Type == "gcp:cloudrunv2/job:Job":
			w = pulumiCloudRun(ns.Type, ns.Inputs)
		case ns.Type == "azure-native:app:ContainerApp":
			w = pulumiContainerApp(ns.Inputs)
		default:
			continue
		}
		w.Addr = fmt.Sprintf("%s %q", ns.Type, name)
		p.Workloads = append(p.Workloads, w)
	}
	return p, nil
}

// component is a generated component found under the program's directory.
type component struct {
	dir  string
	meta Meta
}

// findComponents reads every docuconf.json of a Pulumi component under
// dir, by contract digest.
func findComponents(dir string) (map[string][]component, error) {
	out := map[string][]component{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && p != dir && (e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".")) {
			return filepath.SkipDir
		}
		if e.IsDir() || e.Name() != "docuconf.json" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var meta Meta
		if json.Unmarshal(b, &meta) != nil || meta.Kind != "PulumiComponent" {
			return nil
		}
		// The generator works from the contract's compact JSON.
		var compact bytes.Buffer
		if json.Compact(&compact, meta.Contract) == nil {
			meta.Contract = compact.Bytes()
		}
		out[meta.ContractDigest] = append(out[meta.ContractDigest], component{filepath.Dir(p), meta})
		return nil
	})
	return out, err
}

// previewModule is a component in the preview, as a Module: its
// arguments in a Terraform module's shape, so the rules read them alike.
func previewModule(name, contractName string, inputs map[string]any, comps map[string][]component, opt PulumiOptions) (*Module, error) {
	digest, _ := at(inputs, "contract", "digest").(string)
	var found []component
	if digest != "" {
		found = comps[digest]
	} else {
		// A component generated before it recorded its arguments: by name.
		for _, cs := range comps {
			for _, c := range cs {
				if mc, err := terraform.Parse(c.meta.Contract); err == nil && mc.Name == contractName {
					found = append(found, c)
				}
			}
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s: no component generated from its %s contract (digest %s) is under this directory; run docuconf lint pulumi in the program's directory, or give -dir", name, contractName, orNone(digest))
	}
	sort.Slice(found, func(i, j int) bool { return found[i].dir < found[j].dir })
	comp := found[0]
	c, err := terraform.Parse(comp.meta.Contract)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	m := &Module{Addr: name, Name: name, Dir: comp.dir, Meta: comp.meta, Contract: c, Args: map[string]Expr{}, Unknown: map[string]bool{}, Pulumi: true, Raw: map[string]any{}}
	switch comp.meta.Language {
	case "go", "python":
		m.Lang = comp.meta.Language
	}
	args, _ := inputs["arguments"].(map[string]any)
	if args == nil && inputs["arguments"] != nil {
		// The arguments are one unknown or secret value: an Output.
		for _, v := range c.Vars {
			m.Unknown[v.Name] = true
		}
	}
	isFile := map[string]bool{}
	for _, f := range c.Files {
		isFile[f.Name] = true
	}
	options := map[string]any{}
	for k, a := range args {
		switch k {
		case "podMetadata":
			if hidden(a) {
				continue
			}
			if ann := at(a, "annotations"); ann != nil {
				options["pod_annotations"] = ann
			}
			if lab := at(a, "labels"); lab != nil {
				options["pod_labels"] = lab
			}
		case "overlayValues":
			ov, _ := a.(map[string]any)
			for o, vals := range ov {
				m.Args["overlay_"+o] = expr(vals, func(x any) any { return x })
			}
		default:
			m.Raw[k] = a
			m.Args[k] = expr(a, func(x any) any { return tfArg(x, isFile[k]) })
			if !m.Args[k].IsConstant {
				m.Unknown[k] = true
			}
		}
	}
	if len(opt.UnmanagedEnv) > 0 {
		l := make([]any, len(opt.UnmanagedEnv))
		for i, n := range opt.UnmanagedEnv {
			l[i] = n
		}
		options["unmanaged_env"] = l
	}
	if opt.AllowEnvFrom {
		options["allow_env_from"] = true
	}
	if len(options) > 0 {
		m.Args["docuconf_options"] = Expr{Constant: options, IsConstant: true}
	}
	return m, nil
}

func orNone(s string) string {
	if s == "" {
		return "not recorded"
	}
	return s
}

// expr is an argument as an Expr: a constant, unless something in it is
// unknown or secret at preview.
func expr(a any, conv func(any) any) Expr {
	if hidden(a) {
		origin := "an Output known only during the update"
		if containsSecret(a) {
			origin = "a Pulumi secret"
		}
		return Expr{References: []string{origin}}
	}
	return Expr{Constant: conv(a), IsConstant: true}
}

// hidden reports whether a preview value holds something unknown or
// secret.
func hidden(v any) bool {
	switch x := v.(type) {
	case string:
		return x == pulumiUnknown || x == pulumiSecret
	case map[string]any:
		for _, y := range x {
			if hidden(y) {
				return true
			}
		}
	case []any:
		for _, y := range x {
			if hidden(y) {
				return true
			}
		}
	}
	return false
}

func containsSecret(v any) bool {
	switch x := v.(type) {
	case string:
		return x == pulumiSecret
	case map[string]any:
		for _, y := range x {
			if containsSecret(y) {
				return true
			}
		}
	case []any:
		for _, y := range x {
			if containsSecret(y) {
				return true
			}
		}
	}
	return false
}

// The component's source names, as a Terraform module's attributes.
var tfSource = map[string]string{
	"secretKeyRef": "secret", "configMapKeyRef": "config_map", "fieldRef": "field",
	"resourceFieldRef": "resource_field", "configMap": "config_map",
}

// tfArg is a component argument ({"secretKeyRef": {...}}) as the module
// argument the rules read ({"secret": {...}}): the source renamed, and
// the keys in snake_case, except inside content the platform wrote.
func tfArg(a any, file bool) any {
	m, ok := a.(map[string]any)
	if !ok {
		return a
	}
	out := map[string]any{}
	for k, v := range m {
		name := k
		if t, ok := tfSource[k]; ok && (k != "configMap" || file) {
			name = t
		}
		if k == "value" || k == "inline" {
			out[name] = v
			continue
		}
		out[name] = snakeKeys(v)
	}
	return out
}

func snakeKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if k == "podAnnotations" || k == "podLabels" {
				out[snake(k)] = y
				continue
			}
			out[snake(k)] = snakeKeys(y)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = snakeKeys(y)
		}
		return out
	}
	return v
}

func snake(k string) string {
	var b strings.Builder
	for i, r := range k {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pulumiPodTemplate is where each Kubernetes workload type keeps its pod:
// nil for a pod, which is its own template.
var pulumiPodTemplate = map[string][]any{
	"kubernetes:apps/v1:Deployment":            {"spec", "template"},
	"kubernetes:apps/v1:StatefulSet":           {"spec", "template"},
	"kubernetes:apps/v1:DaemonSet":             {"spec", "template"},
	"kubernetes:apps/v1:ReplicaSet":            {"spec", "template"},
	"kubernetes:batch/v1:Job":                  {"spec", "template"},
	"kubernetes:batch/v1:CronJob":              {"spec", "jobTemplate", "spec", "template"},
	"kubernetes:core/v1:ReplicationController": {"spec", "template"},
	"kubernetes:core/v1:Pod":                   {},
}

// pulumiWorkload reads a Kubernetes workload's planned inputs.
func pulumiWorkload(typ string, in map[string]any) *Workload {
	w := &Workload{Type: typ, Target: "kubernetes"}
	tp := pulumiPodTemplate[typ]
	meta := func(v any) Metadata {
		md := Metadata{}
		if hidden(at(v, "annotations")) || hidden(at(v, "labels")) || hidden(v) {
			md.Unknown = true
			return md
		}
		md.Annotations, _ = at(v, "annotations").(map[string]any)
		md.Labels, _ = at(v, "labels").(map[string]any)
		return md
	}
	pod := at(in, tp...)
	if len(tp) > 0 {
		w.Meta = meta(at(in, "metadata"))
	}
	w.PodMeta = meta(at(pod, "metadata"))
	spec := at(pod, "spec")
	if _, whole := at(spec, "containers").(string); whole {
		// A secret anywhere in the list makes the whole list "[secret]".
		w.Unknown = true
		return w
	}
	containers, _ := at(spec, "containers").([]any)
	for _, c := range containers {
		ct := Container{}
		ct.Name, _ = at(c, "name").(string)
		ct.Image, _ = at(c, "image").(string)
		envs, _ := at(c, "env").([]any)
		for _, e := range envs {
			env := Env{}
			env.Name, _ = at(e, "name").(string)
			val := at(e, "value")
			if s, ok := val.(string); ok && !hidden(s) {
				env.Value = &s
			}
			if vf, ok := at(e, "valueFrom").(map[string]any); ok {
				env.ValueFrom = tfValueFrom(vf)
				env.Unknown = hidden(vf)
			}
			env.Unknown = env.Unknown || val == pulumiUnknown
			env.Sensitive = containsSecret(val)
			ct.Env = append(ct.Env, env)
		}
		ct.EnvFrom, _ = at(c, "envFrom").([]any)
		mounts, _ := at(c, "volumeMounts").([]any)
		for _, mo := range mounts {
			mt := Mount{}
			mt.Name, _ = at(mo, "name").(string)
			mt.MountPath, _ = at(mo, "mountPath").(string)
			mt.SubPath, _ = at(mo, "subPath").(string)
			mt.ReadOnly, _ = at(mo, "readOnly").(bool)
			ct.Mounts = append(ct.Mounts, mt)
		}
		w.Containers = append(w.Containers, ct)
	}
	vols, _ := at(spec, "volumes").([]any)
	for _, v := range vols {
		if m, ok := v.(map[string]any); ok {
			w.Volumes = append(w.Volumes, m)
		}
	}
	return w
}

// tfValueFrom is a planned valueFrom as the plan's value_from block reads:
// each reference a list of one, in snake_case.
func tfValueFrom(vf map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range vf {
		out[snake(k)] = []any{snakeKeys(v)}
	}
	return out
}

// pulumiContainerApp reads an azure-native container app: its template's
// containers and volumes, with each env entry's secretRef resolved to the
// app's configuration.secrets, keyed as the Terraform module's secrets are.
func pulumiContainerApp(in map[string]any) *Workload {
	w := &Workload{Type: "azure-native:app:ContainerApp", Target: "containerapps", AppSecrets: map[string]map[string]any{}}
	unknownSecrets := map[string]bool{}
	secretsList := at(in, "configuration", "secrets")
	_, secretsHidden := secretsList.(string)
	list, _ := secretsList.([]any)
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		sec := map[string]any{"name": n}
		if id, ok := m["keyVaultUrl"].(string); ok && !hidden(id) {
			sec["key_vault_secret_id"] = id
		}
		if id, ok := m["identity"].(string); ok && !hidden(id) {
			sec["identity"] = id
		}
		if hidden(m["keyVaultUrl"]) || hidden(m["identity"]) {
			unknownSecrets[n] = true
		}
		w.AppSecrets[n] = sec
	}
	tmpl := at(in, "template")
	if _, whole := at(tmpl, "containers").(string); whole {
		w.Unknown = true
		return w
	}
	containers, _ := at(tmpl, "containers").([]any)
	for _, c := range containers {
		ct := Container{}
		ct.Name, _ = at(c, "name").(string)
		ct.Image, _ = at(c, "image").(string)
		envs, _ := at(c, "env").([]any)
		for _, e := range envs {
			env := Env{}
			env.Name, _ = at(e, "name").(string)
			val := at(e, "value")
			if ref, ok := at(e, "secretRef").(string); ok && ref != "" {
				sec, found := w.AppSecrets[ref]
				if !found {
					sec = map[string]any{"name": ref}
				}
				env.ValueFrom = map[string]any{"azure": sec}
				// A Key Vault reference the preview does not know, or a
				// secret list it hides, is checked during the update.
				env.Unknown = secretsHidden || unknownSecrets[ref]
			} else {
				if s, ok := val.(string); ok && !hidden(s) {
					env.Value = &s
				}
				env.Unknown = val == pulumiUnknown
				env.Sensitive = containsSecret(val)
			}
			ct.Env = append(ct.Env, env)
		}
		mounts, _ := at(c, "volumeMounts").([]any)
		for _, mo := range mounts {
			mt := Mount{ReadOnly: true} // Container Apps mounts Secret volumes read-only
			mt.Name, _ = at(mo, "volumeName").(string)
			mt.MountPath, _ = at(mo, "mountPath").(string)
			mt.SubPath, _ = at(mo, "subPath").(string)
			ct.Mounts = append(ct.Mounts, mt)
		}
		w.Containers = append(w.Containers, ct)
	}
	vols, _ := at(tmpl, "volumes").([]any)
	for _, v := range vols {
		if m, ok := v.(map[string]any); ok {
			w.Volumes = append(w.Volumes, m)
		}
	}
	return w
}

// pulumiCloudRun reads a Cloud Run service's or job's revision template.
func pulumiCloudRun(typ string, in map[string]any) *Workload {
	w := &Workload{Type: typ, Target: "cloudrun"}
	tmpl := at(in, "template")
	if typ == "gcp:cloudrunv2/job:Job" {
		tmpl = at(tmpl, "template")
	}
	if _, whole := at(tmpl, "containers").(string); whole {
		w.Unknown = true
		return w
	}
	containers, _ := at(tmpl, "containers").([]any)
	for _, c := range containers {
		ct := Container{}
		ct.Name, _ = at(c, "name").(string)
		ct.Image, _ = at(c, "image").(string)
		envs, _ := at(c, "envs").([]any)
		for _, e := range envs {
			env := Env{}
			env.Name, _ = at(e, "name").(string)
			val := at(e, "value")
			ref, isRef := at(e, "valueSource", "secretKeyRef").(map[string]any)
			if s, ok := val.(string); ok && !hidden(s) && !isRef {
				env.Value = &s
			}
			if isRef {
				env.ValueFrom = map[string]any{"gcp": ref}
			}
			env.Unknown = val == pulumiUnknown || hidden(ref)
			env.Sensitive = containsSecret(val)
			ct.Env = append(ct.Env, env)
		}
		mounts, _ := at(c, "volumeMounts").([]any)
		for _, mo := range mounts {
			mt := Mount{ReadOnly: true} // Cloud Run mounts secret volumes read-only
			mt.Name, _ = at(mo, "name").(string)
			mt.MountPath, _ = at(mo, "mountPath").(string)
			ct.Mounts = append(ct.Mounts, mt)
		}
		w.Containers = append(w.Containers, ct)
	}
	vols, _ := at(tmpl, "volumes").([]any)
	for _, v := range vols {
		if m, ok := v.(map[string]any); ok {
			w.Volumes = append(w.Volumes, m)
		}
	}
	return w
}
