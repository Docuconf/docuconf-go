package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The module's outputs must equal what docuconf render writes for the same
// values, and its validations must reject what docuconf vet rejects. These
// tests apply generated modules, so they need terraform (or tofu) on PATH;
// DOCUCONF_TERRAFORM names another binary. Without one they are skipped.

const kitchen = "testdata/terraform/contract.cue"

func terraformBinary(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("DOCUCONF_TERRAFORM"); b != "" {
		return b
	}
	for _, b := range []string{"terraform", "tofu"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	t.Skip("terraform or tofu is not installed")
	return ""
}

// secrets are set in every case: the module requires them.
var secrets = map[string]any{
	"API_TOKEN":    map[string]any{"secretKeyRef": map[string]any{"name": "kitchen", "key": "token"}},
	"DATABASE_URL": map[string]any{"secretKeyRef": map[string]any{"name": "kitchen-db", "key": "url"}},
}

func withSecrets(v map[string]any) map[string]any {
	out := map[string]any{}
	for k, x := range secrets {
		out[k] = x
	}
	for k, x := range v {
		out[k] = x
	}
	return out
}

var renderCases = []struct {
	name   string
	values map[string]any
}{
	{"literals", map[string]any{
		"DEBUG":     true,
		"GREETING":  "hi $(X) <&>",
		"LOG_LEVEL": "warn",
		"WORKERS":   8,
		"RATIO":     0.25,
		"UPSTREAM":  "https://a.example/x?y=1&z",
		"TIMEOUT":   "1m30s",
		"REGION":    "eu-west-1",
		"PROFILE":   "prod",
	}},
	{"durations", map[string]any{
		"BACKOFF":   "1500ms",
		"CACHE_TTL": "1h30m",
		"IDLE":      "26h3m4s5ms",
	}},
	{"whole_durations", map[string]any{
		"BACKOFF":   "2s",
		"CACHE_TTL": "250ms",
		"IDLE":      "59s",
	}},
	{"lists_and_json", map[string]any{
		"HOSTS":    []any{"a.internal", "b.internal"},
		"PORTS":    []any{80, 443},
		"TAGS":     []any{"x", "y<z", `back\slash`, `\u003c`, "line\u2028sep"},
		"FEATURES": map[string]any{"a": true, "b": []any{1, 2}, "c": "<&> $"},
	}},
	{"references", map[string]any{
		"NODE_NAME":    map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.nodeName"}},
		"MEMORY_LIMIT": map[string]any{"resourceFieldRef": map[string]any{"resource": "limits.memory", "divisor": "1Mi"}},
		"LOG_LEVEL":    map[string]any{"configMapKeyRef": map[string]any{"name": "kitchen", "key": "log-level"}},
		"SIGNING_KEYS": map[string]any{"secretKeyRef": map[string]any{"name": "kitchen-keys", "key": "keys"}},
	}},
	{"injected", map[string]any{
		"podAnnotations": map[string]any{"team": "core"},
		"GREETING": map[string]any{"injected": map[string]any{
			"provider":       "bank-vaults",
			"ref":            "vault:secret/data/k#$greeting",
			"podAnnotations": map[string]any{"vault.security.banzaicloud.io/vault-role": "{input}-reader"},
			"podLabels":      map[string]any{"inject": "true"},
		}},
		"API_TOKEN": map[string]any{"injected": map[string]any{"provider": "op"}},
	}},
	{"code_points", map[string]any{
		// Two code points, one grapheme cluster: minLength 2 is met.
		"GREETING": "\U0001F44D\U0001F3FD",
	}},
}

// toArgs turns a values document into the module's arguments, as a
// .tf.json module block.
func toArgs(values map[string]any) map[string]any {
	args := map[string]any{}
	for k, v := range values {
		switch k {
		case "podAnnotations":
			opt, _ := args["docuconf_options"].(map[string]any)
			if opt == nil {
				opt = map[string]any{}
			}
			opt["pod_annotations"] = v
			args["docuconf_options"] = opt
			continue
		case "podLabels":
			opt, _ := args["docuconf_options"].(map[string]any)
			if opt == nil {
				opt = map[string]any{}
			}
			opt["pod_labels"] = v
			args["docuconf_options"] = opt
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			args[k] = map[string]any{"value": v}
			continue
		}
		switch {
		case m["secretKeyRef"] != nil:
			args[k] = map[string]any{"secret": m["secretKeyRef"]}
		case m["configMapKeyRef"] != nil:
			args[k] = map[string]any{"config_map": m["configMapKeyRef"]}
		case m["fieldRef"] != nil:
			args[k] = map[string]any{"field": map[string]any{"field_path": m["fieldRef"].(map[string]any)["fieldPath"]}}
		case m["resourceFieldRef"] != nil:
			r := m["resourceFieldRef"].(map[string]any)
			args[k] = map[string]any{"resource_field": map[string]any{"resource": r["resource"], "divisor": r["divisor"], "container_name": r["containerName"]}}
		case m["injected"] != nil:
			i := m["injected"].(map[string]any)
			args[k] = map[string]any{"injected": map[string]any{"provider": i["provider"], "ref": i["ref"], "pod_annotations": i["podAnnotations"], "pod_labels": i["podLabels"]}}
		default: // a json input's object
			args[k] = map[string]any{"value": v}
		}
	}
	return escapeTemplates(args).(map[string]any)
}

// escapeTemplates keeps strings in .tf.json from being read as templates.
func escapeTemplates(v any) any {
	switch x := v.(type) {
	case string:
		return strings.NewReplacer("${", "$${", "%{", "%%{").Replace(x)
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if y != nil {
				out[k] = escapeTemplates(y)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = escapeTemplates(y)
		}
		return out
	}
	return v
}

// fromModule turns the module's kubernetes_env into #Render's shape.
func fromModule(entries []any) []any {
	camel := map[string]string{
		"secret_key_ref": "secretKeyRef", "config_map_key_ref": "configMapKeyRef",
		"field_ref": "fieldRef", "resource_field_ref": "resourceFieldRef",
		"field_path": "fieldPath", "container_name": "containerName",
	}
	var conv func(any) any
	conv = func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := map[string]any{}
		for k, y := range m {
			if y == nil {
				continue
			}
			if c, ok := camel[k]; ok {
				k = c
			}
			if k == "value_from" {
				k = "valueFrom"
			}
			out[k] = conv(y)
		}
		return out
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = conv(e)
	}
	return out
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func tf(t *testing.T, bin, dir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return append(out.Bytes(), errb.Bytes()...), err
	}
	return out.Bytes(), nil
}

