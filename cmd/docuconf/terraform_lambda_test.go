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

// The lambda target: the module, applied, against docuconf render; its
// validations; and docuconf lint terraform on a plan made with the aws
// provider (from CI's registry, or TF_CLI_CONFIG_FILE's mirror).

var lambdaBase = map[string]any{
	"API_TOKEN":    map[string]any{"injected": map[string]any{"provider": "aws-parameters-and-secrets", "ref": tokenARN}},
	"DATABASE_URL": map[string]any{"injected": map[string]any{"provider": "aws-parameters-and-secrets", "ref": dbParam}},
	"TENANT":       map[string]any{"value": "acme"},
}

func lambdaArgs(args map[string]any) map[string]any {
	out := map[string]any{"source": "./module"}
	for k, v := range lambdaBase {
		out[k] = v
	}
	for k, v := range args {
		out[k] = v
	}
	return escapeTemplates(out).(map[string]any)
}

func TestTerraformLambdaMatchesRender(t *testing.T) {
	bin := terraformBinary(t)
	dir := newTargetRoot(t, kitchen, "lambda")
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
		if !literalCases[tc.name] {
			continue
		}
		args := map[string]any{}
		for k, v := range tc.values {
			args[k] = map[string]any{"value": v}
		}
		modules[tc.name] = lambdaArgs(args)
		outputs[tc.name] = map[string]any{"value": "${module." + tc.name + ".lambda_environment}"}
	}
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
		Value map[string]string `json:"value"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, tc := range renderCases {
		if !literalCases[tc.name] {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			// docuconf render's env, with the kubelet's $$ back to $, and the
			// injected secrets' references as their values.
			want := map[string]string{"API_TOKEN": tokenARN, "DATABASE_URL": dbParam}
			for _, e := range renderDoc(t, p, c, tc.doc).Env {
				m := e.(map[string]any)
				if v, ok := m["value"].(string); ok {
					want[m["name"].(string)] = strings.ReplaceAll(v, "$$", "$")
				}
			}
			if gj, wj := canon(t, got[tc.name].Value), canon(t, want); gj != wj {
				t.Errorf("lambda_environment differs from docuconf render:\n module: %s\n render: %s", gj, wj)
			}
		})
	}
}

func TestTerraformLambdaRejects(t *testing.T) {
	bin := terraformBinary(t)
	dir := newTargetRoot(t, kitchen, "lambda")
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"secret_as_value", map[string]any{"API_TOKEN": map[string]any{"value": "hunter2"}},
			"API_TOKEN: set exactly one of injected (Lambda resolves no secret references, so a secret is injected"},
		{"secret_reference", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"value_from": tokenARN}}},
			"API_TOKEN: set exactly one of injected (Lambda resolves no secret references"},
		{"field", map[string]any{"NODE_NAME": map[string]any{"field": map[string]any{"field_path": "spec.nodeName"}}},
			"NODE_NAME: set exactly one of value or injected (Lambda has no ConfigMaps or Downward API"},
		{"value_checks", map[string]any{"WORKERS": map[string]any{"value": 0}}, "WORKERS: 0 is below min 1"},
		{"size", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "op", "ref": strings.Repeat("x", 4100)}}},
			"and Lambda allows 4096; move large values to files the function reads, or to Parameter Store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{
				"module": map[string]any{"c": lambdaArgs(tc.args)},
				"output": map[string]any{"env": map[string]any{"value": "${module.c.lambda_environment}"}},
			})
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

func TestTerraformLambdaGeneration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-target", "lambda", "-out", t.TempDir()}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"warning: settings: Lambda mounts no files", "warning: overlay appjson: Lambda mounts no files"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("no %q in:\n%s", want, stderr.String())
		}
	}
	src, err := os.ReadFile(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ from, to, want string }{
		"required_file": {`"serving-tls": {type: "tls",`, `"serving-tls": {type: "tls", required: true,`, "the lambda target mounts no files, and the contract requires file inputs serving-tls"},
		"reserved": {`TENANT:`, `AWS_REGION: {type: "string", description: "The region"}
		TENANT:`, "the contract declares AWS_REGION, which Lambda reserves"},
	} {
		t.Run(name, func(t *testing.T) {
			cue := strings.Replace(string(src), tc.from, tc.to, 1)
			if cue == string(src) {
				t.Fatal("kitchen contract changed; update this test")
			}
			dir := t.TempDir()
			file := filepath.Join(dir, "contract.cue")
			os.WriteFile(file, []byte(cue), 0o644)
			stderr.Reset()
			code := run([]string{"terraform", "-contract", file, "-target", "lambda", "-out", filepath.Join(dir, "m")}, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d: %s", code, stderr.String())
			}
		})
	}
}

func TestLintTerraformLambda(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", ordersContract, "-target", "lambda", "-out", filepath.Join(dir, "modules", "orders-config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: %s", stderr.String())
	}
	fn := func(name, variables string) string {
		return fmt.Sprintf(`
resource "aws_lambda_function" %[1]q {
  function_name = %[1]q
  role          = "arn:aws:iam::123456789012:role/orders-api"
  package_type  = "Image"
  image_uri     = "123456789012.dkr.ecr.us-east-1.amazonaws.com/orders-api:2.3.0"
  environment {
    variables = %[2]s
  }
}
`, name, fmt.Sprintf(variables, name))
	}
	const db = `"arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/db-AbCdEf"`
	args := `
  DATABASE_URL    = { injected = { provider = "aws-parameters-and-secrets", ref = ` + db + ` } }
  LOG_LEVEL       = { value = "warn" }
  ALLOWED_ORIGINS = { value = ["https://shop.example.com/$(x)"] }
`
	scenarios := []struct {
		name, args, variables string
		want                  [][2]string
	}{
		{"good", args, "module.%s.lambda_environment", nil},
		{"typo", args, `merge(module.%s.lambda_environment, { PORTT = "9090" })`,
			[][2]string{{"undeclared_input", "PORTT: is not declared in the contract, and the module did not render it; did you mean PORT?"}}},
		{"hand_value", args, `merge(module.%s.lambda_environment, { REQUEST_TIMEOUT = "10m" })`,
			[][2]string{{"invalid_value", "REQUEST_TIMEOUT: 10m is above max 5m"}}},
		{"hand_secret", args, `merge(module.%s.lambda_environment, { WEBHOOK_KEYS = "k1,k2" })`,
			[][2]string{{"secret_as_value", "WEBHOOK_KEYS: is secret, but the container sets it to a plain value"}}},
		// An injected ref that is not a constant: a secret set through the
		// module can only be its injected ref.
		{"expr_ref", strings.Replace(args, "ref = "+db+" }", "ref = local.db }", 1), "module.%s.lambda_environment", nil},
		{"dropped", args, `{ for k, v in module.%s.lambda_environment : k => v if k != "LOG_LEVEL" }`,
			[][2]string{{"unbound_container", "LOG_LEVEL: module.dropped renders it, but the container does not set it; take the container's env from module.dropped.lambda_environment"}}},
	}
	var root strings.Builder
	root.WriteString(`terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "offline"
  secret_key                  = "offline"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}

locals {
  db = "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/db-AbCdEf"
}
`)
	for _, s := range scenarios {
		fmt.Fprintf(&root, "\nmodule %q {\n  source = \"./modules/orders-config\"%s}\n", s.name, s.args)
		root.WriteString(fn(s.name, s.variables))
	}
	os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644)
	if out, err := tfInit(t, bin, dir); err != nil {
		t.Fatalf("init (needs the aws provider): %v\n%s", err, out)
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
	stdout.Reset()
	stderr.Reset()
	run([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, &stdout, &stderr)
	var got struct{ Findings []lint.Finding }
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	findings := map[string][]lint.Finding{}
	for _, f := range got.Findings {
		findings[f.Module] = append(findings[f.Module], f)
	}
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			got := findings["module."+s.name]
			if len(got) == 0 {
				t.Fatalf("no findings at all, not even notices: is the function bound to module.%s?", s.name)
			}
			for _, w := range s.want {
				if !hasFinding(got, w[0], w[1]) {
					t.Errorf("no %s finding with %q; got:\n%s", w[0], w[1], describeFindings(got))
				}
			}
			for _, f := range got {
				expected := f.Level == lint.Notice && f.Code != "deferred"
				for _, w := range s.want {
					expected = expected || f.Code == w[0]
				}
				if !expected {
					t.Errorf("unexpected %s %s: %s", f.Level, f.Code, f.Message)
				}
			}
		})
	}
}
