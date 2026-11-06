package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The ecs and cloudrun components, in TypeScript and Go, against the same
// cases as the Terraform modules for those targets: the literal render
// cases must give docuconf render's env without the kubelet's $ escaping,
// references must render as the target takes them, and what the target
// cannot carry must be rejected.

// targetBase is set in every case.
var targetBase = map[string]map[string]any{
	"ecs": {
		"API_TOKEN":    map[string]any{"secret": map[string]any{"valueFrom": tokenARN}},
		"DATABASE_URL": map[string]any{"secret": map[string]any{"valueFrom": dbParam}},
		"TENANT":       map[string]any{"value": "acme"},
	},
	"cloudrun": {
		"API_TOKEN":    map[string]any{"secret": map[string]any{"secret": "kitchen-token"}},
		"DATABASE_URL": map[string]any{"secret": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-db", "version": "4"}},
		"TENANT":       map[string]any{"value": "acme"},
	},
}

var literalCases = map[string]bool{"literals": true, "durations": true, "whole_durations": true, "lists_and_json": true, "code_points": true}

type targetCase struct {
	name string
	args map[string]any
	want string // in the rejection; "" for a case that renders
}

func targetCases(target string) []targetCase {
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range targetBase[target] {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	var cases []targetCase
	for _, tc := range renderCases {
		if !literalCases[tc.name] {
			continue
		}
		args := map[string]any{}
		for k, v := range tc.values {
			args[k] = map[string]any{"value": v}
		}
		cases = append(cases, targetCase{name: tc.name, args: with(args)})
	}
	reject := func(name string, args map[string]any, want string) targetCase {
		return targetCase{name: "reject/" + name, args: with(args), want: want}
	}
	common := []targetCase{
		reject("secret_as_value", map[string]any{"API_TOKEN": map[string]any{"value": "hunter2"}}, "API_TOKEN: set exactly one of secret or injected (a secret is never a literal value)"),
		reject("value_checks", map[string]any{"WORKERS": map[string]any{"value": 0}}, "WORKERS:"),
		reject("duration", map[string]any{"TIMEOUT": map[string]any{"value": "10m"}}, "TIMEOUT:"),
		reject("provider", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "Bank Vaults"}}}, "GREETING: injected.provider must name the injector"),
		reject("empty_ref", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "op", "ref": ""}}}, "GREETING: injected.ref cannot be empty"),
		reject("indexed_key_set", map[string]any{"SIGNING_KEYS": map[string]any{"secret": targetBase[target]["API_TOKEN"].(map[string]any)["secret"]}}, "SIGNING_KEYS: set exactly one of injected (an indexed key set is one variable per key"),
	}
	cases = append(cases, common...)
	if target == "ecs" {
		cases = append(cases,
			targetCase{name: "selectors", args: with(map[string]any{
				"API_TOKEN":    map[string]any{"secret": map[string]any{"valueFrom": tokenARN, "jsonKey": "token", "versionStage": "AWSCURRENT"}},
				"DATABASE_URL": map[string]any{"secret": map[string]any{"valueFrom": "kitchen-database-url"}},
				"GREETING":     map[string]any{"injected": map[string]any{"provider": "op", "ref": "op://vault/item/$greeting"}},
				"SIGNING_KEYS": map[string]any{"injected": map[string]any{"provider": "op"}},
			})},
			reject("config_map", map[string]any{"LOG_LEVEL": map[string]any{"configMapKeyRef": map[string]any{"name": "a", "key": "b"}}}, "LOG_LEVEL: set exactly one of value or injected (ECS has no ConfigMaps or Downward API)"),
			reject("value_from", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"valueFrom": "not a name!"}}}, `API_TOKEN: secret.valueFrom "not a name!" is not a Secrets Manager ARN, a Parameter Store ARN, or the name of a secret or parameter`),
			reject("selector_needs_arn", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"valueFrom": "kitchen-token", "jsonKey": "token"}}}, "API_TOKEN: secret.jsonKey, versionStage and versionId select from a Secrets Manager secret, so secret.valueFrom must be its full ARN"),
			reject("selector_on_parameter", map[string]any{"DATABASE_URL": map[string]any{"secret": map[string]any{"valueFrom": dbParam, "versionId": "1"}}}, "DATABASE_URL: secret.jsonKey, versionStage and versionId select from a Secrets Manager secret"),
			reject("stage_and_id", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"valueFrom": tokenARN, "versionStage": "AWSCURRENT", "versionId": "x"}}}, "API_TOKEN: set secret.versionStage or secret.versionId, not both"),
			reject("file", map[string]any{"settings": map[string]any{"secret": map[string]any{"secret": "s"}}}, "settings: the ecs target cannot mount this file input"),
		)
	} else {
		cases = append(cases,
			targetCase{name: "files", args: with(map[string]any{
				"settings": map[string]any{"secret": map[string]any{"secret": "kitchen-settings"}},
				"keystore": map[string]any{"secret": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-ks", "version": "2"}},
			})},
			reject("field", map[string]any{"NODE_NAME": map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.nodeName"}}}, "NODE_NAME: set exactly one of value or injected (Cloud Run has no ConfigMaps or Downward API)"),
			reject("secret_name", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"secret": "projects/x/secrets/a b"}}}, `API_TOKEN: secret.secret "projects/x/secrets/a b" is not a Secret Manager secret ID or projects/PROJECT/secrets/SECRET`),
			reject("version", map[string]any{"API_TOKEN": map[string]any{"secret": map[string]any{"secret": "t", "version": "v2"}}}, `API_TOKEN: secret.version "v2" is not latest or a version number`),
			reject("file_inline", map[string]any{"settings": map[string]any{"inline": "a: 1"}}, "settings: set exactly one of secret or injected (Cloud Run mounts files from Secret Manager)"),
			reject("file_version", map[string]any{"settings": map[string]any{"secret": map[string]any{"secret": "s", "version": "0"}}}, `settings: secret.version "0" is not latest or a version number`),
			reject("tls", map[string]any{"serving-tls": map[string]any{"secret": map[string]any{"secret": "s"}}}, "serving-tls: the cloudrun target cannot mount this file input"),
		)
	}
	return cases
}