// newRoot generates the kitchen module and a root module that calls it.
func newRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-out", filepath.Join(dir, "module")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: exit %d: %s", code, stderr.String())
	}
	return dir
}

func TestTerraformMatchesRender(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	modules, outputs := map[string]any{}, map[string]any{}
	for _, tc := range renderCases {
		args := toArgs(withSecrets(tc.values))
		args["source"] = "./module"
		modules[tc.name] = args
		outputs[tc.name] = map[string]any{"value": map[string]any{
			"env":            "${module." + tc.name + ".kubernetes_env}",
			"podAnnotations": "${module." + tc.name + ".pod_annotations}",
			"podLabels":      "${module." + tc.name + ".pod_labels}",
			"bindings":       "${module." + tc.name + ".bindings}",
		}}
	}
	writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": modules, "output": outputs})
	if out, err := tf(t, bin, dir, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if out, err := tf(t, bin, filepath.Join(dir, "module"), "fmt", "-check", "-diff", "-no-color"); err != nil {
		t.Errorf("the generated module is not formatted: %v\n%s", err, out)
	}
	if out, err := tf(t, bin, dir, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	raw, err := tf(t, bin, dir, "output", "-json")
	if err != nil {
		t.Fatalf("output: %v\n%s", err, raw)
	}
	var got map[string]struct {
		Value struct {
			Env            []any                     `json:"env"`
			PodAnnotations map[string]any            `json:"podAnnotations"`
			PodLabels      map[string]any            `json:"podLabels"`
			Bindings       map[string]map[string]any `json:"bindings"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			values := withSecrets(tc.values)
			vf := filepath.Join(t.TempDir(), "values.json")
			writeJSON(t, vf, values)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"vet", "-contract", kitchen, "-values", vf}, &stdout, &stderr); code != 0 {
				t.Fatalf("the case's values do not pass vet: %s%s", stdout.String(), stderr.String())
			}
			v, err := p.LoadData(vf)
			if err != nil {
				t.Fatal(err)
			}
			empty, _ := p.LoadData("")
			rendered, err := p.Render(c, v, empty, empty)
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				Env            []any          `yaml:"env"`
				PodAnnotations map[string]any `yaml:"podAnnotations"`
				PodLabels      map[string]any `yaml:"podLabels"`
			}
			if err := yaml.Unmarshal(rendered, &want); err != nil {
				t.Fatal(err)
			}
			g := got[tc.name].Value
			if gj, wj := canon(t, fromModule(g.Env)), canon(t, want.Env); gj != wj {
				t.Errorf("kubernetes_env differs from docuconf render:\n module: %s\n render: %s", gj, wj)
			}
			if gj, wj := canon(t, g.PodAnnotations), canon(t, want.PodAnnotations); gj != wj {
				t.Errorf("pod_annotations: module %s, render %s", gj, wj)
			}
			if gj, wj := canon(t, g.PodLabels), canon(t, want.PodLabels); gj != wj {
				t.Errorf("pod_labels: module %s, render %s", gj, wj)
			}
			for name := range values {
				if name == "podAnnotations" || name == "podLabels" {
					continue
				}
				if b := g.Bindings[name]; b["set"] != true || b["source"] == nil {
					t.Errorf("bindings[%s] = %v, want it set with a source", name, b)
				}
			}
		})
	}
}

// canon is JSON with sorted keys and numbers as written, so YAML and
// Terraform values compare equal when they mean the same.
func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&x); err != nil {
		t.Fatal(err)
	}
	if len(b) == 4 && string(b) == "null" {
		return "{}"
	}
	b, _ = json.Marshal(x)
	if string(b) == "[]" || string(b) == "{}" {
		return "{}"
	}
	return string(b)
}

var rejectCases = []struct {
	name   string
	values map[string]any
	want   string // in the module's error
}{
	{"int_below_min", map[string]any{"WORKERS": 0}, "WORKERS: 0 is below min 1"},
	{"int_fraction", map[string]any{"WORKERS": 1.5}, "WORKERS: expected an integer, got 1.5"},
	{"string_too_short", map[string]any{"GREETING": "x"}, `GREETING: "x" is 1 characters, below minLength 2`},
	{"string_too_long", map[string]any{"GREETING": "abcdefghijklm"}, "above maxLength 12"},
	{"string_pattern", map[string]any{"GREETING": "abc1"}, `GREETING: "abc1" does not match pattern ^[^0-9]+$`},
	{"enum", map[string]any{"LOG_LEVEL": "trace"}, `LOG_LEVEL: "trace" is not one of debug, info, warn`},
	{"duration_syntax", map[string]any{"TIMEOUT": "ten"}, `TIMEOUT: "ten" is not a duration such as 1m30s`},
	{"duration_above_max", map[string]any{"TIMEOUT": "10m"}, "TIMEOUT: 10m is above max 5m"},
	{"duration_below_min", map[string]any{"TIMEOUT": "999ms"}, "TIMEOUT: 999ms is below min 1s"},
	{"duration_sub_ms", map[string]any{"BACKOFF": "1500us"}, "BACKOFF: 1500us is finer than a millisecond, which the seconds encoding cannot carry"},
	{"url_syntax", map[string]any{"UPSTREAM": "example.com"}, `UPSTREAM: "example.com" is not a URL of the form scheme://...`},
	{"url_scheme", map[string]any{"UPSTREAM": "ftp://example.com"}, `UPSTREAM: scheme "ftp" is not one of http, https`},
	{"float_above_max", map[string]any{"RATIO": 1.5}, "RATIO: 1.5 is above max 1"},
	{"list_min_items", map[string]any{"PORTS": []any{}}, "PORTS: has 0 items, below minItems 1"},
	{"list_max_items", map[string]any{"PORTS": []any{1, 2, 3, 4}}, "PORTS: has 4 items, above maxItems 3"},
	{"list_item_max", map[string]any{"PORTS": []any{70000}}, "PORTS: item 0: 70000 is above itemMax 65535"},
	{"list_item_int", map[string]any{"PORTS": []any{1.5}}, "PORTS: item 0: expected an integer, got 1.5"},
	{"list_item_length", map[string]any{"HOSTS": []any{""}}, `HOSTS: item 0: "" is 0 characters, below itemMinLength 1`},
	{"json_max_length", map[string]any{"FEATURES": map[string]any{"k": strings.Repeat("x", 200)}}, "FEATURES: is 208 characters as compact JSON, above maxLength 200"},
	{"field_path", map[string]any{"NODE_NAME": map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.bogus"}}}, `NODE_NAME: field.field_path "spec.bogus" is not a Downward API field`},
	{"resource", map[string]any{"MEMORY_LIMIT": map[string]any{"resourceFieldRef": map[string]any{"resource": "limits.gpu"}}}, `MEMORY_LIMIT: resource_field.resource "limits.gpu" is not a container resource`},
	{"indexed_injected", map[string]any{"HOSTS": map[string]any{"injected": map[string]any{"provider": "op", "ref": "op://x"}}}, "HOSTS: an indexed list is spread over NAME__0, NAME__1, …, so one injected reference cannot carry it; let the injector set the variables, without ref"},
	{"keyset_indexed_injected", map[string]any{"SIGNING_KEYS": map[string]any{"injected": map[string]any{"provider": "op", "ref": "op://x"}}}, "SIGNING_KEYS: an indexed list is spread over NAME__0"},
	{"provider", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "Bank Vaults"}}}, "GREETING: injected.provider must name the injector as a lowercase label, such as bank-vaults"},
	{"file_placeholder", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "op", "podAnnotations": map[string]any{"a": "{path}"}}}}, "only a file input defines"},
	{"shared_placeholder", map[string]any{"podAnnotations": map[string]any{"a": "{input}"}}, "the shared pod annotations and labels cannot use placeholders"},
	{"label_value", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "op", "podLabels": map[string]any{"a": "not ok"}}}}, `GREETING: pod label a: value "not ok" is not a label value: at most 63 letters`},
	{"annotation_key", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "op", "podAnnotations": map[string]any{"bad key": "x"}}}}, `GREETING: pod annotation key "bad key" is not a Kubernetes qualified name: an optional DNS-subdomain prefix`},
	{"pod_conflict", map[string]any{
		"GREETING":  map[string]any{"injected": map[string]any{"provider": "op", "podAnnotations": map[string]any{"a": "1"}}},
		"NODE_NAME": map[string]any{"injected": map[string]any{"provider": "op", "podAnnotations": map[string]any{"a": "2"}}},
	}, `two inputs set pod annotation ["a"] to different values`},
	{"shared_label_value", map[string]any{"podLabels": map[string]any{"team": "a b"}}, `docuconf_options: pod label team: value "a b" is not a label value`},
}

