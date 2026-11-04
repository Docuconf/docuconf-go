package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/lint"
	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The cloudrun target: the module, applied, against docuconf render; its
// validations; and docuconf lint terraform on a plan made with the google
// provider (from CI's registry, or TF_CLI_CONFIG_FILE's mirror).

var cloudrunBase = map[string]any{
	"API_TOKEN":    map[string]any{"secret": map[string]any{"secret": "kitchen-token"}},
	"DATABASE_URL": map[string]any{"secret": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-db", "version": "4"}},
	"TENANT":       map[string]any{"value": "acme"},
}

func cloudrunArgs(args map[string]any) map[string]any {
	out := map[string]any{"source": "./module"}
	for k, v := range cloudrunBase {
		out[k] = v
	}
	for k, v := range args {
		out[k] = v
	}
	return escapeTemplates(out).(map[string]any)
}

func newTargetRoot(t *testing.T, contract, target string) string {
	t.Helper()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", contract, "-target", target, "-out", filepath.Join(dir, "module")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform -target %s: exit %d: %s", target, code, stderr.String())
	}
	return dir
}

func TestTerraformCloudRunMatchesRender(t *testing.T) {
	bin := terraformBinary(t)
	dir := newTargetRoot(t, kitchen, "cloudrun")
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	literal := map[string]bool{"literals": true, "durations": true, "whole_durations": true, "lists_and_json": true, "code_points": true}
	modules, outputs := map[string]any{}, map[string]any{}
	out := func(name string) map[string]any {
		return map[string]any{"value": map[string]any{
			"env":     "${module." + name + ".cloudrun_env}",
			"volumes": "${module." + name + ".cloudrun_volumes}",
			"mounts":  "${module." + name + ".cloudrun_volume_mounts}",
		}}
	}
	for _, tc := range renderCases {
		if !literal[tc.name] {
			continue
		}
		args := map[string]any{}
		for k, v := range tc.values {
			args[k] = map[string]any{"value": v}
		}
		modules[tc.name] = cloudrunArgs(args)
		outputs[tc.name] = out(tc.name)
	}
	modules["files"] = cloudrunArgs(map[string]any{
		"settings": map[string]any{"secret": map[string]any{"secret": "kitchen-settings"}},
		"keystore": map[string]any{"secret": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-ks", "version": "2"}},
		"ca":       map[string]any{"injected": map[string]any{"provider": "trust-manager"}},
	})
	outputs["files"] = out("files")
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
			Env     []map[string]any `json:"env"`
			Volumes []map[string]any `json:"volumes"`
			Mounts  []map[string]any `json:"mounts"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	secrets := []any{
		map[string]any{"name": "API_TOKEN", "value_from": map[string]any{"secret_key_ref": map[string]any{"secret": "kitchen-token", "version": "latest"}}},
		map[string]any{"name": "DATABASE_URL", "value_from": map[string]any{"secret_key_ref": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-db", "version": "4"}}},
	}
	for _, tc := range renderCases {
		if !literal[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			want := renderDoc(t, p, c, tc.doc)
			var env []any
			for _, e := range want.Env {
				m := e.(map[string]any)
				if v, ok := m["value"].(string); ok {
					env = append(env, map[string]any{"name": m["name"], "value": strings.ReplaceAll(v, "$$", "$")})
				}
			}
			var gotEnv, gotSecrets []any
			for _, e := range got[tc.name].Value.Env {
				if e["value_from"] != nil {
					gotSecrets = append(gotSecrets, map[string]any{"name": e["name"], "value_from": e["value_from"]})
				} else {
					gotEnv = append(gotEnv, map[string]any{"name": e["name"], "value": e["value"]})
				}
			}
			if gj, wj := canon(t, gotEnv), canon(t, env); gj != wj {
				t.Errorf("cloudrun_env values differ from docuconf render:\n module: %s\n render: %s", gj, wj)
			}
			if gj, wj := canon(t, gotSecrets), canon(t, secrets); gj != wj {
				t.Errorf("cloudrun_env secrets: module %s, want %s", gj, wj)
			}
		})
	}
	t.Run("files", func(t *testing.T) {
		g := got["files"].Value
		wantVols := []any{
			map[string]any{"name": "dc-settings", "secret": map[string]any{"secret": "kitchen-settings", "default_mode": 292, "items": []any{map[string]any{"version": "latest", "path": "settings.yaml"}}}},
			map[string]any{"name": "dc-keystore", "secret": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-ks", "default_mode": 256, "items": []any{map[string]any{"version": "2", "path": "client.p12"}}}},
		}
		wantMounts := []any{
			map[string]any{"name": "dc-settings", "mount_path": "/etc/kitchen/settings"},
			map[string]any{"name": "dc-keystore", "mount_path": "/etc/kitchen/ks"},
		}
		if gj, wj := canon(t, g.Volumes), canon(t, wantVols); gj != wj {
			t.Errorf("cloudrun_volumes:\n module %s\n want   %s", gj, wj)
		}
		if gj, wj := canon(t, g.Mounts), canon(t, wantMounts); gj != wj {
			t.Errorf("cloudrun_volume_mounts:\n module %s\n want   %s", gj, wj)
		}
		found := false
		for _, e := range g.Env {
			found = found || (e["name"] == "SETTINGS_PATH" && e["value"] == "/etc/kitchen/settings/settings.yaml")
		}
		if !found {
			t.Errorf("no SETTINGS_PATH entry: %v", g.Env)
		}
	})
}

func TestTerraformCloudRunRejects(t *testing.T) {
	bin := terraformBinary(t)
	dir := newTargetRoot(t, kitchen, "cloudrun")
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"secret_as_value", map[string]any{"API_TOKEN": map[string]any{"value": "hunter2"}},
			"API_TOKEN: set exactly one of secret or injected (a secret is never a literal value"},
		{"field", map[string]any{"NODE_NAME": map[string]any{"field": map[string]any{"field_path": "spec.nodeName"}}},
			"NODE_NAME: set exactly one of value or injected (Cloud Run has no ConfigMaps or Downward API"},
		{"secret_name", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"secret": "projects/x/secrets/a b"}}},
			`API_TOKEN: secret.secret "projects/x/secrets/a b" is not a Secret Manager secret ID or projects/PROJECT/secrets/SECRET`},
		{"version", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"secret": "t", "version": "v2"}}},
			`API_TOKEN: secret.version "v2" is not latest or a version number`},
		{"file_inline", map[string]any{"settings": map[string]any{"inline": "a: 1"}},
			"settings: set exactly one of secret or injected (Cloud Run mounts files from Secret Manager"},
		{"file_version", map[string]any{"settings": map[string]any{"secret": map[string]any{"secret": "s", "version": "0"}}},
			`settings: secret.version "0" is not latest or a version number`},
		{"value_checks", map[string]any{"WORKERS": map[string]any{"value": 0}}, "WORKERS: 0 is below min 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": cloudrunArgs(tc.args)}})
			out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-json")
			if err == nil {
				t.Fatalf("plan succeeded; want an error containing %q", tc.want)
			}
			for _, m := range diagnostics(out) {
				if strings.Contains(m, tc.want) {
					return
				}
			}
			t.Errorf("no error contains %q; got:\n  %s", tc.want, strings.Join(diagnostics(out), "\n  "))
		})
	}
}

func TestTerraformCloudRunTLS(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-target", "cloudrun", "-out", t.TempDir()}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"warning: serving-tls: a tls input is a directory of several files", "warning: overlay appjson: Cloud Run has no ConfigMaps"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("no %q in:\n%s", want, stderr.String())
		}
	}
	dir := t.TempDir()
	src, _ := os.ReadFile(kitchen)
	file := filepath.Join(dir, "contract.cue")
	os.WriteFile(file, []byte(strings.Replace(string(src), `"serving-tls": {type: "tls",`, `"serving-tls": {type: "tls", required: true,`, 1)), 0o644)
	stderr.Reset()
	code := run([]string{"terraform", "-contract", file, "-target", "cloudrun", "-out", filepath.Join(dir, "m")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "the contract requires tls inputs serving-tls") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}

func TestLintTerraformCloudRun(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", ordersContract, "-target", "cloudrun", "-out", filepath.Join(dir, "modules", "orders-config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: %s", stderr.String())
	}
	service := func(name, extra string) string {
		return fmt.Sprintf(`
resource "google_cloud_run_v2_service" %[1]q {
  name     = %[1]q
  location = "us-central1"
  labels   = { app = module.%[1]s.contract.name }
  template {
    containers {
      image = "registry.example.com/orders-api:2.3.0"
      dynamic "env" {
        for_each = module.%[1]s.cloudrun_env
        content {
          name  = env.value.name
          value = env.value.value
          dynamic "value_source" {
            for_each = env.value.value_from == null ? [] : [env.value.value_from]
            content {
              secret_key_ref {
                secret  = value_source.value.secret_key_ref.secret
                version = value_source.value.secret_key_ref.version
              }
            }
          }
        }
      }%[2]s
    }
    dynamic "volumes" {
      for_each = module.%[1]s.cloudrun_volumes
      content {
        name = volumes.value.name
        secret {
          secret = volumes.value.secret.secret
          dynamic "items" {
            for_each = volumes.value.secret.items
            content {
              version = items.value.version
              path    = items.value.path
            }
          }
        }
      }
    }
  }
}
`, name, extra)
	}
	mount := `
      dynamic "volume_mounts" {
        for_each = module.%s.cloudrun_volume_mounts
        content {
          name       = volume_mounts.value.name
          mount_path = volume_mounts.value.mount_path
        }
      }`
	args := `
  DATABASE_URL = { secret = { secret = "projects/acme-prod/secrets/orders-db", version = "7" } }
  WEBHOOK_KEYS = { secret = { secret = "orders-webhook-keys", version = "3" } }
  LOG_LEVEL    = { value = "warn" }
  discounts    = { secret = { secret = "orders-discounts", version = "1" } }
`
	scenarios := []struct {
		name, args, extra string
		want              [][2]string
	}{
		{"good", args, fmt.Sprintf(mount, "good"), nil},
		{"typo", args, fmt.Sprintf(mount, "typo") + `
      env {
        name  = "PORTT"
        value = "9090"
      }`, [][2]string{{"undeclared_input", "PORTT: is not declared in the contract, and the module did not render it; did you mean PORT?"}}},
		{"mounts", args, `
      volume_mounts {
        name       = "dc-discounts"
        mount_path = "/etc/orders"
      }`, [][2]string{{"mount_mismatch", "discounts: volume dc-discounts is mounted at /etc/orders, but the app reads it from /etc/orders/discounts"}}},
		{"hand_value", args, fmt.Sprintf(mount, "hand_value") + `
      env {
        name  = "REQUEST_TIMEOUT"
        value = "10m"
      }`, [][2]string{{"invalid_value", "REQUEST_TIMEOUT: 10m is above max 5m"}}},
		{"other_project", strings.Replace(args, "acme-prod", "acme-dev", 1), fmt.Sprintf(mount, "other_project"), nil},
	}
	var root strings.Builder
	root.WriteString(`terraform {
  required_providers {
    google = { source = "hashicorp/google" }
  }
}

provider "google" {
  project      = "acme-prod"
  region       = "us-central1"
  access_token = "offline"
}
`)
	for _, s := range scenarios {
		fmt.Fprintf(&root, "\nmodule %q {\n  source = \"./modules/orders-config\"%s}\n", s.name, s.args)
		root.WriteString(service(s.name, s.extra))
	}
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644)
	if out, err := tfInit(t, bin, dir); err != nil {
		t.Fatalf("init (needs the google provider; see the comment above): %v\n%s", err, out)
	}
	if out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-refresh=false", "-out", "plan.out"); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	planJSON, err := tf(t, bin, dir, "show", "-json", "plan.out")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, planJSON)
	}
	planFile := filepath.Join(dir, "plan.json")
	os.WriteFile(planFile, planJSON, 0o644)
	lintRun := func(extra ...string) map[string][]lint.Finding {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		run(append([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, extra...), &stdout, &stderr)
		var got struct{ Findings []lint.Finding }
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
		}
		m := map[string][]lint.Finding{}
		for _, f := range got.Findings {
			m[f.Module] = append(m[f.Module], f)
		}
		return m
	}
	findings := lintRun()
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			got := findings["module."+s.name]
			for _, w := range s.want {
				if !hasFinding(got, w[0], w[1]) {
					t.Errorf("no %s finding with %q; got:\n%s", w[0], w[1], describeFindings(got))
				}
			}
			for _, f := range got {
				expected := f.Level == lint.Notice
				for _, w := range s.want {
					expected = expected || f.Code == w[0]
				}
				if !expected {
					t.Errorf("unexpected %s %s: %s", f.Level, f.Code, f.Message)
				}
			}
			// Cloud Run containers here are unnamed: findings say which.
			for _, f := range got {
				if f.Where != "" && !strings.Contains(f.Where, "container 1") {
					t.Errorf("where %q does not name the container by position", f.Where)
				}
			}
		})
	}
	t.Run("policy", func(t *testing.T) {
		policy := filepath.Join(dir, "prod.cue")
		os.WriteFile(policy, []byte(`secretReferences: [string]: gcpSecretManager: {project: "acme-prod", version: !="latest"}
`), 0o644)
		got := lintRun("-policy", policy)
		if f := got["module.good"]; hasLevel(f, lint.Error, "policy_mismatch") {
			t.Errorf("module.good meets the policy:\n%s", describeFindings(f))
		}
		if !hasFinding(got["module.good"], "policy_deferred", "WEBHOOK_KEYS: gcpSecretManager.project is unknown at plan, and the policy constrains it") {
			t.Errorf("no policy_deferred for a bare secret ID:\n%s", describeFindings(got["module.good"]))
		}
		if !hasFinding(got["module.other_project"], "policy_mismatch", `DATABASE_URL: gcpSecretManager.project "acme-dev" is not allowed by policy (policy prod.cue)`) {
			t.Errorf("no project mismatch:\n%s", describeFindings(got["module.other_project"]))
		}
	})
}
