package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/lint"
)

// The lint pulumi tests write a preview's JSON, in the shape pulumi
// preview --json gives it (the examples' CI jobs lint real previews), for
// a program with one orders component and one workload, and check what
// docuconf lint pulumi finds. The component is generated into the
// program's directory, as docuconf pulumi writes it.

const (
	unknown = "04da6b54-80e4-46f7-96ec-b56ff0331ba9"
	secret  = "[secret]"
)

// ordersArgs are the arguments deploy/pulumi sets, as the component
// records them.
func ordersArgs() map[string]any {
	return map[string]any{
		"ALLOWED_ORIGINS": map[string]any{"value": []any{"https://shop.example.com"}},
		"DATABASE_URL":    map[string]any{"secretKeyRef": map[string]any{"name": "orders-db", "key": "url"}},
		"WEBHOOK_KEYS":    map[string]any{"secretKeyRef": map[string]any{"name": "orders-webhooks", "key": "keys"}},
		"LOG_LEVEL":       map[string]any{"value": "warn"},
		"WORKER_COUNT":    map[string]any{"value": 8},
		"discounts":       map[string]any{"inline": map[string]any{"codes": map[string]any{"WELCOME10": 10}}},
	}
}

// ordersEnv is what the component renders for ordersArgs.
func ordersEnv() []any {
	return []any{
		map[string]any{"name": "ALLOWED_ORIGINS", "value": "https://shop.example.com"},
		map[string]any{"name": "DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "orders-db", "key": "url"}}},
		map[string]any{"name": "LOG_LEVEL", "value": "warn"},
		map[string]any{"name": "WEBHOOK_KEYS", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "orders-webhooks", "key": "keys"}}},
		map[string]any{"name": "WORKER_COUNT", "value": "8"},
	}
}

func discountsMount() []any {
	return []any{map[string]any{"name": "dc-discounts", "mountPath": "/etc/orders/discounts", "readOnly": true}}
}

func discountsVolume() []any {
	return []any{map[string]any{"name": "dc-discounts", "configMap": map[string]any{"name": "orders-api-discounts-abc"}}}
}

// deployment is a Deployment's planned inputs.
func deployment(env, mounts, volumes any, podMeta map[string]any, extra map[string]any) map[string]any {
	c := map[string]any{"name": "orders-api", "image": "registry.example.com/orders-api:2.3.0", "env": env, "volumeMounts": mounts}
	for k, v := range extra {
		c[k] = v
	}
	if podMeta == nil {
		podMeta = map[string]any{"labels": map[string]any{"app": "orders-api"}}
	}
	return map[string]any{
		"metadata": map[string]any{"name": "orders-api"},
		"spec": map[string]any{"template": map[string]any{
			"metadata": podMeta,
			"spec":     map[string]any{"containers": []any{c}, "volumes": volumes},
		}},
	}
}

type step struct {
	typ    string
	name   string
	inputs map[string]any
}

// pulumiProgram generates the orders component into a program directory
// and returns it.
func pulumiProgram(t *testing.T, lang, target string) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "orders-config")
	if lang == "python" {
		out = filepath.Join(dir, "orders_config") // a Python package's name
	}
	var stdout, stderr bytes.Buffer
	if err := runPulumi([]string{"-contract", ordersContract, "-lang", lang, "-target", target, "-out", out}, &stdout, &stderr); err != nil {
		t.Fatalf("docuconf pulumi: %v\n%s", err, stderr.String())
	}
	return dir
}

