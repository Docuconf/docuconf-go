package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The TypeScript component is run with Node and @pulumi/pulumi's mocks:
// no Pulumi CLI, plugin or cloud. The packages come from
// testdata/pulumi, installed with npm ci on first use; without node and
// npm the tests are skipped.

var pulumiModules = filepath.Join("testdata", "pulumi")

func nodeModules(t *testing.T) string {
	t.Helper()
	for _, b := range []string{"node", "npm"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s is not installed", b)
		}
	}
	dir, err := filepath.Abs(pulumiModules)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@pulumi", "pulumi")); err != nil {
		cmd := exec.Command("npm", "ci", "--no-audit", "--no-fund")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm ci in %s: %v\n%s", dir, err, out)
		}
	}
	return filepath.Join(dir, "node_modules")
}

// pulumiArgs turns the documents into the component's arguments: a literal
// becomes { value }, references and file sources are already in the
// component's shape.
func pulumiArgs(d doc) map[string]any {
	args := map[string]any{}
	pod := map[string]any{}
	for k, x := range d.allValues() {
		switch k {
		case "podAnnotations":
			pod["annotations"] = x
			continue
		case "podLabels":
			pod["labels"] = x
			continue
		}
		m, ok := x.(map[string]any)
		isRef := ok && (m["secretKeyRef"] != nil || m["configMapKeyRef"] != nil || m["fieldRef"] != nil || m["resourceFieldRef"] != nil || m["injected"] != nil)
		if isRef {
			args[k] = m
		} else {
			args[k] = map[string]any{"value": x}
		}
	}
	if len(pod) > 0 {
		args["podMetadata"] = pod
	}
	for k, x := range d.files {
		args[k] = x
	}
	if d.overlays != nil {
		args["overlayValues"] = d.overlays
	}
	return args
}

// pulumiComponent generates the kitchen component next to a node_modules
// link, and a harness that constructs it once per case and prints what
// each instance rendered, or the error it threw.
func pulumiComponent(t *testing.T) string {
	t.Helper()
	modules := nodeModules(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pulumi", "-contract", kitchen, "-out", filepath.Join(dir, "kitchen-config")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf pulumi: exit %d: %s", code, stderr.String())
	}
	if err := os.Symlink(modules, filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	harness := `import * as pulumi from "@pulumi/pulumi";
import { readFileSync } from "node:fs";
import { KitchenApiConfig } from "./kitchen-config";

pulumi.runtime.setMocks({ newResource: (a) => ({ id: a.name, state: a.inputs }), call: (a) => a.inputs }, "test", "test", false);

const value = <T>(o: pulumi.Output<T>): Promise<T> => (o as unknown as { promise(): Promise<T> }).promise();

async function main() {
  const cases: { name: string; args: Record<string, unknown> }[] = JSON.parse(readFileSync(process.argv[2]!, "utf8"));
  const out: Record<string, unknown> = {};
  for (const c of cases) {
    try {
      const x = new KitchenApiConfig(c.name, c.args as never);
      await value(x.checked);
      out[c.name] = {
        env: await value(x.kubernetesEnv),
        volumes: await value(x.kubernetesVolumes),
        volumeMounts: await value(x.kubernetesVolumeMounts),
        restartTriggers: await value(x.restartTriggers),
        podAnnotations: await value(x.podAnnotations),
        podLabels: await value(x.podLabels),
        configMaps: x.kubernetesConfigMaps,
        bindings: x.bindings,
      };
    } catch (e) {
      out[c.name] = { error: (e as Error).message };
    }
  }
  process.stdout.write(JSON.stringify(out));
}
// Outputs of a rejected case that the harness does not await reject too.
process.on("unhandledRejection", () => {});
main().catch((e) => { console.error(e); process.exit(1); });
`
	writeFile(t, filepath.Join(dir, "harness.ts"), harness)
	writeFile(t, filepath.Join(dir, "tsconfig.json"), pulumiTSConfig)
	return dir
}

// pulumiTSConfig is the tsconfig.json of `pulumi new typescript`, which the
// generated component must compile under.
const pulumiTSConfig = `{
  "compilerOptions": {
    "strict": true,
    "outDir": "bin",
    "target": "es2020",
    "module": "commonjs",
    "moduleResolution": "node",
    "sourceMap": true,
    "experimentalDecorators": true,
    "pretty": true,
    "noFallthroughCasesInSwitch": true,
    "noImplicitReturns": true,
    "forceConsistentCasingInFileNames": true,
    "noEmit": true
  },
  "files": ["harness.ts"]
}
`

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

type pulumiResult struct {
	Error           string              `json:"error"`
	Env             any                 `json:"env"`
	Volumes         any                 `json:"volumes"`
	VolumeMounts    any                 `json:"volumeMounts"`
	RestartTriggers any                 `json:"restartTriggers"`
	PodAnnotations  any                 `json:"podAnnotations"`
	PodLabels       any                 `json:"podLabels"`
	ConfigMaps      []pulumiConfigMap   `json:"configMaps"`
	Bindings        map[string]struct{} `json:"bindings"`
}

type pulumiConfigMap struct {
	Name      string            `json:"name"`
	Data      map[string]string `json:"data"`
	Immutable bool              `json:"immutable"`
}

// runHarness runs the cases through the component.
func runHarness(t *testing.T, dir string, cases map[string]map[string]any) map[string]pulumiResult {
	t.Helper()
	var list []map[string]any
	for name, args := range cases {
		list = append(list, map[string]any{"name": name, "args": args})
	}
	writeJSON(t, filepath.Join(dir, "cases.json"), list)
	cmd := exec.Command(filepath.Join(dir, "node_modules", ".bin", "tsx"), "harness.ts", "cases.json")
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("harness: %v\n%s%s", err, out.String(), errb.String())
	}
	got := map[string]pulumiResult{}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("harness output: %v\n%s", err, out.String())
	}
	return got
}

