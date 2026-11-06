package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/lint"
)

// The policy pack's tests generate a pack, run its checks (policy.ts) on
// the stack resources of each docuconf lint pulumi case, as CrossGuard
// gives them, and check the pack finds what the linter finds in the
// preview. A value Pulumi does not know at preview becomes a property
// that throws when read, as CrossGuard's does.

const policyHarness = `import { readFileSync } from "node:fs";
import { contract, digest } from "./contract";
import { checkStack, type Target } from "./policy";

const UNKNOWN = "04da6b54-80e4-46f7-96ec-b56ff0331ba9";

// CrossGuard's props throw when an unknown value is read.
function props(x: unknown): unknown {
  if (Array.isArray(x)) {
    const out: unknown[] = [];
    x.forEach((v, i) => define(out, String(i), v));
    return out;
  }
  if (typeof x === "object" && x !== null) {
    const out = {};
    for (const [k, v] of Object.entries(x)) define(out, k, v);
    return out;
  }
  return x;
}
function define(o: object, k: string, v: unknown) {
  if (v === UNKNOWN) {
    Object.defineProperty(o, k, { enumerable: true, get: () => { throw new Error("unknown during preview"); } });
  } else {
    Object.defineProperty(o, k, { enumerable: true, value: props(v), writable: true });
  }
}

const cases = JSON.parse(readFileSync(process.argv[2]!, "utf8"));
const out: Record<string, unknown> = {};
for (const [name, c] of Object.entries<any>(cases)) {
  const resources = c.resources.map((r: any) => ({ ...r, props: props(r.props) }));
  out[name] = checkStack(contract, digest, c.target as Target, resources, c.settings);
}
console.log(JSON.stringify(out));
`

type policyCase struct {
	Target    string           `json:"target"`
	Resources []map[string]any `json:"resources"`
	Settings  map[string]any   `json:"settings"`
}

// policyPack generates the orders pack for a target into a directory with
// the test modules, and returns it.
func policyPack(t *testing.T, target string) string {
	t.Helper()
	modules := nodeModules(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runPulumi([]string{"policy", "-contract", ordersContract, "-target", target, "-out", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("docuconf pulumi policy: %v\n%s", err, stderr.String())
	}
	if err := os.Symlink(modules, filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "harness.ts"), policyHarness)
	return dir
}

func runPolicy(t *testing.T, dir string, cases map[string]policyCase) map[string][]lint.Finding {
	t.Helper()
	writeJSON(t, filepath.Join(dir, "cases.json"), cases)
	cmd := exec.Command(filepath.Join(dir, "node_modules", ".bin", "tsx"), "harness.ts", "cases.json")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("policy harness: %v\n%s", err, stderr.String())
	}
	var out map[string][]lint.Finding
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("policy harness output: %v\n%s", err, b)
	}
	return out
}

// stackOf is a lint case's program as CrossGuard sees the stack: the
// component's registered outputs, and the workloads' inputs.
func stackOf(t *testing.T, comp string, args any, steps []step) []map[string]any {
	res := []map[string]any{{
		"type": "docuconf:orders-api:Config", "name": "orders", "urn": "urn:pulumi:ci::orders::docuconf:orders-api:Config::orders",
		"props": map[string]any{"contract": map[string]any{"name": "orders-api", "digest": componentDigest(t, comp)}, "arguments": args},
	}}
	for _, s := range steps {
		res = append(res, map[string]any{"type": s.typ, "name": s.name, "urn": "urn:pulumi:ci::orders::" + s.typ + "::" + s.name, "props": s.inputs})
	}
	return res
}

// comparable is a finding as both report it. The pack names its own
// settings where the linter names its flags, and invalid_value carries
// the SDK's message where the linter carries vet's.
func comparable(f lint.Finding) string {
	m := f.Message
	if f.Code == "invalid_value" {
		m, _, _ = strings.Cut(m, ":")
	}
	for _, r := range [][2]string{
		{"docuconf lint pulumi -unmanaged-env", "the policy pack's unmanagedEnv setting"},
		{"the linter cannot see before apply", "the policy pack cannot see before the update"},
		{"docuconf lint pulumi -allow-env-from", "the policy pack's allowEnvFrom setting"},
		{"no workload in the preview", "no workload in the stack"},
	} {
		m = strings.ReplaceAll(m, r[0], r[1])
	}
	return f.Level + " " + f.Code + ": " + m
}

func comparables(fs []lint.Finding, pack bool) []string {
	var out []string
	for _, f := range fs {
		if f.Level == lint.Notice || (!pack && f.Code == "env_from_unchecked" && f.Level == lint.Warning) {
			// The pack reports what is allowed as nothing, not a warning.
			continue
		}
		out = append(out, comparable(f))
	}
	sort.Strings(out)
	return out
}