func componentDigest(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "orders-config", "docuconf.json"))
	if os.IsNotExist(err) {
		b, err = os.ReadFile(filepath.Join(dir, "orders_config", "docuconf.json"))
	}
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ContractDigest string `json:"contractDigest"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m.ContractDigest
}

// lintPreview writes a preview with the component (args) and the steps,
// and runs docuconf lint pulumi on it.
func lintPreview(t *testing.T, dir string, args any, steps []step, flags ...string) ([]lint.Finding, error) {
	t.Helper()
	all := []any{
		map[string]any{"op": "create", "urn": "urn:pulumi:ci::orders::pulumi:pulumi:Stack::orders-ci",
			"newState": map[string]any{"type": "pulumi:pulumi:Stack", "custom": false}},
		map[string]any{"op": "create", "urn": "urn:pulumi:ci::orders::docuconf:orders-api:Config::orders",
			"newState": map[string]any{"type": "docuconf:orders-api:Config", "custom": false, "inputs": map[string]any{
				"contract":  map[string]any{"name": "orders-api", "digest": componentDigest(t, dir)},
				"arguments": args,
			}}},
	}
	for _, s := range steps {
		all = append(all, map[string]any{"op": "create", "urn": "urn:pulumi:ci::orders::" + s.typ + "::" + s.name,
			"newState": map[string]any{"type": s.typ, "custom": true, "inputs": s.inputs}})
	}
	b, _ := json.Marshal(map[string]any{"steps": all})
	file := filepath.Join(t.TempDir(), "preview.json")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := runLint(append([]string{"pulumi", file, "-dir", dir, "-format", "json"}, flags...), &stdout, &stderr)
	if err != nil && err != errProblems {
		t.Fatalf("docuconf lint pulumi: %v\n%s", err, stderr.String())
	}
	var out struct {
		Findings []lint.Finding `json:"findings"`
	}
	if jerr := json.Unmarshal(stdout.Bytes(), &out); jerr != nil {
		t.Fatalf("lint output: %v\n%s", jerr, stdout.String())
	}
	return out.Findings, err
}

func withArgs(change map[string]any) map[string]any {
	a := ordersArgs()
	for k, v := range change {
		if v == nil {
			delete(a, k)
		} else {
			a[k] = v
		}
	}
	return a
}

func withEnv(env []any, extra ...any) []any {
	return append(append([]any{}, env...), extra...)
}

// pulumiCase is a program's component arguments and workloads, and what
// docuconf lint pulumi must find in its preview. The policy pack's tests
// check the pack finds the same.
type pulumiCase struct {
	name   string
	args   any
	steps  []step
	flags  []string
	code   string // a finding that must be there ("" for none but notices)
	msg    string
	absent string // a code that must not be there
}

func pulumiCases() []pulumiCase {
	deploy := func(in map[string]any) []step {
		return []step{{"kubernetes:apps/v1:Deployment", "orders-api", in}}
	}
	clean := deployment(ordersEnv(), discountsMount(), discountsVolume(), nil, nil)
	vault := map[string]any{"injected": map[string]any{
		"provider": "bank-vaults", "ref": "vault:secret/data/orders#port",
		"podAnnotations": map[string]any{"vault.security.banzaicloud.io/vault-addr": "https://vault:8200"},
	}}
	vaultEnv := withEnv(ordersEnv(), map[string]any{"name": "PORT", "value": "vault:secret/data/orders#port"})
	return []pulumiCase{
		{name: "clean", args: ordersArgs(), steps: deploy(clean)},
		{name: "typo", args: ordersArgs(), steps: deploy(deployment(withEnv(ordersEnv(), map[string]any{"name": "PORTT", "value": "8080"}), discountsMount(), discountsVolume(), nil, nil)),
			code: "undeclared_input", msg: "PORTT: is not declared in the contract, and the component did not render it; did you mean PORT?"},
		{name: "unmanaged", args: ordersArgs(), flags: []string{"-unmanaged-env", "OTEL_SERVICE_NAME"},
			steps: deploy(deployment(withEnv(ordersEnv(), map[string]any{"name": "OTEL_SERVICE_NAME", "value": "orders"}), discountsMount(), discountsVolume(), nil, nil))},
		{name: "unlisted", args: ordersArgs(),
			steps: deploy(deployment(withEnv(ordersEnv(), map[string]any{"name": "OTEL_SERVICE_NAME", "value": "orders"}), discountsMount(), discountsVolume(), nil, nil)),
			code:  "undeclared_input", msg: "list it in docuconf lint pulumi -unmanaged-env"},
		{name: "duplicate", args: ordersArgs(), steps: deploy(deployment(withEnv(ordersEnv(), map[string]any{"name": "LOG_LEVEL", "value": "debug"}), discountsMount(), discountsVolume(), nil, nil)),
			code: "duplicate_env", msg: "LOG_LEVEL: is set 2 times"},
		{name: "secret_by_hand", args: withArgs(map[string]any{"DATABASE_URL": nil}),
			steps: deploy(deployment(withEnv(ordersEnv()[2:], ordersEnv()[0], map[string]any{"name": "DATABASE_URL", "value": "postgres://u:p@db/orders"}), discountsMount(), discountsVolume(), nil, nil)),
			code:  "secret_as_value", msg: "DATABASE_URL: is secret, but the container sets it to a plain value; take it from the component"},
		{name: "invalid_by_hand", args: withArgs(map[string]any{"WORKER_COUNT": nil}),
			steps: deploy(deployment(withEnv(ordersEnv()[:4], map[string]any{"name": "WORKER_COUNT", "value": "100"}), discountsMount(), discountsVolume(), nil, nil)),
			code:  "invalid_value", msg: "WORKER_COUNT"},
		{name: "unbound", args: ordersArgs(), steps: deploy(deployment(ordersEnv()[1:], discountsMount(), discountsVolume(), nil, nil)),
			code: "unbound_container", msg: "ALLOWED_ORIGINS: orders renders it, but the container does not set it; take the container's env from orders.kubernetesEnv"},
		// The injector's reference is the env value: the recorded argument
		// says it is injected, so it is not read as a literal int.
		{name: "injected", args: withArgs(map[string]any{"PORT": vault}),
			steps:  deploy(deployment(vaultEnv, discountsMount(), discountsVolume(), map[string]any{"annotations": map[string]any{"vault.security.banzaicloud.io/vault-addr": "https://vault:8200"}}, nil)),
			absent: "invalid_value"},
		{name: "injector_annotation", args: withArgs(map[string]any{"PORT": vault}),
			steps: deploy(deployment(vaultEnv, discountsMount(), discountsVolume(), nil, nil)),
			code:  "pod_metadata_missing", msg: "the pod template lacks annotation vault.security.banzaicloud.io/vault-addr=https://vault:8200, which an injector needs; set its metadata from orders.podAnnotations"},
		{name: "env_from", args: ordersArgs(), steps: deploy(deployment(ordersEnv(), discountsMount(), discountsVolume(), nil, map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "everything"}}}})),
			code: "env_from_unchecked", msg: "the container takes envFrom a whole ConfigMap or Secret"},
		{name: "mount_path", args: ordersArgs(), steps: deploy(deployment(ordersEnv(), []any{map[string]any{"name": "dc-discounts", "mountPath": "/etc/discounts", "readOnly": true}}, discountsVolume(), nil, nil)),
			code: "mount_mismatch", msg: "discounts: volume dc-discounts is mounted at /etc/discounts, but the app reads it from /etc/orders/discounts"},
		{name: "sub_path", args: ordersArgs(), steps: deploy(deployment(ordersEnv(), []any{map[string]any{"name": "dc-discounts", "mountPath": "/etc/orders/discounts", "subPath": "discounts.yaml", "readOnly": true}}, discountsVolume(), nil, nil)),
			code: "mount_mismatch", msg: "mounted with subPath discounts.yaml"},
		{name: "no_volume", args: ordersArgs(), steps: deploy(deployment(ordersEnv(), discountsMount(), []any{}, nil, nil)),
			code: "mount_mismatch", msg: "take its volumes from orders.kubernetesVolumes"},
		// A Pulumi secret in the env hides the whole container list.
		{name: "secret_containers", args: withArgs(map[string]any{"LOG_LEVEL": map[string]any{"value": secret}}),
			steps: deploy(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": secret}}}}),
			code:  "deferred", msg: "containers are unknown or secret at preview, so the linter checks only orders's arguments; the values are checked during the update, by the component's checks", absent: "module_unused"},
		{name: "invalid_argument", args: withArgs(map[string]any{"WORKER_COUNT": map[string]any{"value": 100}}),
			steps: deploy(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": secret}}}}),
			code:  "invalid_value", msg: "WORKER_COUNT"},
		{name: "unknown_argument", args: withArgs(map[string]any{"DATABASE_URL": map[string]any{"secretKeyRef": map[string]any{"name": unknown, "key": "url"}}}),
			steps: deploy(deployment(withEnv(ordersEnv()[2:], ordersEnv()[0], map[string]any{"name": "DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": unknown, "key": "url"}}}), discountsMount(), discountsVolume(), nil, nil)),
			code:  "deferred", msg: "DATABASE_URL: is unknown at preview (from an Output known only during the update); it is checked during the update, by the component's checks, and at boot"},
		{name: "unused", args: ordersArgs(), code: "module_unused", msg: "no workload in the preview takes orders's outputs"},
	}
}

func TestLintPulumi(t *testing.T) {
	dir := pulumiProgram(t, "typescript", "kubernetes")
	cases := pulumiCases()
	deploy := func(in map[string]any) []step {
		return []step{{"kubernetes:apps/v1:Deployment", "orders-api", in}}
	}
	clean := deployment(ordersEnv(), discountsMount(), discountsVolume(), nil, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, _ := lintPreview(t, dir, tc.args, tc.steps, tc.flags...)
			if tc.code == "" {
				for _, f := range fs {
					if f.Level != lint.Notice {
						t.Errorf("unexpected finding: %s", textLine(f))
					}
				}
			} else if !hasFinding(fs, tc.code, tc.msg) {
				t.Errorf("want %s %q, got:\n%s", tc.code, tc.msg, describeFindings(fs))
			}
			if tc.absent != "" && hasFinding(fs, tc.absent, "") {
				t.Errorf("want no %s, got:\n%s", tc.absent, describeFindings(fs))
			}
		})
	}

	t.Run("allow_env_from", func(t *testing.T) {
		fs, _ := lintPreview(t, dir, ordersArgs(), deploy(deployment(ordersEnv(), discountsMount(), discountsVolume(), nil, map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "everything"}}}})), "-allow-env-from")
		if !hasLevel(fs, lint.Warning, "env_from_unchecked") || hasLevel(fs, lint.Error, "env_from_unchecked") {
			t.Errorf("want env_from_unchecked as a warning, got:\n%s", describeFindings(fs))
		}
	})

	t.Run("policy", func(t *testing.T) {
		policy := filepath.Join(t.TempDir(), "prod.cue")
		writeFile(t, policy, "DATABASE_URL: secretKeyRef: name: \"orders-db-prod\"\n")
		fs, _ := lintPreview(t, dir, ordersArgs(), deploy(clean), "-policy", policy)
		if !hasFinding(fs, "policy_mismatch", "DATABASE_URL") {
			t.Errorf("want policy_mismatch, got:\n%s", describeFindings(fs))
		}
	})

	t.Run("stale", func(t *testing.T) {
		stale := pulumiProgram(t, "typescript", "kubernetes")
		f := filepath.Join(stale, "orders-config", "index.ts")
		b, _ := os.ReadFile(f)
		writeFile(t, f, string(b)+"// edited\n")
		fs, _ := lintPreview(t, stale, ordersArgs(), deploy(clean))
		if !hasFinding(fs, "stale_module", "differs from what docuconf pulumi generates from its docuconf.json (index.ts)") {
			t.Errorf("want stale_module, got:\n%s", describeFindings(fs))
		}
	})

	t.Run("contract", func(t *testing.T) {
		fs, _ := lintPreview(t, dir, ordersArgs(), deploy(clean), "-contract", ordersContract)
		if hasLevel(fs, lint.Error, "stale_module") {
			t.Errorf("want no stale_module, got:\n%s", describeFindings(fs))
		}
	})

	t.Run("explain", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{"steps": []any{map[string]any{"op": "create", "urn": "urn:pulumi:ci::orders::docuconf:orders-api:Config::orders",
			"newState": map[string]any{"type": "docuconf:orders-api:Config", "inputs": map[string]any{
				"contract": map[string]any{"name": "orders-api", "digest": componentDigest(t, dir)}, "arguments": ordersArgs()}}}}})
		file := filepath.Join(t.TempDir(), "preview.json")
		writeFile(t, file, string(b))
		var stdout, stderr bytes.Buffer
		_ = runLint([]string{"pulumi", file, "-dir", dir, "-explain"}, &stdout, &stderr)
		for _, want := range []string{"AT PREVIEW", "DATABASE_URL     secretKeyRef  {\"key\":\"url\",\"name\":\"orders-db\"}", "WORKER_COUNT     value         literal 8"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("-explain lacks %q:\n%s", want, stdout.String())
			}
		}
	})

	t.Run("no_component", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{"steps": []any{map[string]any{"op": "create", "urn": "urn:pulumi:ci::orders::docuconf:orders-api:Config::orders",
			"newState": map[string]any{"type": "docuconf:orders-api:Config", "inputs": map[string]any{
				"contract": map[string]any{"name": "orders-api", "digest": "sha256:0000"}, "arguments": ordersArgs()}}}}})
		file := filepath.Join(t.TempDir(), "preview.json")
		writeFile(t, file, string(b))
		var stdout, stderr bytes.Buffer
		err := runLint([]string{"pulumi", file, "-dir", dir}, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "no component generated from its orders-api contract (digest sha256:0000) is under this directory") {
			t.Errorf("want a missing component error, got %v", err)
		}
	})
}

// The Go component's outputs are named in Go, and the ecs and cloudrun
// workloads are read from their own resources.
func TestLintPulumiTargets(t *testing.T) {
	t.Run("go", func(t *testing.T) {
		dir := pulumiProgram(t, "go", "kubernetes")
		fs, _ := lintPreview(t, dir, ordersArgs(), []step{{"kubernetes:apps/v1:Deployment", "orders-api", deployment(ordersEnv()[1:], discountsMount(), discountsVolume(), nil, nil)}})
		if !hasFinding(fs, "unbound_container", "take the container's env from orders.KubernetesEnv") {
			t.Errorf("want the Go output's name, got:\n%s", describeFindings(fs))
		}
	})

	t.Run("python", func(t *testing.T) {
		dir := pulumiProgram(t, "python", "kubernetes")
		fs, _ := lintPreview(t, dir, ordersArgs(), []step{{"kubernetes:apps/v1:Deployment", "orders-api", deployment(ordersEnv()[1:], discountsMount(), discountsVolume(), nil, nil)}})
		if !hasFinding(fs, "unbound_container", "take the container's env from orders.kubernetes_env") {
			t.Errorf("want the Python output's name, got:\n%s", describeFindings(fs))
		}
		if hasLevel(fs, lint.Error, "stale_module") {
			t.Errorf("the Python component is reported stale:\n%s", describeFindings(fs))
		}
	})

	ecsArgs := map[string]any{
		"ALLOWED_ORIGINS": map[string]any{"value": []any{"https://shop.example.com"}},
		"DATABASE_URL":    map[string]any{"secret": map[string]any{"valueFrom": "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf", "jsonKey": "url"}},
		"LOG_LEVEL":       map[string]any{"value": "warn"},
	}
	defs := func(env, secrets []any) string {
		b, _ := json.Marshal([]any{map[string]any{"name": "orders-api", "image": "registry.example.com/orders-api:2.3.0", "environment": env, "secrets": secrets}})
		return string(b)
	}
	ecsEnv := []any{map[string]any{"name": "ALLOWED_ORIGINS", "value": "https://shop.example.com"}, map[string]any{"name": "LOG_LEVEL", "value": "warn"}}
	ecsSecrets := []any{map[string]any{"name": "DATABASE_URL", "valueFrom": "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf:url::"}}
	t.Run("ecs", func(t *testing.T) {
		dir := pulumiProgram(t, "typescript", "ecs")
		task := func(s any) []step {
			return []step{{"aws:ecs/taskDefinition:TaskDefinition", "orders", map[string]any{"family": "orders-api", "containerDefinitions": s}}}
		}
		fs, _ := lintPreview(t, dir, ecsArgs, task(defs(ecsEnv, ecsSecrets)))
		for _, f := range fs {
			if f.Level != lint.Notice {
				t.Errorf("clean: unexpected finding: %s", textLine(f))
			}
		}
		fs, _ = lintPreview(t, dir, ecsArgs, task(defs(ecsEnv[1:], ecsSecrets)))
		if !hasFinding(fs, "unbound_container", "take the container's env from orders.ecsEnvironment and .ecsSecrets") {
			t.Errorf("unbound: got:\n%s", describeFindings(fs))
		}
		policy := filepath.Join(t.TempDir(), "prod.cue")
		writeFile(t, policy, "secretReferences: [string]: awsSecretsManager?: account: \"210987654321\"\n")
		fs, _ = lintPreview(t, dir, ecsArgs, task(defs(ecsEnv, ecsSecrets)), "-policy", policy)
		if !hasFinding(fs, "policy_mismatch", "123456789012") {
			t.Errorf("policy: got:\n%s", describeFindings(fs))
		}
		fs, _ = lintPreview(t, dir, ecsArgs, task(unknown))
		if !hasFinding(fs, "deferred", "containers are unknown or secret at preview") {
			t.Errorf("unknown definitions: got:\n%s", describeFindings(fs))
		}
	})

	t.Run("cloudrun", func(t *testing.T) {
		dir := pulumiProgram(t, "typescript", "cloudrun")
		args := map[string]any{
			"DATABASE_URL": map[string]any{"secret": map[string]any{"secret": "orders-db"}},
			"LOG_LEVEL":    map[string]any{"value": "warn"},
			"discounts":    map[string]any{"secret": map[string]any{"secret": "orders-discounts"}},
		}
		service := func(envs, mounts any) []step {
			return []step{{"gcp:cloudrunv2/service:Service", "orders", map[string]any{"template": map[string]any{
				"containers": []any{map[string]any{"image": "us-docker.pkg.dev/acme/orders:2.3.0", "envs": envs, "volumeMounts": mounts}},
				"volumes":    []any{map[string]any{"name": "dc-discounts", "secret": map[string]any{"secret": "orders-discounts"}}},
			}}}}
		}
		envs := []any{
			map[string]any{"name": "DATABASE_URL", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "orders-db", "version": "latest"}}},
			map[string]any{"name": "LOG_LEVEL", "value": "warn"},
		}
		mounts := []any{map[string]any{"name": "dc-discounts", "mountPath": "/etc/orders/discounts"}}
		fs, _ := lintPreview(t, dir, args, service(envs, mounts))
		for _, f := range fs {
			if f.Level != lint.Notice {
				t.Errorf("clean: unexpected finding: %s", textLine(f))
			}
		}
		fs, _ = lintPreview(t, dir, args, service(append(envs, map[string]any{"name": "LOG_LEVL", "value": "x"}), []any{}))
		if !hasFinding(fs, "undeclared_input", "LOG_LEVL: is not declared in the contract, and the component did not render it; did you mean LOG_LEVEL?") ||
			!hasFinding(fs, "mount_mismatch", "the container does not mount volume dc-discounts; take its mounts from orders.cloudrunVolumeMounts") {
			t.Errorf("typo and mount: got:\n%s", describeFindings(fs))
		}
	})

	t.Run("containerapps", func(t *testing.T) {
		const kv = "https://orders-kv.vault.azure.net/secrets/"
		args := map[string]any{
			"ALLOWED_ORIGINS": map[string]any{"value": []any{"https://shop.example.com/$(x)"}},
			"DATABASE_URL":    map[string]any{"secret": map[string]any{"keyVaultSecretId": kv + "orders-db"}},
			"LOG_LEVEL":       map[string]any{"value": "warn"},
			"discounts":       map[string]any{"secret": map[string]any{"keyVaultSecretId": kv + "orders-discounts"}},
		}
		app := func(env, mounts, secrets any) []step {
			return []step{{"azure-native:app:ContainerApp", "orders", map[string]any{
				"configuration": map[string]any{"secrets": secrets},
				"template": map[string]any{
					"containers": []any{map[string]any{"name": "orders-api", "image": "ordersacr.azurecr.io/orders-api:2.3.0", "env": env, "volumeMounts": mounts}},
					"volumes":    []any{map[string]any{"name": "dc-discounts", "storageType": "Secret", "secrets": []any{map[string]any{"secretRef": "dc-file-discounts", "path": "discounts.yaml"}}}},
				},
			}}}
		}
		secrets := []any{
			map[string]any{"name": "dc-database-url", "keyVaultUrl": kv + "orders-db", "identity": "System"},
			map[string]any{"name": "dc-file-discounts", "keyVaultUrl": kv + "orders-discounts", "identity": "System"},
		}
		env := []any{
			map[string]any{"name": "ALLOWED_ORIGINS", "value": "https://shop.example.com/$$(x)"},
			map[string]any{"name": "DATABASE_URL", "secretRef": "dc-database-url"},
			map[string]any{"name": "LOG_LEVEL", "value": "warn"},
		}
		mounts := []any{map[string]any{"volumeName": "dc-discounts", "mountPath": "/etc/orders/discounts"}}
		for _, lang := range []string{"typescript", "go"} {
			dir := pulumiProgram(t, lang, "containerapps")
			fs, _ := lintPreview(t, dir, args, app(env, mounts, secrets))
			for _, f := range fs {
				if f.Level != lint.Notice || f.Code == "deferred" {
					t.Errorf("%s clean: unexpected finding: %s", lang, textLine(f))
				}
			}
			out := map[string]string{"typescript": "orders.containerappVolumeMounts", "go": "orders.ContainerAppVolumeMounts"}[lang]
			fs, _ = lintPreview(t, dir, args, app(append(env, map[string]any{"name": "LOG_LEVL", "value": "x"}), []any{}, secrets))
			if !hasFinding(fs, "undeclared_input", "LOG_LEVL: is not declared in the contract, and the component did not render it; did you mean LOG_LEVEL?") ||
				!hasFinding(fs, "mount_mismatch", "the container does not mount volume dc-discounts; take its mounts from "+out) {
				t.Errorf("%s typo and mount: got:\n%s", lang, describeFindings(fs))
			}
		}
		dir := pulumiProgram(t, "typescript", "containerapps")
		policy := filepath.Join(t.TempDir(), "prod.cue")
		writeFile(t, policy, "secretReferences: [string]: azureKeyVault: vault: \"prod-kv.vault.azure.net\"\n")
		fs, _ := lintPreview(t, dir, args, app(env, mounts, secrets), "-policy", policy)
		if !hasFinding(fs, "policy_mismatch", `DATABASE_URL: azureKeyVault.vault "orders-kv.vault.azure.net" is not allowed by policy`) {
			t.Errorf("policy: got:\n%s", describeFindings(fs))
		}
		fs, _ = lintPreview(t, dir, args, app(env, mounts, unknown))
		if !hasFinding(fs, "deferred", "DATABASE_URL: is unknown at preview") {
			t.Errorf("hidden secrets: got:\n%s", describeFindings(fs))
		}
	})
}
