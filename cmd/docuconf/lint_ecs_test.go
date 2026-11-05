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

// docuconf lint terraform on the ecs target: task definitions planned with
// the aws provider (which, like the kubernetes provider, CI downloads and
// elsewhere comes from TF_CLI_CONFIG_FILE's mirror), and the policy forms
// of ECS references.

const (
	prodDB   = "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf"
	otherDB  = "arn:aws:secretsmanager:us-east-1:210987654321:secret:orders/prod/db-AbCdEf"
	hooksSSM = "arn:aws:ssm:us-east-1:123456789012:parameter/orders/prod/webhook-keys"
)

type ecsScenario struct {
	name string
	args string // module arguments, HCL
	// container is the container definition's HCL, with %[1]s for the
	// module's address.
	container string
	want      [][2]string
}

const ecsGoodArgs = `
  DATABASE_URL = { secret = { value_from = %q } }
  WEBHOOK_KEYS = { secret = { value_from = %q } }
  LOG_LEVEL    = { value = "warn" }
  PORT         = { value = 8080 }
`

const ecsWired = `{
    name        = "orders-api"
    image       = "registry.example.com/orders-api:2.3.0"
    environment = %[1]s.ecs_environment
    secrets     = %[1]s.ecs_secrets
  }`

var ecsScenarios = []ecsScenario{
	{name: "good", args: fmt.Sprintf(ecsGoodArgs, prodDB, hooksSSM), container: ecsWired},
	{name: "typo", args: fmt.Sprintf(ecsGoodArgs, prodDB, hooksSSM), container: `{
    name        = "orders-api"
    image       = "registry.example.com/orders-api:2.3.0"
    environment = concat(%[1]s.ecs_environment, [{ name = "PORTT", value = "9090" }])
    secrets     = %[1]s.ecs_secrets
  }`, want: [][2]string{{"undeclared_input", "PORTT: is not declared in the contract, and the module did not render it; did you mean PORT?"}}},
	{name: "secret_value", args: fmt.Sprintf(`
  DATABASE_URL = { secret = { value_from = %q } }
`, prodDB), container: `{
    name        = "orders-api"
    image       = "registry.example.com/orders-api:2.3.0"
    environment = concat(%[1]s.ecs_environment, [{ name = "WEBHOOK_KEYS", value = "hunter2" }])
    secrets     = %[1]s.ecs_secrets
  }`, want: [][2]string{{"secret_as_value", "WEBHOOK_KEYS: is secret, but the container sets it to a plain value"}}},
	{name: "hand_value", args: fmt.Sprintf(ecsGoodArgs, prodDB, hooksSSM), container: `{
    name        = "orders-api"
    image       = "registry.example.com/orders-api:2.3.0"
    environment = concat(%[1]s.ecs_environment, [{ name = "REQUEST_TIMEOUT", value = "10m" }])
    secrets     = %[1]s.ecs_secrets
  }`, want: [][2]string{{"invalid_value", "REQUEST_TIMEOUT: 10m is above max 5m"}}},
	{name: "files", args: fmt.Sprintf(ecsGoodArgs, prodDB, hooksSSM), container: `{
    name             = "orders-api"
    image            = "registry.example.com/orders-api:2.3.0"
    environment      = %[1]s.ecs_environment
    secrets          = %[1]s.ecs_secrets
    environmentFiles = [{ type = "s3", value = "arn:aws:s3:::bucket/orders.env" }]
  }`, want: [][2]string{{"env_from_unchecked", "the container takes environmentFiles"}}},
	{name: "unwired", args: fmt.Sprintf(ecsGoodArgs, prodDB, hooksSSM), container: `{
    name        = "orders-api"
    image       = "registry.example.com/orders-api:2.3.0"
    environment = [for e in %[1]s.ecs_environment : e if e.name != "PORT"]
    secrets     = %[1]s.ecs_secrets
  }`, want: [][2]string{{"unbound_container", "PORT: module.unwired renders it, but the container does not set it"}}},
	{name: "other_account", args: fmt.Sprintf(ecsGoodArgs, otherDB, hooksSSM), container: ecsWired},
}