func TestPulumiPolicyMatchesLint(t *testing.T) {
	comp := pulumiProgram(t, "typescript", "kubernetes")
	pack := policyPack(t, "kubernetes")
	cases := map[string]policyCase{}
	want := map[string][]string{}
	for _, tc := range pulumiCases() {
		switch tc.name {
		case "secret_containers", "invalid_argument":
			// A preview hides a container list that holds a Pulumi
			// secret; CrossGuard gives the pack the plaintext, so the
			// pack checks what the linter cannot.
			continue
		}
		settings := map[string]any{}
		for i := 0; i < len(tc.flags); i++ {
			switch tc.flags[i] {
			case "-unmanaged-env":
				settings["unmanagedEnv"] = strings.Split(tc.flags[i+1], ",")
				i++
			case "-allow-env-from":
				settings["allowEnvFrom"] = true
			}
		}
		fs, _ := lintPreview(t, comp, tc.args, tc.steps, tc.flags...)
		want[tc.name] = comparables(fs, false)
		cases[tc.name] = policyCase{Target: "kubernetes", Resources: stackOf(t, comp, tc.args, tc.steps), Settings: settings}
	}
	got := runPolicy(t, pack, cases)
	for name := range cases {
		g := comparables(got[name], true)
		if strings.Join(g, "\n") != strings.Join(want[name], "\n") {
			t.Errorf("%s: the pack and the linter differ\npack:\n  %s\nlinter:\n  %s", name, strings.Join(g, "\n  "), strings.Join(want[name], "\n  "))
		}
	}
	// The pack's own words.
	for name, msg := range map[string]string{
		"unlisted":        "list it in the policy pack's unmanagedEnv setting",
		"invalid_by_hand": "WORKER_COUNT: must be at most 64 (got 100)",
	} {
		found := false
		for _, f := range got[name] {
			found = found || strings.Contains(f.Message, msg)
		}
		if !found {
			t.Errorf("%s: want %q, got %v", name, msg, got[name])
		}
	}
}