// ownWording are the cases whose module message names a module attribute
// (field.field_path, docuconf_options) or a conflict only the module
// reports this way.
var ownWording = map[string]bool{
	"field_path": true, "resource": true, "shared_placeholder": true,
	"shared_label_value": true, "pod_conflict": true,
}

func TestTerraformRejectsWhatVetRejects(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
	writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	if out, err := tf(t, bin, dir, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			values := withSecrets(tc.values)
			vf := filepath.Join(t.TempDir(), "values.json")
			writeJSON(t, vf, values)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"vet", "-contract", kitchen, "-values", vf}, &stdout, &stderr); code != 1 {
				t.Errorf("docuconf vet accepts the case (exit %d), so the module should too: %s", code, stderr.String())
			}
			// The module words each problem as vet does, except where it names
			// its own attributes rather than the values document's keys.
			if !strings.Contains(stdout.String(), tc.want) && !ownWording[tc.name] {
				t.Errorf("vet words it differently; the module says %q, vet says:\n%s", tc.want, stdout.String())
			}
			args := toArgs(values)
			args["source"] = "./module"
			writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": args}})
			out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-json")
			if err == nil {
				t.Fatalf("plan succeeded; want an error containing %q", tc.want)
			}
			msgs := diagnostics(out)
			for _, m := range msgs {
				if strings.Contains(m, tc.want) {
					return
				}
			}
			t.Errorf("no error contains %q; got:\n  %s\nvet said:\n%s", tc.want, strings.Join(msgs, "\n  "), stdout.String())
		})
	}
}