// targetResult is what a component rendered, in one shape for both
// languages and both targets, or the error it threw.
type targetResult struct {
	Error       string           `json:"error"`
	Environment []map[string]any `json:"environment"`
	Secrets     []map[string]any `json:"secrets"`
	Env         []map[string]any `json:"env"`
	Volumes     []map[string]any `json:"volumes"`
	Mounts      []map[string]any `json:"mounts"`
}

const tsTargetHarness = `import * as pulumi from "@pulumi/pulumi";
import { readFileSync } from "node:fs";
import { KitchenApiConfig } from "./kitchen-config";

pulumi.runtime.setMocks({ newResource: (a) => ({ id: a.name, state: a.inputs }), call: (a) => a.inputs }, "test", "test", false);

const value = <T>(o: pulumi.Output<T>): Promise<T> => (o as unknown as { promise(): Promise<T> }).promise();

async function main() {
  const target = process.argv[3];
  const cases: { name: string; args: Record<string, unknown> }[] = JSON.parse(readFileSync(process.argv[2]!, "utf8"));
  const out: Record<string, unknown> = {};
  for (const c of cases) {
    try {
      const x = new KitchenApiConfig(c.name, c.args as never) as any;
      await value(x.checked);
      out[c.name] = target === "ecs"
        ? { environment: await value(x.ecsEnvironment), secrets: await value(x.ecsSecrets) }
        : { env: await value(x.cloudrunEnv), volumes: await value(x.cloudrunVolumes), mounts: await value(x.cloudrunVolumeMounts) };
    } catch (e) {
      out[c.name] = { error: (e as Error).message };
    }
  }
  process.stdout.write(JSON.stringify(out));
}
process.on("unhandledRejection", () => {});
main().catch((e) => { console.error(e); process.exit(1); });
`

func runTSTarget(t *testing.T, target string, cases []targetCase) map[string]targetResult {
	t.Helper()
	modules := nodeModules(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pulumi", "-contract", kitchen, "-target", target, "-out", filepath.Join(dir, "kitchen-config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf pulumi -target %s: %s", target, stderr.String())
	}
	os.Symlink(modules, filepath.Join(dir, "node_modules"))
	writeFile(t, filepath.Join(dir, "harness.ts"), tsTargetHarness)
	writeFile(t, filepath.Join(dir, "tsconfig.json"), pulumiTSConfig)
	tsc := exec.Command(filepath.Join(dir, "node_modules", ".bin", "tsc"), "-p", ".")
	tsc.Dir = dir
	if out, err := tsc.CombinedOutput(); err != nil {
		t.Fatalf("tsc on the %s component: %v\n%s", target, err, out)
	}
	var list []map[string]any
	for _, c := range cases {
		list = append(list, map[string]any{"name": c.name, "args": c.args})
	}
	writeJSON(t, filepath.Join(dir, "cases.json"), list)
	cmd := exec.Command(filepath.Join(dir, "node_modules", ".bin", "tsx"), "harness.ts", "cases.json", target)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("harness: %v\n%s", err, out)
	}
	got := map[string]targetResult{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("harness output: %v\n%s", err, out)
	}
	return got
}

