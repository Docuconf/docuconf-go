// Package lint reads a Terraform or OpenTofu plan (terraform show -json)
// and finds the docuconf modules in it, the workloads whose containers take
// their outputs, and what those containers actually receive, as
// spec/IAC.md section 7 describes. It reports what it can see in the plan;
// the docuconf CLI checks the values it reconstructs with vet's engine.
package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/terraform"
)

// Expr is a module argument's expression in the plan's configuration: a
// constant, or the references the expression makes.
type Expr struct {
	Constant   any
	IsConstant bool
	References []string
}

// Module is one docuconf module call.
type Module struct {
	Addr     string // module.orders_config, or module.app.module.orders_config
	Name     string // orders_config
	Scope    string // the address of the module that calls it: "" for the root
	Dir      string
	Meta     Meta
	Contract *terraform.Contract
	Args     map[string]Expr
	// Unknown are the arguments whose validations Terraform could not
	// evaluate at plan: their values are known only during apply.
	Unknown map[string]bool
}

// Target is the module's target: kubernetes unless docuconf.json says.
func (m *Module) Target() string {
	if len(m.Meta.Targets) == 1 {
		return m.Meta.Targets[0]
	}
	return "kubernetes"
}

// Meta is a module's docuconf.json.
type Meta struct {
	Kind           string          `json:"kind"`
	ContractDigest string          `json:"contractDigest"`
	Contract       json.RawMessage `json:"contract"`
	Generator      struct {
		Version string `json:"version"`
	} `json:"generator"`
	Targets []string `json:"targets"`
}

// Workload is a resource that runs the app: a Kubernetes resource that
// runs pods, or an ECS task definition.
type Workload struct {
	Addr   string // the full address, module path included
	Scope  string // the module the resource is declared in
	Type   string
	Target string // kubernetes, ecs or cloudrun
	// Unknown is set when the plan does not know what the containers get,
	// such as container_definitions built from an ARN created in the same
	// apply.
	Unknown    bool
	Config     map[string]any // the resource's expressions
	Meta       Metadata       // the workload's own metadata (none for a pod)
	PodMeta    Metadata       // the pod template's metadata
	Containers []Container
	Volumes    []map[string]any
}

// Metadata is planned annotations and labels; nil maps are unknown.
type Metadata struct {
	Annotations, Labels map[string]any
	Unknown             bool
}

// Container is one container of a workload, as planned.
type Container struct {
	Name    string
	Image   string
	Env     []Env
	EnvFrom []any
	Mounts  []Mount
}

// Env is one planned env entry.
type Env struct {
	Name      string
	Value     *string
	ValueFrom map[string]any // the first value_from block: secret_key_ref, …; for ECS, {"ecs": valueFrom}; for Cloud Run, {"gcp": {secret, version}}
	Unknown   bool           // the value (or a reference field) is unknown at plan
	Sensitive bool
}

// Mount is one planned volume mount.
type Mount struct {
	Name, MountPath, SubPath string
	ReadOnly                 bool
}

// Plan is what the linter reads from a plan.
type Plan struct {
	Modules   []*Module
	Workloads []*Workload
}

type planJSON struct {
	FormatVersion   string `json:"format_version"`
	ResourceChanges []struct {
		Address       string `json:"address"`
		ModuleAddress string `json:"module_address"`
		Type          string `json:"type"`
		Mode          string `json:"mode"`
		Change        struct {
			Actions        []string `json:"actions"`
			After          any      `json:"after"`
			AfterUnknown   any      `json:"after_unknown"`
			AfterSensitive any      `json:"after_sensitive"`
		} `json:"change"`
	} `json:"resource_changes"`
	Configuration struct {
		RootModule configModule `json:"root_module"`
	} `json:"configuration"`
	Checks []struct {
		Address struct {
			Kind   string `json:"kind"`
			Module string `json:"module"`
			Name   string `json:"name"`
		} `json:"address"`
		Status string `json:"status"`
	} `json:"checks"`
}

type configModule struct {
	Resources []struct {
		Address     string         `json:"address"`
		Type        string         `json:"type"`
		Mode        string         `json:"mode"`
		Expressions map[string]any `json:"expressions"`
	} `json:"resources"`
	ModuleCalls map[string]struct {
		Source      string                     `json:"source"`
		Expressions map[string]json.RawMessage `json:"expressions"`
		Module      configModule               `json:"module"`
	} `json:"module_calls"`
}

// modulesJSON is .terraform/modules/modules.json, where terraform init
// records the directory of every module call, local or downloaded.
type modulesJSON struct {
	Modules []struct {
		Key, Source, Dir string
	}
}