// diagnostics are the error messages in terraform's -json output.
func diagnostics(out []byte) []string {
	var msgs []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		var m struct {
			Diagnostic *struct {
				Severity, Summary, Detail string
			} `json:"diagnostic"`
		}
		if json.Unmarshal(line, &m) == nil && m.Diagnostic != nil && m.Diagnostic.Severity == "error" {
			msgs = append(msgs, fmt.Sprintf("%s: %s", m.Diagnostic.Summary, strings.Join(strings.Fields(m.Diagnostic.Detail), " ")))
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, string(out))
	}
	return msgs
}

func TestTerraformRequiredInputs(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
	// REGION is required, but the dev profile supplies it, so the module
	// cannot insist on it; API_TOKEN and DATABASE_URL have no profile.
	writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	if out, err := tf(t, bin, dir, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-json")
	if err == nil {
		t.Fatal("plan succeeded without the required secrets")
	}
	got := strings.Join(diagnostics(out), "\n")
	for _, want := range []string{"API_TOKEN", "DATABASE_URL"} {
		if !strings.Contains(got, want) {
			t.Errorf("no error names %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"REGION"`) {
		t.Errorf("REGION is supplied by a profile but the module requires it:\n%s", got)
	}
}

func TestTerraformDeprecatedWarns(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
	args := toArgs(withSecrets(map[string]any{"LEGACY_MODE": true}))
	args["source"] = "./module"
	writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": args}})
	if out, err := tf(t, bin, dir, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color")
	if err != nil {
		t.Fatalf("a deprecated input should warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(strings.Join(strings.Fields(string(out)), " "), "warning: LEGACY_MODE is deprecated, and the platform still sets it: use MODE instead") {
		t.Errorf("no deprecation warning:\n%s", out)
	}
}

func TestTerraformCheck(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check of a fresh module: exit %d: %s", code, stderr.String())
	}
	// Another generator version alone does not make it stale.
	meta := filepath.Join(dir, "docuconf.json")
	b, _ := os.ReadFile(meta)
	os.WriteFile(meta, bytes.Replace(b, []byte(`"version": "`+version+`"`), []byte(`"version": "0.0.1"`), 1), 0o644)
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check after a version change: exit %d: %s", code, stderr.String())
	}
	os.WriteFile(filepath.Join(dir, "variables.tf"), []byte("# edited\n"), 0o644)
	stderr.Reset()
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir, "-check"}, &stdout, &stderr); code != 1 {
		t.Fatalf("-check of an edited module: exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "variables.tf") || !strings.Contains(stderr.String(), "-# edited") {
		t.Errorf("-check does not name the stale file and show a diff: %s", stderr.String())
	}
	// A generated file the generator no longer writes is stale, and is
	// removed on the next run; a file the platform wrote is left alone.
	old := filepath.Join(dir, "kubernetes_volumes.tf")
	os.WriteFile(old, []byte("# Code generated by docuconf. DO NOT EDIT.\n"), 0o644)
	mine := filepath.Join(dir, "extra.tf")
	os.WriteFile(mine, []byte("locals {}\n"), 0o644)
	stdout.Reset()
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("%s was not removed", old)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("%s was removed: %v", mine, err)
	}
}

func TestTerraformRejectsFiles(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"terraform", "-contract", "../../examples/orders/contract.cue", "-out", t.TempDir()}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "file inputs (discounts, serving-tls) are not supported") {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}
