package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The ecs target: the module's environment must hold what docuconf render
// writes for the same literals (without the kubelet's $ escaping), its
// secrets the valueFrom ECS takes, and its validations reject what the
// target cannot carry.

const (
	tokenARN = "arn:aws:secretsmanager:us-east-1:123456789012:secret:kitchen/token-AbCdEf"
	dbParam  = "arn:aws:ssm:us-east-1:123456789012:parameter/kitchen/database-url"
)

// ecsBase is set in every case.
var ecsBase = map[string]any{
	"API_TOKEN":    map[string]any{"secret": map[string]any{"value_from": tokenARN}},
	"DATABASE_URL": map[string]any{"secret": map[string]any{"value_from": dbParam}},
	"TENANT":       map[string]any{"value": "acme"},
}

func ecsArgs(args map[string]any) map[string]any {
	out := map[string]any{"source": "./module"}
	for k, v := range ecsBase {
		out[k] = v
	}
	for k, v := range args {
		out[k] = v
	}
	return escapeTemplates(out).(map[string]any)
}

func newECSRoot(t *testing.T, contract string) string {
	t.Helper()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", contract, "-target", "ecs", "-out", filepath.Join(dir, "module")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform -target ecs: exit %d: %s", code, stderr.String())
	}
	return dir
}

