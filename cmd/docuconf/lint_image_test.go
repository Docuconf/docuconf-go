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

// contract_image_mismatch: a module generated from an OCI reference
// records the image, and the linter compares each bound container's image
// with it (spec/IAC.md section 4.7).
func TestLintImage(t *testing.T) {
	bin := terraformBinary(t)
	host, reg := testRegistry(t, true)
	img := pushImage(t, reg, "orders-api", "2.3.0", nil)
	other := pushImage(t, reg, "orders-api", "2.4.0", nil)
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders-api:2.3.0"); code != 0 {
		t.Fatal(stderr)
	}
	pinned := host + "/orders-api@" + string(img.Digest)

	dir := t.TempDir()
	if _, stderr, code := docuconf(t, "terraform", "-contract", "oci://"+host+"/orders-api:2.3.0", "-target", "ecs", "-out", filepath.Join(dir, "modules", "orders-config")); code != 0 {
		t.Fatal(stderr)
	}
	if _, stderr, code := docuconf(t, "terraform", "-contract", ordersContract, "-target", "ecs", "-out", filepath.Join(dir, "modules", "local-config")); code != 0 {
		t.Fatal(stderr)
	}
	images := map[string]string{
		"same":     fmt.Sprintf("%q", pinned),
		"mirrored": fmt.Sprintf("%q", "mirror.example.com/team/orders-api:2.3.0@"+string(img.Digest)),
		"other":    fmt.Sprintf("%q", host+"/orders-api@"+string(other.Digest)),
		"tag":      fmt.Sprintf("%q", host+"/orders-api:2.3.0"),
		"unknown":  "aws_ecr_repository.orders.repository_url",
		"local":    fmt.Sprintf("%q", pinned),
	}
	names := []string{"local", "mirrored", "other", "same", "tag", "unknown"}
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

