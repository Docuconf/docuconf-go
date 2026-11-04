package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/lint"
)

// The lint tests plan a root module with one module call and one pod per
// scenario, using the kubernetes provider, and check what docuconf lint
// terraform finds in the plan. They need terraform (or tofu) and the
// provider: CI downloads it; elsewhere, set TF_CLI_CONFIG_FILE to a
// configuration with a provider mirror.

const ordersContract = "../../examples/orders/contract.cue"

type podSpec struct {
	module      string // the module call the pod takes its config from
	wireEnv     bool   // env from the module's kubernetes_env
	wireMounts  bool   // volume mounts from the module
	annotate    bool   // pod annotations from the module
	label       bool   // a pod label from the module's contract output: a reference outside dynamic blocks
	extraEnv    map[string]string
	envFrom     bool
	mounts      string // hand-written volume_mount blocks
	deployment  bool   // a Deployment, with the module's annotations on its own metadata
	noContainer bool   // the app container sets nothing from the module
}

const valueFrom = `
              dynamic "value_from" {
                for_each = env.value.value_from == null ? [] : [env.value.value_from]
                content {
                  dynamic "secret_key_ref" {
                    for_each = value_from.value.secret_key_ref == null ? [] : [value_from.value.secret_key_ref]
                    content {
                      name = secret_key_ref.value.name
                      key  = secret_key_ref.value.key
                    }
                  }
                  dynamic "config_map_key_ref" {
                    for_each = value_from.value.config_map_key_ref == null ? [] : [value_from.value.config_map_key_ref]
                    content {
                      name = config_map_key_ref.value.name
                      key  = config_map_key_ref.value.key
                    }
                  }
                }
              }`

func (p podSpec) hcl(name string) string {
	m := "module." + p.module
	var c strings.Builder
	if p.wireEnv && !p.noContainer {
		fmt.Fprintf(&c, `
          dynamic "env" {
            for_each = %s.kubernetes_env
            content {
              name  = env.value.name
              value = env.value.value%s
            }
          }`, m, valueFrom)
	}
	for _, k := range sortedMapKeys(p.extraEnv) {
		fmt.Fprintf(&c, "\n          env {\n            name  = %q\n            value = %q\n          }", k, p.extraEnv[k])
	}
	if p.envFrom {
		c.WriteString("\n          env_from {\n            config_map_ref { name = \"everything\" }\n          }")
	}
	if p.wireMounts {
		fmt.Fprintf(&c, `
          dynamic "volume_mount" {
            for_each = %s.kubernetes_volume_mounts
            content {
              name       = volume_mount.value.name
              mount_path = volume_mount.value.mount_path
              read_only  = volume_mount.value.read_only
            }
          }`, m)
	}
	c.WriteString(p.mounts)
	volumes := fmt.Sprintf(`
        dynamic "volume" {
          for_each = %s.kubernetes_volumes
          content {
            name = volume.value.name
            dynamic "config_map" {
              for_each = volume.value.config_map == null ? [] : [volume.value.config_map]
              content {
                name = config_map.value.name
                dynamic "items" {
                  for_each = config_map.value.items
                  content {
                    key  = items.value.key
                    path = items.value.path
                  }
                }
              }
            }
            dynamic "secret" {
              for_each = volume.value.secret == null ? [] : [volume.value.secret]
              content { secret_name = secret.value.secret_name }
            }
          }
        }`, m)
	ann := ""
	if p.annotate {
		ann = fmt.Sprintf("\n        annotations = %s.pod_annotations", m)
	}
	if p.label {
		ann += fmt.Sprintf("\n        labels      = { \"app.kubernetes.io/name\" = %s.contract.name }", m)
	}
	containers := fmt.Sprintf(`
        container {
          name  = "app"
          image = "ghcr.io/acme/orders-api:1.4.0"%s
        }
        container {
          name  = "sidecar"
          image = "envoyproxy/envoy:v1.30"
          env {
            name  = "ENVOY_UID"
            value = "0"
          }
        }%s`, c.String(), volumes)
	if p.deployment {
		return fmt.Sprintf(`
resource "kubernetes_deployment_v1" %q {
  metadata {
    name        = %q
    annotations = %s.pod_annotations
  }
  spec {
    selector { match_labels = { app = %q } }
    template {
      metadata {
        labels = { app = %q }
      }
      spec {%s
      }
    }
  }
}
`, name, strings.ReplaceAll(name, "_", "-"), m, name, name, containers)
	}
	return fmt.Sprintf(`
resource "kubernetes_pod_v1" %q {
  metadata {
    name = %q%s
  }
  spec {%s
  }
}
`, name, strings.ReplaceAll(name, "_", "-"), ann, containers)
}

func sortedMapKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lintScenario is one module call, the pod that uses it, and what the
// linter must find: each want is a code and a substring of its message.
type lintScenario struct {
	name string
	args string // the module call's arguments, in HCL
	pod  *podSpec
	want [][2]string
	// source overrides the module's directory, for a stale copy.
	source string
}

const goodArgs = `
  DATABASE_URL = { secret = { name = kubernetes_secret_v1.db.metadata[0].name, key = "url" } }
  WEBHOOK_KEYS = { secret = { name = "orders-webhooks", key = "keys" } }
  LOG_LEVEL    = { value = var.log_level }
  PORT         = { value = 8080 }
  discounts    = { inline = { codes = { WELCOME10 = 10 } } }
  serving-tls  = { secret = { name = "orders-tls" } }
`

func wired(module string) *podSpec {
	return &podSpec{module: module, wireEnv: true, wireMounts: true, annotate: true}
}

var lintScenarios = []lintScenario{
	{name: "good", args: goodArgs, pod: wired("good")},
	{name: "typo", args: goodArgs, pod: func() *podSpec { p := wired("typo"); p.extraEnv = map[string]string{"PORTT": "9090"}; return p }(),
		want: [][2]string{{"undeclared_input", "PORTT: is not declared in the contract, and the module did not render it; did you mean PORT?"}}},
	{name: "duplicate", args: goodArgs, pod: func() *podSpec {
		p := wired("duplicate")
		p.extraEnv = map[string]string{"LOG_LEVEL": "debug"}
		return p
	}(), want: [][2]string{{"duplicate_env", "LOG_LEVEL: is set 2 times in the container's env"}}},
	{name: "secret_value", args: strings.Replace(goodArgs, `  WEBHOOK_KEYS = { secret = { name = "orders-webhooks", key = "keys" } }`, "", 1),
		pod: func() *podSpec {
			p := wired("secret_value")
			p.extraEnv = map[string]string{"WEBHOOK_KEYS": "hunter2"}
			return p
		}(),
		want: [][2]string{{"secret_as_value", "WEBHOOK_KEYS: is secret, but the container sets it to a plain value"}}},
	{name: "unwired", args: goodArgs, pod: &podSpec{module: "unwired", annotate: true, wireMounts: true},
		want: [][2]string{{"unbound_container", "none of its containers takes the env the module renders"}}},
	{name: "env_from", args: goodArgs, pod: func() *podSpec { p := wired("env_from"); p.envFrom = true; return p }(),
		want: [][2]string{{"env_from_unchecked", "the container takes env_from a whole ConfigMap or Secret"}}},
	{name: "mounts", args: goodArgs, pod: &podSpec{module: "mounts", wireEnv: true, annotate: true, mounts: `
          volume_mount {
            name       = "dc-discounts"
            mount_path = "/etc/orders"
            read_only  = true
          }
          volume_mount {
            name       = "dc-serving-tls"
            mount_path = "/etc/orders/tls"
            read_only  = false
          }`},
		want: [][2]string{
			{"mount_mismatch", "discounts: volume dc-discounts is mounted at /etc/orders, but the app reads it from /etc/orders/discounts"},
			{"mount_mismatch", "serving-tls: volume dc-serving-tls is mounted writable"},
		}},
	{name: "misplaced", args: goodArgs, pod: func() *podSpec { p := wired("misplaced"); p.deployment = true; return p }(),
		want: [][2]string{{"pod_metadata_misplaced", "module.misplaced.pod_annotations is on the workload's metadata"}}},
	{name: "injected", args: goodArgs + `
  REQUEST_TIMEOUT = { injected = { provider = "bank-vaults", pod_annotations = { "vault.security.banzaicloud.io/vault-role" = "{input}" } } }
`, pod: &podSpec{module: "injected", wireEnv: true, wireMounts: true, label: true},
		want: [][2]string{{"pod_metadata_missing", "the pod template lacks annotation vault.security.banzaicloud.io/vault-role=REQUEST_TIMEOUT"}}},
	{name: "hand_value", args: goodArgs, pod: func() *podSpec {
		p := wired("hand_value")
		p.extraEnv = map[string]string{"REQUEST_TIMEOUT": "10m"}
		return p
	}(), want: [][2]string{{"invalid_value", "REQUEST_TIMEOUT: 10m is above max 5m"}}},
	{name: "deferred", args: goodArgs + `
  ALLOWED_ORIGINS = { value = ["https://${kubernetes_namespace_v1.ns.metadata[0].uid}.example.com"] }
`, pod: wired("deferred"),
		want: [][2]string{{"deferred", "ALLOWED_ORIGINS: is unknown at plan (from kubernetes_namespace_v1.ns.metadata[0].uid); it is checked at apply"}}},
	{name: "unused", args: goodArgs, want: [][2]string{{"module_unused", "no workload in the plan takes module.unused's outputs"}}},
	{name: "stale", args: goodArgs, source: "./modules/stale", pod: wired("stale"),
		want: [][2]string{{"stale_module", "differs from what docuconf terraform generates from its docuconf.json (README.md)"}}},
}