const goTargetHarness = `package kitchenapiconfig

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type mocks struct{}

func (mocks) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return a.Name, a.Inputs, nil
}
func (mocks) Call(a pulumi.MockCallArgs) (resource.PropertyMap, error) { return a.Args, nil }

func TestHarness(t *testing.T) {
	raw, _ := os.ReadFile(os.Getenv("DOCUCONF_CASES"))
	var cases []struct {
		Name string
		Args map[string]map[string]any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	results := map[string]any{}
	for _, c := range cases {
		res := map[string]any{}
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			comp := &KitchenApiConfig{}
			if err := ctx.RegisterComponentResource("docuconf:test:Config", c.Name, comp); err != nil {
				return err
			}
			in := inputs{}
			for n, src := range c.Args {
				for kind, x := range src {
					in[n] = source{kind, x}
				}
			}
			all, _, err := buildTarget(ctx, comp, in)
			if err != nil {
				return err
			}
			ctx.Export("r", all.ApplyT(func(x any) error {
				r := x.(targetRendered)
				res["environment"], res["secrets"] = r.ecsEnvironment, r.ecsSecrets
				res["env"], res["volumes"], res["mounts"] = r.env, r.volumes, r.volumeMounts
				return nil
			}))
			return nil
		}, pulumi.WithMocks("test", "test", mocks{}))
		if err != nil {
			res = map[string]any{"error": err.Error()}
		}
		results[c.Name] = res
	}
	b, _ := json.Marshal(results)
	os.WriteFile(os.Getenv("DOCUCONF_OUT"), b, 0o644)
}
`

func runGoTarget(t *testing.T, target string, cases []targetCase) map[string]targetResult {
	t.Helper()
	if os.Getenv("DOCUCONF_SKIP_PULUMI_GO") != "" {
		t.Skip("DOCUCONF_SKIP_PULUMI_GO is set")
	}
	dir := t.TempDir()
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	mod, _ := os.ReadFile(filepath.Join("testdata", "pulumi-go", "go.mod.tmpl"))
	sum, _ := os.ReadFile(filepath.Join("testdata", "pulumi-go", "go.sum"))
	writeFile(t, filepath.Join(dir, "go.mod"), string(mod)+"\nreplace github.com/docuconf/docuconf-go => "+repo+"\n")
	writeFile(t, filepath.Join(dir, "go.sum"), string(sum))
	pkg := filepath.Join(dir, "kitchenapiconfig")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pulumi", "-contract", kitchen, "-lang", "go", "-target", target, "-out", pkg}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf pulumi -lang go -target %s: %s", target, stderr.String())
	}
	writeFile(t, filepath.Join(pkg, "harness_test.go"), goTargetHarness)
	var list []map[string]any
	for _, c := range cases {
		list = append(list, map[string]any{"name": c.name, "args": c.args})
	}
	writeJSON(t, filepath.Join(dir, "cases.json"), list)
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet on the %s component: %v\n%s", target, err, out)
	}
	cmd := exec.Command("go", "test", "-count=1", "-run", "TestHarness", "./kitchenapiconfig")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DOCUCONF_CASES="+filepath.Join(dir, "cases.json"), "DOCUCONF_OUT="+filepath.Join(dir, "out.json"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("harness: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "out.json"))
	got := map[string]targetResult{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("harness output: %v\n%s", err, raw)
	}
	return got
}