func TestPulumiTypeChecks(t *testing.T) {
	dir := pulumiComponent(t)
	cmd := exec.Command(filepath.Join(dir, "node_modules", ".bin", "tsc"), "-p", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc, with the tsconfig of pulumi new typescript: %v\n%s", err, out)
	}
}

func TestPulumiMatchesRender(t *testing.T) {
	dir := pulumiComponent(t)
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]any{}
	for _, tc := range renderCases {
		cases[tc.name] = pulumiArgs(tc.doc)
	}
	got := runHarness(t, dir, cases)
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			g := got[tc.name]
			if g.Error != "" {
				t.Fatalf("the component rejects a case vet accepts:\n%s", g.Error)
			}
			want := renderDoc(t, p, c, tc.doc)
			compare := func(what string, component, render any) {
				t.Helper()
				if gj, wj := canon(t, component), canon(t, render); gj != wj {
					t.Errorf("%s differs from docuconf render:\n component: %s\n render:    %s", what, gj, wj)
				}
			}
			compare("env", g.Env, want.Env)
			compare("volumes", maskHashesIn(g.Volumes), maskHashesIn(want.Volumes))
			compare("volumeMounts", g.VolumeMounts, want.VolumeMounts)
			compare("restartTriggers", g.RestartTriggers, want.RestartTriggers)
			compare("podAnnotations", g.PodAnnotations, want.PodAnnotations)
			compare("podLabels", g.PodLabels, want.PodLabels)

			// ConfigMaps by meaning: the same names but for the hash, the same
			// immutability, and content that parses to the same data. A
			// hashed name carries the hash of the content the component wrote.
			cms := map[string]any{}
			for _, cm := range want.ConfigMaps {
				data := map[string]any{}
				for k, x := range cm.Data {
					data[k] = parseContent(t, k, x)
				}
				cms[maskHashes(`"`+cm.Metadata.Name+`"`)] = map[string]any{"data": data, "immutable": cm.Immutable}
			}
			gcms := map[string]any{}
			for _, cm := range g.ConfigMaps {
				data := map[string]any{}
				for k, content := range cm.Data {
					data[k] = parseContent(t, k, content)
					if m := regexp.MustCompile(`-([0-9a-f]{10})$`).FindStringSubmatch(cm.Name); m != nil {
						sum := sha256.Sum256([]byte(content))
						if h := hex.EncodeToString(sum[:])[:10]; h != m[1] {
							t.Errorf("ConfigMap %s: hash %s, but its content hashes to %s", cm.Name, m[1], h)
						}
					}
				}
				gcms[maskHashes(`"`+cm.Name+`"`)] = map[string]any{"data": data, "immutable": cm.Immutable}
			}
			compare("configMaps", gcms, cms)
		})
	}
}

// maskHashesIn masks the content hashes of ConfigMap names inside v.
func maskHashesIn(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal([]byte(maskHashes(string(b))), &out)
	return out
}

// The component rejects exactly what vet rejects, and names the input. Its
// wording is the app's (spec/IAC.md 6.2), so only the input is compared.
func TestPulumiRejectsWhatVetRejects(t *testing.T) {
	dir := pulumiComponent(t)
	cases := map[string]map[string]any{}
	for _, tc := range rejectCases {
		cases[tc.name] = pulumiArgs(tc.doc)
	}
	got := runHarness(t, dir, cases)
	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(append([]string{"vet"}, vetArgs(t, tc.doc)...), &stdout, &stderr)
			g := got[tc.name]
			switch {
			case code == 0 && g.Error != "":
				t.Errorf("vet accepts the case, but the component rejects it:\n%s", g.Error)
			case code != 0 && g.Error == "":
				t.Errorf("vet rejects the case, but the component accepts it; vet said:\n%s", stdout.String())
			case code != 0:
				input := vetSubject(stdout.String(), tc.want)
				if !strings.Contains(g.Error, input) {
					t.Errorf("the component's error does not name %s:\n%s\nvet said:\n%s", input, g.Error, stdout.String())
				}
			}
		})
	}
}

func TestPulumiCheck(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "c")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pulumi", "-contract", kitchen, "-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf pulumi: %s", stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"pulumi", "-contract", kitchen, "-out", out, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check on a fresh component: exit %d: %s", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(out, "index.ts"), []byte("// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"pulumi", "-contract", kitchen, "-out", out, "-check"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "is out of date: index.ts") {
		t.Fatalf("-check on an edited component: exit %d: %s", code, stderr.String())
	}
	for _, args := range [][]string{{"-lang", "cobol"}, {"-target", "lambda"}} {
		stderr.Reset()
		if code := run(append([]string{"pulumi", "-contract", kitchen, "-out", out}, args...), &stdout, &stderr); code == 0 {
			t.Errorf("%v: want an error", args)
		}
	}
}

// vetSubject is what vet's first problem is about: the text before its
// first colon, the input or pod metadata key. Where vet prints a raw CUE
// path instead, the module's wording names it.
func vetSubject(vetOut, want string) string {
	for _, l := range strings.Split(vetOut, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "warning:") {
			continue
		}
		if strings.HasPrefix(l, "#") {
			break
		}
		subject, _, _ := strings.Cut(l, ":")
		return subject
	}
	subject, _, _ := strings.Cut(want, ":")
	return strings.TrimSpace(subject)
}