func TestLintTerraform(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	module := filepath.Join(dir, "modules", "orders-config")
	if code := run([]string{"terraform", "-contract", ordersContract, "-out", module}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: %s", stderr.String())
	}
	// A copy someone edited by hand.
	stale := filepath.Join(dir, "modules", "stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(module)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(module, e.Name()))
		if e.Name() == "README.md" {
			b = append(b, "Edited by hand.\n"...)
		}
		os.WriteFile(filepath.Join(stale, e.Name()), b, 0o644)
	}

	var root strings.Builder
	root.WriteString(`terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

provider "kubernetes" {
  host = "https://127.0.0.1:6443"
}

variable "log_level" {
  type    = string
  default = "warn"
}

resource "kubernetes_namespace_v1" "ns" {
  metadata { name = "orders" }
}

resource "kubernetes_secret_v1" "db" {
  metadata { name = "orders-db" }
  data = { url = "postgres://db" }
}
`)
	for _, s := range lintScenarios {
		src := s.source
		if src == "" {
			src = "./modules/orders-config"
		}
		fmt.Fprintf(&root, "\nmodule %q {\n  source = %q%s}\n", s.name, src, s.args)
		if s.pod != nil {
			root.WriteString(s.pod.hcl(s.name))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := tf(t, bin, dir, "init", "-input=false", "-no-color"); err != nil {
		t.Fatalf("init (needs the kubernetes provider; see the comment above): %v\n%s", err, out)
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

	lintRun := func(extra ...string) ([]lint.Finding, int, string) {
		stdout.Reset()
		stderr.Reset()
		code := run(append([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, extra...), &stdout, &stderr)
		var got struct{ Findings []lint.Finding }
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
		}
		return got.Findings, code, stderr.String()
	}
	findings, code, errOut := lintRun()
	if code != 1 {
		t.Errorf("exit %d, want 1: %s", code, errOut)
	}
	byModule := map[string][]lint.Finding{}
	for _, f := range findings {
		byModule[f.Module] = append(byModule[f.Module], f)
	}
	for _, s := range lintScenarios {
		t.Run(s.name, func(t *testing.T) {
			got := byModule["module."+s.name]
			for _, w := range s.want {
				found := false
				for _, f := range got {
					if f.Code == w[0] && strings.Contains(f.Message, w[1]) {
						found = true
					}
				}
				if !found {
					t.Errorf("no %s finding with %q; got:\n%s", w[0], w[1], describeFindings(got))
				}
			}
			// Nothing else at error or warning level.
			for _, f := range got {
				if f.Level == lint.Notice {
					continue
				}
				expected := false
				for _, w := range s.want {
					if f.Code == w[0] {
						expected = true
					}
				}
				if !expected {
					t.Errorf("unexpected %s %s: %s", f.Level, f.Code, f.Message)
				}
			}
		})
	}

	t.Run("policy", func(t *testing.T) {
		policy := filepath.Join(dir, "prod.cue")
		os.WriteFile(policy, []byte("secretReferences: [string]: secretKeyRef: name: =~\"^orders-\"\nLOG_LEVEL?: \"info\" | \"warn\" | \"error\"\n"), 0o644)
		findings, _, _ := lintRun("-policy", policy)
		var hits []string
		for _, f := range findings {
			if f.Code == "policy_mismatch" && f.Module == "module.good" {
				hits = append(hits, f.Message)
			}
		}
		if len(hits) != 0 {
			t.Errorf("module.good meets the policy, but: %v", hits)
		}
		os.WriteFile(policy, []byte("secretReferences: [string]: secretKeyRef: name: =~\"^prod-\"\n"), 0o644)
		findings, _, _ = lintRun("-policy", policy)
		found := false
		for _, f := range findings {
			if f.Code == "policy_mismatch" && f.Module == "module.good" && strings.Contains(f.Message, "DATABASE_URL") && strings.Contains(f.Message, "(policy prod.cue)") {
				found = true
			}
		}
		if !found {
			t.Errorf("no policy_mismatch for DATABASE_URL on module.good:\n%s", describeFindings(findings))
		}
	})

	t.Run("explain", func(t *testing.T) {
		stdout.Reset()
		run([]string{"lint", "terraform", planFile, "-dir", dir, "-explain"}, &stdout, &stderr)
		out := strings.Join(strings.Fields(stdout.String()), " ")
		for _, want := range []string{
			`module.good → kubernetes_pod_v1.good, container "app"`,
			"DATABASE_URL secret kubernetes_secret_v1.db.metadata[0].name known, checked",
			"PORT value literal 8080 known, checked",
			"WORKER_COUNT — unset —",
			"ALLOWED_ORIGINS value kubernetes_namespace_v1.ns.metadata[0].uid unknown: checked at apply",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("-explain lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("sarif", func(t *testing.T) {
		stdout.Reset()
		run([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "sarif"}, &stdout, &stderr)
		var s struct {
			Version string
			Runs    []struct{ Results []map[string]any }
		}
		if err := json.Unmarshal(stdout.Bytes(), &s); err != nil || s.Version != "2.1.0" || len(s.Runs) != 1 || len(s.Runs[0].Results) == 0 {
			t.Errorf("not SARIF 2.1.0 with results: %v\n%s", err, stdout.String())
		}
	})

	t.Run("contract", func(t *testing.T) {
		findings, _, _ := lintRun("-contract", ordersContract)
		for _, f := range findings {
			if f.Code == "stale_module" && f.Module != "module.stale" {
				t.Errorf("%s: %s", f.Module, f.Message)
			}
		}
	})
}

func describeFindings(fs []lint.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "  %s %s %s: %s\n", f.Level, f.Code, f.Where, f.Message)
	}
	return b.String()
}