// checkTarget compares a component's results with docuconf render and the
// Terraform modules' expectations.
func checkTarget(t *testing.T, target string, got map[string]targetResult) {
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range targetCases(target) {
		t.Run(tc.name, func(t *testing.T) {
			g := got[tc.name]
			if tc.want != "" {
				if g.Error == "" {
					t.Fatalf("accepted; want an error containing %q", tc.want)
				}
				if !strings.Contains(g.Error, tc.want) {
					t.Errorf("the error does not contain %q:\n%s", tc.want, g.Error)
				}
				return
			}
			if g.Error != "" {
				t.Fatalf("rejected:\n%s", g.Error)
			}
			env := g.Environment
			var refs []map[string]any
			if target == "cloudrun" {
				env = nil
				for _, e := range g.Env {
					if e["valueSource"] != nil {
						refs = append(refs, e)
					} else {
						env = append(env, e)
					}
				}
			}
			for _, rc := range renderCases {
				if rc.name != tc.name {
					continue
				}
				// #Render's env for the same values, less the secrets, with
				// the kubelet's $$ back to $.
				var want []any
				for _, e := range renderDoc(t, p, c, rc.doc).Env {
					m := e.(map[string]any)
					if v, ok := m["value"].(string); ok {
						want = append(want, map[string]any{"name": m["name"], "value": strings.ReplaceAll(v, "$$", "$")})
					}
				}
				if gj, wj := canon(t, env), canon(t, want); gj != wj {
					t.Errorf("env differs from docuconf render:\n component: %s\n render:    %s", gj, wj)
				}
				if target == "ecs" {
					want := []map[string]any{{"name": "API_TOKEN", "valueFrom": tokenARN}, {"name": "DATABASE_URL", "valueFrom": dbParam}}
					if gj, wj := canon(t, g.Secrets), canon(t, want); gj != wj {
						t.Errorf("secrets: %s, want %s", gj, wj)
					}
				} else {
					want := []map[string]any{
						{"name": "API_TOKEN", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "kitchen-token", "version": "latest"}}},
						{"name": "DATABASE_URL", "valueSource": map[string]any{"secretKeyRef": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-db", "version": "4"}}},
					}
					if gj, wj := canon(t, refs), canon(t, want); gj != wj {
						t.Errorf("secret env: %s, want %s", gj, wj)
					}
				}
			}
			switch tc.name {
			case "selectors":
				want := []map[string]any{
					{"name": "API_TOKEN", "valueFrom": tokenARN + ":token:AWSCURRENT:"},
					{"name": "DATABASE_URL", "valueFrom": "kitchen-database-url"},
				}
				if gj, wj := canon(t, g.Secrets), canon(t, want); gj != wj {
					t.Errorf("secrets: %s, want %s", gj, wj)
				}
				found := false
				for _, e := range g.Environment {
					found = found || (e["name"] == "GREETING" && e["value"] == "op://vault/item/$greeting")
					if strings.HasPrefix(e["name"].(string), "SIGNING_KEYS") {
						t.Errorf("an injected key set without ref rendered %v", e)
					}
				}
				if !found {
					t.Errorf("no GREETING entry with the injected reference, unescaped: %v", g.Environment)
				}
			case "files":
				wantVols := []any{
					map[string]any{"name": "dc-settings", "secret": map[string]any{"secret": "kitchen-settings", "defaultMode": 292, "items": []any{map[string]any{"version": "latest", "path": "settings.yaml"}}}},
					map[string]any{"name": "dc-keystore", "secret": map[string]any{"secret": "projects/acme-prod/secrets/kitchen-ks", "defaultMode": 256, "items": []any{map[string]any{"version": "2", "path": "client.p12"}}}},
				}
				wantMounts := []any{
					map[string]any{"name": "dc-settings", "mountPath": "/etc/kitchen/settings"},
					map[string]any{"name": "dc-keystore", "mountPath": "/etc/kitchen/ks"},
				}
				if gj, wj := canon(t, g.Volumes), canon(t, wantVols); gj != wj {
					t.Errorf("volumes:\n component %s\n want      %s", gj, wj)
				}
				if gj, wj := canon(t, g.Mounts), canon(t, wantMounts); gj != wj {
					t.Errorf("mounts:\n component %s\n want      %s", gj, wj)
				}
				found := false
				for _, e := range g.Env {
					found = found || (e["name"] == "SETTINGS_PATH" && e["value"] == "/etc/kitchen/settings/settings.yaml")
				}
				if !found {
					t.Errorf("no SETTINGS_PATH entry: %v", g.Env)
				}
			}
		})
	}
}

func TestPulumiTargets(t *testing.T) {
	for _, target := range []string{"ecs", "cloudrun"} {
		t.Run("typescript/"+target, func(t *testing.T) {
			checkTarget(t, target, runTSTarget(t, target, targetCases(target)))
		})
	}
}

func TestPulumiGoTargets(t *testing.T) {
	for _, target := range []string{"ecs", "cloudrun"} {
		t.Run(target, func(t *testing.T) {
			checkTarget(t, target, runGoTarget(t, target, targetCases(target)))
		})
	}
}

func TestPulumiTargetFiles(t *testing.T) {
	// A required file input the target cannot carry fails generation.
	dir := t.TempDir()
	contract := filepath.Join(dir, "contract.cue")
	src, err := os.ReadFile(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	marked := strings.Replace(string(src), `"serving-tls": {type: "tls",`, `"serving-tls": {required: true, type: "tls",`, 1)
	if marked == string(src) {
		t.Fatal("no serving-tls input in the kitchen contract")
	}
	os.WriteFile(contract, []byte(marked), 0o644)
	for target, want := range map[string]string{"ecs": "the ecs target mounts no files", "cloudrun": "requires tls inputs serving-tls"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"pulumi", "-contract", contract, "-target", target, "-out", filepath.Join(dir, target)}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), want) {
			t.Errorf("-target %s with a required tls input: exit %d, %s", target, code, stderr.String())
		}
	}
}
