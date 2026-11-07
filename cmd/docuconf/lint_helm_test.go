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
)

// docuconf lint terraform on a helm_release that renders a module's
// helm_values with the docuconf library chart: the release's docuconf:
// values are what the chart renders, so they are what the linter checks.
// Needs the helm provider, as the other lint tests need theirs.
func TestLintHelmRelease(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	if _, stderr, code := docuconf(t, "terraform", "-contract", ordersContract, "-out", filepath.Join(dir, "modules", "orders-config")); code != 0 {
		t.Fatal(stderr)
	}
	chart := filepath.Join(dir, "chart")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: orders\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const args = `
  DATABASE_URL = { secret = { name = "orders-db", key = "url" } }
  WEBHOOK_KEYS = { secret = { name = "orders-webhooks", key = "keys" } }
`
	scenarios := []struct {
		name, args, release string
		want                [][2]string
	}{
		{name: "good", args: args + `  LOG_LEVEL = { value = "warn" }
`, release: `values = [yamlencode({ docuconf = module.good.helm_values })]`},
		{name: "override", args: args, release: `values = [yamlencode({ docuconf = module.override.helm_values }), yamlencode({ docuconf = { values = { REQUEST_TIMEOUT = "10m" } } })]`,
			want: [][2]string{{"invalid_value", "REQUEST_TIMEOUT: 10m is above max 5m"}}},
		{name: "typo", args: args, release: `values = [yamlencode({ docuconf = module.typo.helm_values }), yamlencode({ docuconf = { values = { PORTT = 9090 } } })]`,
			want: [][2]string{{"", "PORTT"}}},
		{name: "nokey", args: args, release: `values = [yamlencode({ orders = module.nokey.helm_values })]`,
			want: [][2]string{{"unbound_container", "helm_release.nokey reads module.nokey, but its values have no docuconf key, so the chart renders none of the inputs; pass values = [yamlencode({ docuconf = module.nokey.helm_values })]"}}},
		{name: "unknown", args: args + `  LOG_LEVEL = { value = terraform_data.level.output }
`, release: `values = [yamlencode({ docuconf = module.unknown.helm_values })]`,
			want: [][2]string{{"deferred", "helm_release.unknown's values are unknown or sensitive at plan, so the linter checks only module.unknown's arguments"}}},
		{name: "set", args: args, release: "values = [yamlencode({ docuconf = module.set.helm_values })]\n  set = [{ name = \"docuconf.values.LOG_LEVEL\", value = \"debug\" }]",
			want: [][2]string{{"deferred", "helm_release.set: set docuconf.values.LOG_LEVEL overrides the values, and the linter does not read set blocks"}}},
	}
	var root strings.Builder
	root.WriteString(`terraform {
  required_providers {
    helm = { source = "hashicorp/helm" }
  }
}

provider "helm" {
  kubernetes = { config_path = "/nonexistent" }
}

resource "terraform_data" "level" {
  input = "warn"
}
`)
	for _, s := range scenarios {
		fmt.Fprintf(&root, "\nmodule %q {\n  source = \"./modules/orders-config\"%s}\n", s.name, s.args)
		fmt.Fprintf(&root, "\nresource \"helm_release\" %q {\n  name  = %q\n  chart = \"./chart\"\n  %s\n}\n", s.name, s.name, s.release)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := tfInit(t, bin, dir); err != nil {
		t.Fatalf("init (needs the helm provider): %v\n%s", err, out)
	}
	if out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-refresh=false", "-out", "plan.out"); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	planJSON, err := tf(t, bin, dir, "show", "-json", "plan.out")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, planJSON)
	}
	planFile := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planFile, planJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	run([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, &stdout, &stderr)
	var got struct{ Findings []lint.Finding }
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	byModule := map[string][]lint.Finding{}
	for _, f := range got.Findings {
		byModule[f.Module] = append(byModule[f.Module], f)
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			fs := byModule["module."+s.name]
			for _, w := range s.want {
				found := false
				for _, f := range fs {
					found = found || (w[0] == "" || f.Code == w[0]) && strings.Contains(f.Message, w[1])
				}
				if !found {
					t.Errorf("no %s finding with %q; got:\n%s", w[0], w[1], describeFindings(fs))
				}
			}
			for _, f := range fs {
				if f.Level == lint.Notice {
					continue
				}
				expected := false
				for _, w := range s.want {
					expected = expected || (w[0] == "" || f.Code == w[0]) && strings.Contains(f.Message, w[1])
				}
				if !expected {
					t.Errorf("unexpected %s %s: %s", f.Level, f.Code, f.Message)
				}
				if f.Code == "module_unused" {
					t.Errorf("the release does not bind the module: %s", f.Message)
				}
			}
		})
	}
}