// Read reads a plan's JSON. root is the Terraform root directory, where
// .terraform/modules/modules.json and local module sources are found.
func Read(planFile, root string) (*Plan, error) {
	b, err := os.ReadFile(planFile)
	if err != nil {
		return nil, err
	}
	var pj planJSON
	if err := json.Unmarshal(b, &pj); err != nil {
		return nil, fmt.Errorf("%s: not a plan's JSON (terraform show -json plan.out): %w", planFile, err)
	}
	if pj.FormatVersion == "" {
		return nil, fmt.Errorf("%s: not a plan's JSON (terraform show -json plan.out): no format_version", planFile)
	}
	dirs := map[string]string{}
	if mb, err := os.ReadFile(filepath.Join(root, ".terraform", "modules", "modules.json")); err == nil {
		var mj modulesJSON
		if err := json.Unmarshal(mb, &mj); err == nil {
			for _, m := range mj.Modules {
				dirs[m.Key] = filepath.Join(root, m.Dir)
			}
		}
	}
	p := &Plan{}
	configs := map[string]map[string]any{} // resource address → expressions
	var walk func(m configModule, scope, key, dir string) error
	walk = func(m configModule, scope, key, dir string) error {
		for _, r := range m.Resources {
			if r.Mode == "managed" {
				configs[join(scope, r.Address)] = r.Expressions
			}
		}
		names := make([]string, 0, len(m.ModuleCalls))
		for n := range m.ModuleCalls {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			call := m.ModuleCalls[n]
			k := n
			if key != "" {
				k = key + "." + n
			}
			d, ok := dirs[k]
			if !ok && (strings.HasPrefix(call.Source, "./") || strings.HasPrefix(call.Source, "../")) {
				d = filepath.Join(dir, call.Source)
			}
			addr := join(scope, "module."+n)
			if d != "" {
				mod, err := readModule(d, addr, n, scope, call.Expressions)
				if err != nil {
					return err
				}
				if mod != nil {
					p.Modules = append(p.Modules, mod)
				}
			}
			if err := walk(call.Module, addr, k, d); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(pj.Configuration.RootModule, "", "", root); err != nil {
		return nil, err
	}
	for _, c := range pj.Checks {
		if c.Address.Kind != "var" || c.Status != "unknown" {
			continue
		}
		for _, m := range p.Modules {
			if m.Addr == stripKeys(c.Address.Module) {
				m.Unknown[c.Address.Name] = true
			}
		}
	}
	for _, rc := range pj.ResourceChanges {
		if rc.Mode != "managed" || isDelete(rc.Change.Actions) {
			continue
		}
		var w *Workload
		switch {
		case podTemplate[rc.Type]:
			w = readWorkload(rc.Type, rc.Change.After, rc.Change.AfterUnknown, rc.Change.AfterSensitive)
		case rc.Type == "aws_ecs_task_definition":
			w = readTaskDefinition(rc.Change.After, rc.Change.AfterUnknown)
		case rc.Type == "google_cloud_run_v2_service" || rc.Type == "google_cloud_run_v2_job":
			w = readCloudRun(rc.Type, rc.Change.After, rc.Change.AfterUnknown, rc.Change.AfterSensitive)
		default:
			continue
		}
		scope := stripKeys(rc.ModuleAddress)
		local := strings.TrimPrefix(stripKeys(rc.Address), scope+".")
		w.Addr, w.Scope = rc.Address, scope
		w.Config = configs[join(scope, local)]
		p.Workloads = append(p.Workloads, w)
	}
	return p, nil
}

func isDelete(actions []string) bool {
	return len(actions) == 1 && actions[0] == "delete"
}

func join(scope, addr string) string {
	if scope == "" {
		return addr
	}
	return scope + "." + addr
}

// stripKeys drops instance keys from an address: module.a[0].module.b["x"]
// is module.a.module.b. Every instance of a call has the same arguments'
// expressions, so the linter checks a call once per workload.
func stripKeys(a string) string {
	var b strings.Builder
	depth := 0
	inStr := false
	for i := 0; i < len(a); i++ {
		c := a[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"' && depth > 0:
			inStr = true
		case c == '[':
			depth++
		case c == ']':
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func readModule(dir, addr, name, scope string, exprs map[string]json.RawMessage) (*Module, error) {
	b, err := os.ReadFile(filepath.Join(dir, "docuconf.json"))
	if os.IsNotExist(err) {
		return nil, nil // not a docuconf module
	}
	if err != nil {
		return nil, err
	}
	var meta Meta
	if err := json.Unmarshal(b, &meta); err != nil || meta.Kind != "TerraformModule" {
		return nil, fmt.Errorf("%s: %s/docuconf.json is not a docuconf module's", addr, dir)
	}
	c, err := terraform.Parse(meta.Contract)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", addr, err)
	}
	m := &Module{Addr: addr, Name: name, Scope: scope, Dir: dir, Meta: meta, Contract: c, Args: map[string]Expr{}, Unknown: map[string]bool{}}
	for k, raw := range exprs {
		var e struct {
			ConstantValue json.RawMessage `json:"constant_value"`
			References    []string        `json:"references"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		x := Expr{References: e.References}
		if e.ConstantValue != nil {
			x.IsConstant = true
			d := json.NewDecoder(bytes.NewReader(e.ConstantValue))
			d.UseNumber()
			if err := d.Decode(&x.Constant); err != nil {
				return nil, err
			}
		}
		m.Args[k] = x
	}
	return m, nil
}

// podTemplate lists the resource types whose pods the linter reads.
var podTemplate = map[string]bool{
	"kubernetes_deployment": true, "kubernetes_deployment_v1": true,
	"kubernetes_stateful_set": true, "kubernetes_stateful_set_v1": true,
	"kubernetes_daemonset": true, "kubernetes_daemon_set_v1": true,
	"kubernetes_replication_controller": true, "kubernetes_replication_controller_v1": true,
	"kubernetes_job": true, "kubernetes_job_v1": true,
	"kubernetes_cron_job": true, "kubernetes_cron_job_v1": true,
	"kubernetes_pod": true, "kubernetes_pod_v1": true,
}

// at walks a planned value by keys and indexes; nil when absent.
func at(v any, p ...any) any {
	for _, k := range p {
		switch k := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

// templatePath is where a workload type keeps its pod: the pod's metadata
// and spec. A pod is its own template.
func templatePath(typ string) []any {
	switch {
	case strings.HasPrefix(typ, "kubernetes_pod"):
		return nil
	case strings.HasPrefix(typ, "kubernetes_cron_job"):
		return []any{"spec", 0, "job_template", 0, "spec", 0, "template", 0}
	}
	return []any{"spec", 0, "template", 0}
}

// readTaskDefinition reads an ECS task definition's container_definitions,
// a JSON string, which is unknown at plan when it is built from values
// only known during apply.
func readTaskDefinition(after, unknown any) *Workload {
	w := &Workload{Type: "aws_ecs_task_definition", Target: "ecs"}
	s, ok := at(after, "container_definitions").(string)
	if !ok || at(unknown, "container_definitions") == true {
		w.Unknown = true
		return w
	}
	var defs []struct {
		Name        string `json:"name"`
		Image       string `json:"image"`
		Environment []struct {
			Name  string  `json:"name"`
			Value *string `json:"value"`
		} `json:"environment"`
		Secrets []struct {
			Name      string `json:"name"`
			ValueFrom string `json:"valueFrom"`
		} `json:"secrets"`
		EnvironmentFiles []any `json:"environmentFiles"`
	}
	if json.Unmarshal([]byte(s), &defs) != nil {
		w.Unknown = true
		return w
	}
	for _, d := range defs {
		ct := Container{Name: d.Name, Image: d.Image, EnvFrom: d.EnvironmentFiles}
		for _, e := range d.Environment {
			ct.Env = append(ct.Env, Env{Name: e.Name, Value: e.Value})
		}
		for _, e := range d.Secrets {
			ct.Env = append(ct.Env, Env{Name: e.Name, ValueFrom: map[string]any{"ecs": e.ValueFrom}})
		}
		w.Containers = append(w.Containers, ct)
	}
	return w
}

// readCloudRun reads a Cloud Run service's or job's revision template:
// its containers' env and volume mounts, and its volumes.
func readCloudRun(typ string, after, unknown, sensitive any) *Workload {
	w := &Workload{Type: typ, Target: "cloudrun"}
	tp := []any{"template", 0}
	if typ == "google_cloud_run_v2_job" {
		tp = []any{"template", 0, "template", 0}
	}
	sub := func(v any, extra ...any) any { return at(v, append(append([]any{}, tp...), extra...)...) }
	containers, _ := sub(after, "containers").([]any)
	for i, c := range containers {
		cu := sub(unknown, "containers", i)
		cs := sub(sensitive, "containers", i)
		ct := Container{}
		ct.Name, _ = at(c, "name").(string)
		ct.Image, _ = at(c, "image").(string)
		envs, _ := at(c, "env").([]any)
		for j, e := range envs {
			env := Env{}
			env.Name, _ = at(e, "name").(string)
			if s, ok := at(e, "value").(string); ok && at(e, "value_source", 0) == nil {
				env.Value = &s
			}
			if r, ok := at(e, "value_source", 0, "secret_key_ref", 0).(map[string]any); ok {
				env.ValueFrom = map[string]any{"gcp": r}
			}
			eu := at(cu, "env", j)
			env.Unknown = eu == true || at(eu, "value") == true || anyTrue(at(eu, "value_source"))
			es := at(cs, "env", j)
			env.Sensitive = cs == true || at(cs, "env") == true || es == true || at(es, "value") == true
			ct.Env = append(ct.Env, env)
		}
		mounts, _ := at(c, "volume_mounts").([]any)
		for _, mo := range mounts {
			m := Mount{ReadOnly: true} // Cloud Run mounts secret volumes read-only
			m.Name, _ = at(mo, "name").(string)
			m.MountPath, _ = at(mo, "mount_path").(string)
			ct.Mounts = append(ct.Mounts, m)
		}
		w.Containers = append(w.Containers, ct)
	}
	vols, _ := sub(after, "volumes").([]any)
	for _, v := range vols {
		if m, ok := v.(map[string]any); ok {
			w.Volumes = append(w.Volumes, m)
		}
	}
	return w
}

func readWorkload(typ string, after, unknown, sensitive any) *Workload {
	w := &Workload{Type: typ, Target: "kubernetes"}
	tp := templatePath(typ)
	sub := func(v any, extra ...any) any { return at(v, append(append([]any{}, tp...), extra...)...) }
	meta := func(v, u any) Metadata {
		m := Metadata{}
		m.Annotations, _ = at(v, "annotations").(map[string]any)
		m.Labels, _ = at(v, "labels").(map[string]any)
		if at(u, "annotations") == true || at(u, "labels") == true {
			m.Unknown = true
		}
		return m
	}
	if tp != nil {
		w.Meta = meta(at(after, "metadata", 0), at(unknown, "metadata", 0))
	}
	w.PodMeta = meta(sub(after, "metadata", 0), sub(unknown, "metadata", 0))
	containers, _ := sub(after, "spec", 0, "container").([]any)
	for i, c := range containers {
		cu := sub(unknown, "spec", 0, "container", i)
		cs := sub(sensitive, "spec", 0, "container", i)
		ct := Container{}
		ct.Name, _ = at(c, "name").(string)
		ct.Image, _ = at(c, "image").(string)
		envs, _ := at(c, "env").([]any)
		for j, e := range envs {
			env := Env{}
			env.Name, _ = at(e, "name").(string)
			if s, ok := at(e, "value").(string); ok {
				env.Value = &s
			}
			if vf, ok := at(e, "value_from", 0).(map[string]any); ok {
				env.ValueFrom = vf
			}
			eu := at(cu, "env", j)
			env.Unknown = eu == true || at(eu, "value") == true || anyTrue(at(eu, "value_from"))
			es := at(cs, "env", j)
			env.Sensitive = cs == true || at(cs, "env") == true || es == true || at(es, "value") == true
			ct.Env = append(ct.Env, env)
		}
		ct.EnvFrom, _ = at(c, "env_from").([]any)
		mounts, _ := at(c, "volume_mount").([]any)
		for _, mo := range mounts {
			m := Mount{}
			m.Name, _ = at(mo, "name").(string)
			m.MountPath, _ = at(mo, "mount_path").(string)
			m.SubPath, _ = at(mo, "sub_path").(string)
			m.ReadOnly, _ = at(mo, "read_only").(bool)
			ct.Mounts = append(ct.Mounts, m)
		}
		w.Containers = append(w.Containers, ct)
	}
	vols, _ := sub(after, "spec", 0, "volume").([]any)
	for _, v := range vols {
		if m, ok := v.(map[string]any); ok {
			w.Volumes = append(w.Volumes, m)
		}
	}
	return w
}

// anyTrue reports whether an after_unknown value marks anything unknown.
func anyTrue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case []any:
		for _, y := range x {
			if anyTrue(y) {
				return true
			}
		}
	case map[string]any:
		for _, y := range x {
			if anyTrue(y) {
				return true
			}
		}
	}
	return false
}

// refersTo reports whether expressions reference the module (relative to
// the scope they are declared in) and returns the attributes they read.
func refersTo(exprs any, module string) (bool, []string) {
	var found []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if refs, ok := x["references"].([]any); ok {
				for _, r := range refs {
					s, _ := r.(string)
					if s == module || strings.HasPrefix(s, module+".") || strings.HasPrefix(s, module+"[") {
						found = append(found, s)
					}
				}
			}
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(exprs)
	return len(found) > 0, found
}

// mountDir is where a file input's volume is mounted (SPEC.md 4.6).
func mountDir(f terraform.File) string {
	p, _ := f.Raw["path"].(string)
	if f.Type == "tls" {
		return p
	}
	return path.Dir(p)
}