func TestPulumiPolicy(t *testing.T) {
	comp := pulumiProgram(t, "typescript", "kubernetes")
	pack := policyPack(t, "kubernetes")
	clean := []step{{"kubernetes:apps/v1:Deployment", "orders-api", deployment(ordersEnv(), discountsMount(), discountsVolume(), nil, nil)}}
	stack := func(digest string, args any, steps []step) []map[string]any {
		s := stackOf(t, comp, args, steps)
		s[0]["props"].(map[string]any)["contract"].(map[string]any)["digest"] = digest
		return s
	}
	unknownEnv := deployment(withEnv(ordersEnv(), map[string]any{"name": "PORTT", "value": unknown}), discountsMount(), discountsVolume(), nil, nil)
	got := runPolicy(t, pack, map[string]policyCase{
		"stale": {Target: "kubernetes", Resources: stack("sha256:0000", ordersArgs(), clean)},
		// Another target's pack ignores the component.
		"other_target": {Target: "kubernetes", Resources: func() []map[string]any {
			s := stackOf(t, comp, ordersArgs(), nil)
			s[0]["props"].(map[string]any)["contract"].(map[string]any)["target"] = "ecs"
			return s
		}()},
		// An unknown value hides the workload: the component checks it later.
		"unknown_workload": {Target: "kubernetes", Resources: stackOf(t, comp, ordersArgs(), []step{{"kubernetes:apps/v1:Deployment", "orders-api", unknownEnv}})},
		"unknown_argument": {Target: "kubernetes", Resources: stackOf(t, comp, withArgs(map[string]any{"LOG_LEVEL": map[string]any{"value": unknown}}), clean)},
		"python_names": {Target: "kubernetes", Resources: func() []map[string]any {
			s := stackOf(t, comp, ordersArgs(), []step{{"kubernetes:apps/v1:Deployment", "orders-api", deployment(ordersEnv()[1:], discountsMount(), discountsVolume(), nil, nil)}})
			s[0]["props"].(map[string]any)["contract"].(map[string]any)["language"] = "python"
			return s
		}()},
		"go_names": {Target: "kubernetes", Resources: func() []map[string]any {
			s := stackOf(t, comp, ordersArgs(), []step{{"kubernetes:apps/v1:Deployment", "orders-api", deployment(ordersEnv()[1:], discountsMount(), discountsVolume(), nil, nil)}})
			s[0]["props"].(map[string]any)["contract"].(map[string]any)["language"] = "go"
			return s
		}()},
	})
	expect := func(name, code, msg string) {
		t.Helper()
		for _, f := range got[name] {
			if f.Code == code && strings.Contains(f.Message, msg) {
				return
			}
		}
		t.Errorf("%s: want %s %q, got %v", name, code, msg, got[name])
	}
	none := func(name string) {
		t.Helper()
		for _, f := range got[name] {
			if f.Level == "error" {
				t.Errorf("%s: unexpected %s: %s", name, f.Code, f.Message)
			}
		}
	}
	expect("stale", "stale_module", "orders was generated from another version of the orders-api contract than this policy pack")
	if len(got["other_target"]) != 0 {
		t.Errorf("other_target: want nothing, got %v", got["other_target"])
	}
	none("unknown_workload")
	none("unknown_argument")
	expect("go_names", "unbound_container", "take the container's env from orders.KubernetesEnv")
	expect("python_names", "unbound_container", "take the container's env from orders.kubernetes_env")

	t.Run("ecs", func(t *testing.T) {
		comp := pulumiProgram(t, "typescript", "ecs")
		pack := policyPack(t, "ecs")
		args := map[string]any{
			"LOG_LEVEL":    map[string]any{"value": "warn"},
			"DATABASE_URL": map[string]any{"secret": map[string]any{"valueFrom": "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf"}},
		}
		defs := func(env []any) []step {
			b, _ := json.Marshal([]any{map[string]any{"name": "orders-api", "environment": env,
				"secrets": []any{map[string]any{"name": "DATABASE_URL", "valueFrom": "arn:aws:secretsmanager:us-east-1:123456789012:secret:orders/prod/db-AbCdEf"}}}})
			return []step{{"aws:ecs/taskDefinition:TaskDefinition", "orders", map[string]any{"containerDefinitions": string(b)}}}
		}
		got := runPolicy(t, pack, map[string]policyCase{
			"clean":   {Target: "ecs", Resources: stackOf(t, comp, args, defs([]any{map[string]any{"name": "LOG_LEVEL", "value": "warn"}}))},
			"typo":    {Target: "ecs", Resources: stackOf(t, comp, args, defs([]any{map[string]any{"name": "LOG_LEVEL", "value": "warn"}, map[string]any{"name": "LOGLEVEL", "value": "x"}}))},
			"unknown": {Target: "ecs", Resources: stackOf(t, comp, args, []step{{"aws:ecs/taskDefinition:TaskDefinition", "orders", map[string]any{"containerDefinitions": unknown}}})},
		})
		for _, n := range []string{"clean", "unknown"} {
			if len(got[n]) != 0 {
				t.Errorf("%s: want nothing, got %v", n, got[n])
			}
		}
		if len(got["typo"]) != 1 || got["typo"][0].Code != "undeclared_input" || !strings.Contains(got["typo"][0].Message, "did you mean LOG_LEVEL?") {
			t.Errorf("typo: got %v", got["typo"])
		}
	})

	t.Run("cloudrun", func(t *testing.T) {
		comp := pulumiProgram(t, "typescript", "cloudrun")
		pack := policyPack(t, "cloudrun")
		args := map[string]any{
			"LOG_LEVEL":    map[string]any{"value": "warn"},
			"DATABASE_URL": map[string]any{"secret": map[string]any{"secret": "orders-db"}},
			"discounts":    map[string]any{"secret": map[string]any{"secret": "orders-discounts"}},
		}
		service := func(mounts []any) []step {
			return []step{{"gcp:cloudrunv2/service:Service", "orders", map[string]any{"template": map[string]any{
				"containers": []any{map[string]any{"envs": []any{
					map[string]any{"name": "LOG_LEVEL", "value": "warn"},
					map[string]any{"name": "DATABASE_URL", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "orders-db", "version": "latest"}}},
				}, "volumeMounts": mounts}},
				"volumes": []any{map[string]any{"name": "dc-discounts"}},
			}}}}
		}
		got := runPolicy(t, pack, map[string]policyCase{
			"clean":    {Target: "cloudrun", Resources: stackOf(t, comp, args, service([]any{map[string]any{"name": "dc-discounts", "mountPath": "/etc/orders/discounts"}}))},
			"no_mount": {Target: "cloudrun", Resources: stackOf(t, comp, args, service([]any{}))},
		})
		if len(got["clean"]) != 0 {
			t.Errorf("clean: want nothing, got %v", got["clean"])
		}
		if len(got["no_mount"]) != 1 || !strings.Contains(got["no_mount"][0].Message, "take its mounts from orders.cloudrunVolumeMounts") {
			t.Errorf("no_mount: got %v", got["no_mount"])
		}
	})
}

// The pack type-checks, and docuconf pulumi policy -check passes on what it
// wrote.
func TestPulumiPolicyGenerated(t *testing.T) {
	for _, target := range []string{"kubernetes", "ecs", "cloudrun"} {
		pack := policyPack(t, target)
		cmd := exec.Command(filepath.Join(pack, "node_modules", ".bin", "tsc"), "--noEmit", "-p", ".")
		cmd.Dir = pack
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: tsc: %v\n%s", target, err, out)
		}
		fresh := t.TempDir()
		var stdout, stderr bytes.Buffer
		if err := runPulumi([]string{"policy", "-contract", ordersContract, "-target", target, "-out", fresh}, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if err := runPulumi([]string{"policy", "-contract", ordersContract, "-target", target, "-out", fresh, "-check"}, &stdout, &stderr); err != nil {
			t.Errorf("%s: -check: %v\n%s", target, err, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if err := runPulumi([]string{"policy", "-contract", ordersContract, "-target", "lambda", "-out", t.TempDir()}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), `-target "lambda" is not supported by docuconf pulumi policy yet`) {
		t.Errorf("want an unsupported target error, got %v", err)
	}
}