func TestTerraformECSMatchesRender(t *testing.T) {
	bin := terraformBinary(t)
	dir := newECSRoot(t, kitchen)
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	// The literal cases of the kubernetes test: the same values, set as
	// value arguments.
	literal := map[string]bool{"literals": true, "durations": true, "whole_durations": true, "lists_and_json": true, "code_points": true}
	modules, outputs := map[string]any{}, map[string]any{}
	for _, tc := range renderCases {
		if !literal[tc.name] {
			continue
		}
		args := map[string]any{}
		for k, v := range tc.values {
			args[k] = map[string]any{"value": v}
		}
		modules[tc.name] = ecsArgs(args)
		outputs[tc.name] = map[string]any{"value": map[string]any{
			"environment": "${module." + tc.name + ".ecs_environment}",
			"secrets":     "${module." + tc.name + ".ecs_secrets}",
		}}
	}
	// Secrets with selectors, and injected sources.
	modules["selectors"] = ecsArgs(map[string]any{
		"API_TOKEN":    map[string]any{"secret": map[string]any{"value_from": tokenARN, "json_key": "token", "version_stage": "AWSCURRENT"}},
		"DATABASE_URL": map[string]any{"secret": map[string]any{"value_from": "kitchen-database-url"}},
		"GREETING":     map[string]any{"injected": map[string]any{"provider": "op", "ref": "op://vault/item/$greeting"}},
		"SIGNING_KEYS": map[string]any{"injected": map[string]any{"provider": "op"}},
	})
	outputs["selectors"] = map[string]any{"value": map[string]any{
		"environment": "${module.selectors.ecs_environment}",
		"secrets":     "${module.selectors.ecs_secrets}",
	}}
	initRoot(t, bin, dir, map[string]any{"module": modules, "output": outputs})
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
			Environment []map[string]any `json:"environment"`
			Secrets     []map[string]any `json:"secrets"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantSecrets := []map[string]any{{"name": "API_TOKEN", "valueFrom": tokenARN}, {"name": "DATABASE_URL", "valueFrom": dbParam}}
	for _, tc := range renderCases {
		if !literal[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			// #Render's env for the same values, less the secrets, with the
			// kubelet's $$ back to $: ECS passes values as they are.
			want := renderDoc(t, p, c, tc.doc)
			var env []any
			for _, e := range want.Env {
				m := e.(map[string]any)
				if v, ok := m["value"].(string); ok {
					env = append(env, map[string]any{"name": m["name"], "value": strings.ReplaceAll(v, "$$", "$")})
				}
			}
			g := got[tc.name].Value
			if gj, wj := canon(t, g.Environment), canon(t, env); gj != wj {
				t.Errorf("ecs_environment differs from docuconf render:\n module: %s\n render: %s", gj, wj)
			}
			if gj, wj := canon(t, g.Secrets), canon(t, wantSecrets); gj != wj {
				t.Errorf("ecs_secrets: module %s, want %s", gj, wj)
			}
		})
	}
	t.Run("selectors", func(t *testing.T) {
		g := got["selectors"].Value
		want := []map[string]any{
			{"name": "API_TOKEN", "valueFrom": tokenARN + ":token:AWSCURRENT:"},
			{"name": "DATABASE_URL", "valueFrom": "kitchen-database-url"},
		}
		if gj, wj := canon(t, g.Secrets), canon(t, want); gj != wj {
			t.Errorf("ecs_secrets: module %s, want %s", gj, wj)
		}
		// An injected reference is the variable's value; without one the
		// injector sets the variables and the module renders nothing.
		found := false
		for _, e := range g.Environment {
			if e["name"] == "GREETING" && e["value"] == "op://vault/item/$greeting" {
				found = true
			}
			if strings.HasPrefix(e["name"].(string), "SIGNING_KEYS") {
				t.Errorf("an injected key set without ref rendered %v", e)
			}
		}
		if !found {
			t.Errorf("no GREETING entry with the injected reference, unescaped: %v", g.Environment)
		}
	})
}

func TestTerraformECSRejects(t *testing.T) {
	bin := terraformBinary(t)
	dir := newECSRoot(t, kitchen)
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"secret_as_value", map[string]any{"API_TOKEN": map[string]any{"value": "hunter2"}},
			"API_TOKEN: set exactly one of secret or injected (a secret is never a literal value"},
		{"config_map", map[string]any{"LOG_LEVEL": map[string]any{"config_map": map[string]any{"name": "a", "key": "b"}}},
			"LOG_LEVEL: set exactly one of value or injected (ECS has no ConfigMaps or Downward API"},
		{"value_from", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"value_from": "not a name!"}}},
			`API_TOKEN: secret.value_from "not a name!" is not a Secrets Manager ARN, a Parameter Store ARN, or the name of a secret or parameter`},
		{"selector_needs_arn", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"value_from": "kitchen-token", "json_key": "token"}}},
			"API_TOKEN: secret.json_key, version_stage and version_id select from a Secrets Manager secret, so secret.value_from must be its full ARN"},
		{"selector_on_parameter", map[string]any{"DATABASE_URL": map[string]any{"secret": map[string]any{"value_from": dbParam, "version_id": "1"}}},
			"DATABASE_URL: secret.json_key, version_stage and version_id select from a Secrets Manager secret"},
		{"stage_and_id", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"value_from": tokenARN, "version_stage": "AWSCURRENT", "version_id": "x"}}},
			"API_TOKEN: set secret.version_stage or secret.version_id, not both"},
		{"indexed_key_set", map[string]any{"SIGNING_KEYS": map[string]any{"secret": map[string]any{"value_from": tokenARN}}},
			"SIGNING_KEYS: set exactly one of injected (an indexed key set is one variable per key"},
		{"value_checks", map[string]any{"WORKERS": map[string]any{"value": 0}}, "WORKERS: 0 is below min 1"},
		{"duration", map[string]any{"TIMEOUT": map[string]any{"value": "10m"}}, "TIMEOUT: 10m is above max 5m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": ecsArgs(tc.args)}})
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
			t.Errorf("no error contains %q; got:\n  %s", tc.want, strings.Join(msgs, "\n  "))
		})
	}
}

func TestTerraformECSFiles(t *testing.T) {
	// The kitchen contract's file inputs and overlays are optional: they
	// are left out, with a warning each.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-target", "ecs", "-out", t.TempDir()}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"warning: serving-tls: ECS mounts no files", "warning: overlay appjson: ECS mounts no files"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("no %q in:\n%s", want, stderr.String())
		}
	}
	// A required file input cannot be left out.
	dir := t.TempDir()
	src, _ := os.ReadFile(kitchen)
	c := strings.Replace(string(src), `ca: {type: "caBundle",`, `ca: {type: "caBundle", required: true,`, 1)
	file := filepath.Join(dir, "contract.cue")
	os.WriteFile(file, []byte(c), 0o644)
	stderr.Reset()
	code := run([]string{"terraform", "-contract", file, "-target", "ecs", "-out", filepath.Join(dir, "m")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "the ecs target mounts no files, and the contract requires file inputs ca") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	// One target per module.
	stderr.Reset()
	code = run([]string{"terraform", "-contract", kitchen, "-target", "kubernetes,ecs", "-out", filepath.Join(dir, "m2")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "-target names one target") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}
