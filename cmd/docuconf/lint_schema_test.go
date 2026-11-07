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

// origin_type_mismatch: with -schema (terraform providers schema -json),
// an unknown value is checked by the type of the attribute it comes from.
// Plans with the kubernetes and aws providers, as the other lint tests do.
func TestLintSchema(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	if d := os.Getenv("KEEP"); d != "" {
		dir = d
	}
	if _, stderr, code := docuconf(t, "terraform", "-contract", ordersContract, "-out", filepath.Join(dir, "modules", "orders-config")); code != 0 {
		t.Fatal(stderr)
	}
	const secrets = `
  DATABASE_URL = { secret = { name = "orders-db", key = "url" } }
  WEBHOOK_KEYS = { secret = { name = "orders-webhooks", key = "keys" } }
`
	modules := map[string]string{
		// The database's attributes are unknown until it is created.
		"typed": secrets + `  LOG_LEVEL       = { value = aws_db_instance.db.multi_az }
  REQUEST_TIMEOUT = { value = aws_db_instance.db.port }
  PORT            = { value = aws_db_instance.db.address }
  WORKER_COUNT    = { value = aws_db_instance.db.port }
`,
		// A ConfigMap's name, unknown until the database is: a source's
		// reference, which says nothing of the value's type.
		"configmap": secrets + `  PORT = { config_map = { name = kubernetes_config_map_v1.cm.metadata[0].name, key = "port" } }
`,
		// Two attributes in one string: its type is the template's.
		"interpolated": secrets + `  LOG_LEVEL = { value = "${aws_db_instance.db.address}${aws_db_instance.db.port}" }
`,
	}
	var root strings.Builder
	root.WriteString(`terraform {
  required_providers {
    aws        = { source = "hashicorp/aws" }
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}

resource "aws_db_instance" "db" {
  identifier          = "orders"
  engine              = "postgres"
  instance_class      = "db.t3.micro"
  allocated_storage   = 20
  username            = "orders"
  password            = "not-a-real-password"
  skip_final_snapshot = true
}
`)
	root.WriteString(`
resource "kubernetes_config_map_v1" "cm" {
  metadata { name = "orders-${aws_db_instance.db.resource_id}" }
  data = { port = "8080" }
}
`)
	for _, name := range []string{"configmap", "interpolated", "typed"} {
		fmt.Fprintf(&root, "\nmodule %q {\n  source = \"./modules/orders-config\"%s}\n", name, modules[name])
		fmt.Fprintf(&root, `
resource "kubernetes_deployment_v1" %[1]q {
  metadata { name = %[1]q }
  spec {
    selector { match_labels = { app = %[1]q } }
    template {
      metadata {
        labels      = { app = %[1]q }
        annotations = module.%[1]s.pod_annotations
      }
      spec {
        container {
          name  = "orders-api"
          image = "registry.example.com/orders-api:2.3.0"
          dynamic "env" {
            for_each = module.%[1]s.kubernetes_env
            content {
              name  = env.value.name
              value = env.value.value
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
                }
              }
            }
          }
        }
      }
    }
  }
}
`, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := tfInit(t, bin, dir); err != nil {
		t.Fatalf("init (needs the aws and kubernetes providers): %v\n%s", err, out)
	}
	if out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-refresh=false", "-out", "plan.out"); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	planJSON, err := tf(t, bin, dir, "show", "-json", "plan.out")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, planJSON)
	}
	schemaJSON, err := tf(t, bin, dir, "providers", "schema", "-json")
	if err != nil {
		t.Fatalf("providers schema: %v", err)
	}
	planFile, schemaFile := filepath.Join(dir, "plan.json"), filepath.Join(dir, "schema.json")
	os.WriteFile(planFile, planJSON, 0o644)
	os.WriteFile(schemaFile, schemaJSON, 0o644)

	lintRun := func(extra ...string) map[string][]lint.Finding {
		t.Helper()
		var stdout, stderr bytes.Buffer
		run(append([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, extra...), &stdout, &stderr)
		var got struct{ Findings []lint.Finding }
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
		}
		m := map[string][]lint.Finding{}
		for _, f := range got.Findings {
			if f.Code == "origin_type_mismatch" {
				m[f.Module] = append(m[f.Module], f)
			}
		}
		return m
	}
	if got := lintRun(); len(got) != 0 {
		t.Errorf("without -schema, want no origin_type_mismatch, got %v", got)
	}
	got := lintRun("-schema", schemaFile)
	want := []string{
		"LOG_LEVEL: its value comes from aws_db_instance.db.multi_az, a bool attribute in the provider schema, and a bool is true, which is not one of debug, info, warn, error",
		"PORT: its value comes from aws_db_instance.db.address, a string attribute in the provider schema, and a string may hold a number, which only the apply-time precondition decides",
		"REQUEST_TIMEOUT: its value comes from aws_db_instance.db.port, a number attribute in the provider schema, and a number cannot be a duration",
	}
	fs := got["module.typed"]
	if len(fs) != len(want) {
		t.Errorf("typed: want %d findings, got:\n%s", len(want), describeFindings(fs))
	}
	for _, w := range want {
		if !hasFinding(fs, "origin_type_mismatch", w) {
			t.Errorf("typed: no finding %q; got:\n%s", w, describeFindings(fs))
		}
	}
	for _, f := range fs {
		if f.Level != lint.Warning {
			t.Errorf("%s is a %s, want a warning", f.Message, f.Level)
		}
	}
	for _, name := range []string{"configmap", "interpolated"} {
		if fs := got["module."+name]; len(fs) != 0 {
			t.Errorf("%s: want nothing, got:\n%s", name, describeFindings(fs))
		}
	}
	// A schema that is not one.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"lint", "terraform", planFile, "-dir", dir, "-schema", planFile}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "not a provider schema") {
		t.Errorf("bad -schema: exit %d: %s", code, stderr.String())
	}
}