resource "aws_ecr_repository" "orders" {
  name = "orders-api"
}
`)
	for _, name := range names {
		source := "./modules/orders-config"
		if name == "local" {
			source = "./modules/local-config"
		}
		fmt.Fprintf(&root, "\nmodule %q {\n  source = %q\n  DATABASE_URL = { secret = { value_from = %q } }\n  WEBHOOK_KEYS = { secret = { value_from = %q } }\n}\n", name, source, prodDB, hooksSSM)
		fmt.Fprintf(&root, "\nresource \"aws_ecs_task_definition\" %q {\n  family                = %q\n  container_definitions = jsonencode([{\n    name        = \"orders-api\"\n    image       = %s\n    environment = module.%s.ecs_environment\n    secrets     = module.%s.ecs_secrets\n  }])\n}\n", name, name, images[name], name, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o644); err != nil {
		t.Fatal(err)
	}
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
		if f.Code == "image_unverified" || f.Code == "contract_image_mismatch" || f.Code == "stale_module" {
			byModule[f.Module] = append(byModule[f.Module], f)
		}
	}
	want := map[string][2]string{
		"other":   {"contract_image_mismatch", "runs " + host + "/orders-api@" + string(other.Digest) + ", but module.other's contract describes " + pinned + "; regenerate the module from the image the container runs (docuconf terraform -contract oci://" + host + "/orders-api@" + string(other.Digest) + "), or deploy that image"},
		"tag":     {"image_unverified", "runs " + host + "/orders-api:2.3.0, which is not pinned by digest, so the linter cannot check that it is " + pinned},
		"unknown": {"image_unverified", "aws_ecs_task_definition.unknown: the containers are unknown, so the linter cannot check that the app runs " + pinned},
		"local":   {"image_unverified", "the module records no image digest, so the linter cannot check that the bound container runs the image the contract describes; generate it from the image (-contract oci://IMAGE) to check"},
	}
	for _, name := range names {
		fs := byModule["module."+name]
		w, ok := want[name]
		if !ok {
			if len(fs) != 0 {
				t.Errorf("%s: want no image findings, got:\n%s", name, describeFindings(fs))
			}
			continue
		}
		if len(fs) != 1 || !hasFinding(fs, w[0], w[1]) {
			t.Errorf("%s: want one %s with %q, got:\n%s", name, w[0], w[1], describeFindings(fs))
		}
	}
	// A mismatch is an error: lint fails.
	if code := run([]string{"lint", "terraform", planFile, "-dir", dir}, &stdout, &stderr); code != 1 {
		t.Errorf("lint: exit %d, want 1", code)
	}
}

// The same rule for a Pulumi component generated from an OCI reference,
// on a Lambda function's imageUri.
func TestLintPulumiImage(t *testing.T) {
	host, reg := testRegistry(t, true)
	img := pushImage(t, reg, "orders-api", "2.3.0", nil)
	other := pushImage(t, reg, "orders-api", "2.4.0", nil)
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders-api:2.3.0"); code != 0 {
		t.Fatal(stderr)
	}
	pinned := host + "/orders-api@" + string(img.Digest)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runPulumi([]string{"-contract", "oci://" + pinned, "-lang", "typescript", "-target", "lambda", "-out", filepath.Join(dir, "orders-config")}, &stdout, &stderr); err != nil {
		t.Fatalf("docuconf pulumi: %v\n%s", err, stderr.String())
	}
	const db = "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/db-AbCdEf"
	args := map[string]any{
		"DATABASE_URL": map[string]any{"injected": map[string]any{"provider": "aws-parameters-and-secrets", "ref": db}},
		"WEBHOOK_KEYS": map[string]any{"injected": map[string]any{"provider": "aws-parameters-and-secrets", "ref": db}},
	}
	fn := func(image string) []step {
		return []step{{"aws:lambda/function:Function", "orders", map[string]any{"name": "orders-api", "imageUri": image,
			"environment": map[string]any{"variables": map[string]any{"DATABASE_URL": db, "WEBHOOK_KEYS": db}}}}}
	}
	imageFindings := func(fs []lint.Finding) []lint.Finding {
		var out []lint.Finding
		for _, f := range fs {
			if f.Code == "image_unverified" || f.Code == "contract_image_mismatch" || f.Code == "stale_module" {
				out = append(out, f)
			}
		}
		return out
	}
	if fs, _ := lintPreview(t, dir, args, fn(pinned)); len(imageFindings(fs)) != 0 {
		t.Errorf("same image: got:\n%s", describeFindings(imageFindings(fs)))
	}
	fs, err := lintPreview(t, dir, args, fn(host+"/orders-api@"+string(other.Digest)))
	if err != errProblems || !hasFinding(fs, "contract_image_mismatch", "but orders's contract describes "+pinned+"; regenerate the component from the image the container runs (docuconf pulumi -contract oci://") {
		t.Errorf("other image: err %v, got:\n%s", err, describeFindings(fs))
	}
	if fs, _ := lintPreview(t, dir, args, fn("")); !hasFinding(fs, "image_unverified", "the image is unknown at preview, or it is a .zip function, which has none") {
		t.Errorf("zip function: got:\n%s", describeFindings(imageFindings(fs)))
	}
}

// The policy pack checks the image too, when it was generated from an OCI
// reference.
func TestPolicyImage(t *testing.T) {
	modules := nodeModules(t)
	host, reg := testRegistry(t, true)
	img := pushImage(t, reg, "orders-api", "2.3.0", nil)
	other := pushImage(t, reg, "orders-api", "2.4.0", nil)
	if _, stderr, code := docuconf(t, "push", "-contract", ordersContract, host+"/orders-api:2.3.0"); code != 0 {
		t.Fatal(stderr)
	}
	pinned := host + "/orders-api@" + string(img.Digest)
	pack := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runPulumi([]string{"policy", "-contract", "oci://" + pinned, "-target", "lambda", "-out", pack}, &stdout, &stderr); err != nil {
		t.Fatalf("docuconf pulumi policy: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(read(t, filepath.Join(pack, "docuconf.json")), `"image": "`+pinned+`"`) {
		t.Errorf("the pack's docuconf.json does not record the image")
	}
	if err := os.Symlink(modules, filepath.Join(pack, "node_modules")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pack, "harness.ts"), policyHarness)
	comp := pulumiProgram(t, "typescript", "lambda")
	const db = "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/db-AbCdEf"
	args := map[string]any{
		"DATABASE_URL": map[string]any{"injected": map[string]any{"provider": "aws-parameters-and-secrets", "ref": db}},
		"WEBHOOK_KEYS": map[string]any{"injected": map[string]any{"provider": "aws-parameters-and-secrets", "ref": db}},
	}
	fn := func(image string) []step {
		in := map[string]any{"environment": map[string]any{"variables": map[string]any{"DATABASE_URL": db, "WEBHOOK_KEYS": db}}}
		if image != "" {
			in["imageUri"] = image
		}
		return []step{{"aws:lambda/function:Function", "orders", in}}
	}
	got := runPolicy(t, pack, map[string]policyCase{
		"same":  {Target: "lambda", Resources: stackOf(t, comp, args, fn(pinned))},
		"other": {Target: "lambda", Resources: stackOf(t, comp, args, fn(host+"/orders-api@"+string(other.Digest)))},
		"tag":   {Target: "lambda", Resources: stackOf(t, comp, args, fn(host+"/orders-api:2.3.0"))},
		"zip":   {Target: "lambda", Resources: stackOf(t, comp, args, fn(""))},
	})
	for _, name := range []string{"same", "tag", "zip"} {
		if len(got[name]) != 0 {
			t.Errorf("%s: want nothing, got %v", name, got[name])
		}
	}
	if fs := got["other"]; len(fs) != 1 || fs[0].Code != "contract_image_mismatch" || !strings.Contains(fs[0].Message, "but orders's contract describes "+pinned) {
		t.Errorf("other: got %v", fs)
	}
}