func TestLintTerraformECS(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", ordersContract, "-target", "ecs", "-out", filepath.Join(dir, "modules", "orders-config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: %s", stderr.String())
	}
	var root strings.Builder
	root.WriteString(`terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
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

resource "aws_secretsmanager_secret" "db" {
  name = "orders/prod/db"
}
`)
	for _, s := range ecsScenarios {
		fmt.Fprintf(&root, "\nmodule %q {\n  source = \"./modules/orders-config\"%s}\n", s.name, s.args)
		fmt.Fprintf(&root, "\nresource \"aws_ecs_task_definition\" %q {\n  family                = %q\n  container_definitions = jsonencode([%s])\n}\n",
			s.name, s.name, fmt.Sprintf(s.container, "module."+s.name))
	}
	// A secret created in the same apply: its ARN, and so the container
	// definitions, are unknown at plan.
	fmt.Fprintf(&root, "\nmodule \"unknown\" {\n  source = \"./modules/orders-config\"\n  DATABASE_URL = { secret = { value_from = aws_secretsmanager_secret.db.arn } }\n  WEBHOOK_KEYS = { secret = { value_from = %q } }\n}\n", "orders-webhook-keys")
	fmt.Fprintf(&root, "\nresource \"aws_ecs_task_definition\" \"unknown\" {\n  family                = \"unknown\"\n  container_definitions = jsonencode([%s])\n}\n", fmt.Sprintf(ecsWired, "module.unknown"))
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := tfInit(t, bin, dir); err != nil {
		t.Fatalf("init (needs the aws provider; see the comment above): %v\n%s", err, out)
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

	lintRun := func(extra ...string) []lint.Finding {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		run(append([]string{"lint", "terraform", planFile, "-dir", dir, "-format", "json"}, extra...), &stdout, &stderr)
		var got struct{ Findings []lint.Finding }
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("lint output: %v\n%s%s", err, stdout.String(), stderr.String())
		}
		return got.Findings
	}
	byModule := func(fs []lint.Finding) map[string][]lint.Finding {
		m := map[string][]lint.Finding{}
		for _, f := range fs {
			m[f.Module] = append(m[f.Module], f)
		}
		return m
	}
	findings := byModule(lintRun())
	for _, s := range ecsScenarios {
		t.Run(s.name, func(t *testing.T) {
			got := findings["module."+s.name]
			for _, w := range s.want {
				if !hasFinding(got, w[0], w[1]) {
					t.Errorf("no %s finding with %q; got:\n%s", w[0], w[1], describeFindings(got))
				}
			}
			for _, f := range got {
				if f.Level == lint.Notice {
					continue
				}
				expected := false
				for _, w := range s.want {
					expected = expected || f.Code == w[0]
				}
				if !expected {
					t.Errorf("unexpected %s %s: %s", f.Level, f.Code, f.Message)
				}
			}
		})
	}
	t.Run("unknown_container_definitions", func(t *testing.T) {
		if !hasFinding(findings["module.unknown"], "deferred", "aws_ecs_task_definition.unknown's container definitions are unknown at plan") {
			t.Errorf("no deferred notice:\n%s", describeFindings(findings["module.unknown"]))
		}
	})

	t.Run("policy", func(t *testing.T) {
		policy := filepath.Join(dir, "prod.cue")
		os.WriteFile(policy, []byte(`secretReferences: [string]: awsSecretsManager: {account: "123456789012", region: "us-east-1", name: =~"^orders/prod/"}
`), 0o644)
		got := byModule(lintRun("-policy", policy))
		// module.good wires WEBHOOK_KEYS to Parameter Store, which a
		// Secrets Manager policy does not allow.
		if !hasFinding(got["module.good"], "policy_mismatch", "WEBHOOK_KEYS: is set from awsSsmParameter, and the policy allows only awsSecretsManager here (policy prod.cue)") {
			t.Errorf("no kind mismatch:\n%s", describeFindings(got["module.good"]))
		}
		if hasFinding(got["module.good"], "policy_mismatch", "DATABASE_URL") {
			t.Errorf("DATABASE_URL meets the policy:\n%s", describeFindings(got["module.good"]))
		}

		os.WriteFile(policy, []byte(`secretReferences: [string]: awsSecretsManager?: {account: "123456789012"}
secretReferences: [string]: awsSsmParameter?: {account: "123456789012", name: =~"^/orders/prod/"}
`), 0o644)
		got = byModule(lintRun("-policy", policy))
		if f := got["module.good"]; hasLevel(f, lint.Error, "policy_mismatch") {
			t.Errorf("module.good meets the policy:\n%s", describeFindings(f))
		}
		if !hasFinding(got["module.other_account"], "policy_mismatch", `DATABASE_URL: awsSecretsManager.account "210987654321" is not allowed by policy (policy prod.cue)`) {
			t.Errorf("no account mismatch:\n%s", describeFindings(got["module.other_account"]))
		}
		// The account of a bare parameter name is unknown: deferred.
		if !hasFinding(got["module.unknown"], "policy_deferred", "WEBHOOK_KEYS: awsSsmParameter.account is unknown at plan, and the policy constrains it") {
			t.Errorf("no policy_deferred:\n%s", describeFindings(got["module.unknown"]))
		}
		if !hasFinding(got["module.unknown"], "policy_mismatch", `WEBHOOK_KEYS: awsSsmParameter.name "orders-webhook-keys" is not allowed by policy`) {
			t.Errorf("no name mismatch:\n%s", describeFindings(got["module.unknown"]))
		}
	})
}

func hasFinding(fs []lint.Finding, code, msg string) bool {
	for _, f := range fs {
		if f.Code == code && strings.Contains(f.Message, msg) {
			return true
		}
	}
	return false
}

func hasLevel(fs []lint.Finding, level, code string) bool {
	for _, f := range fs {
		if f.Level == level && f.Code == code {
			return true
		}
	}
	return false
}
