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
	"sync"
	"testing"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The Go component is run with go test and the Pulumi Go SDK's mocks: no
// Pulumi CLI, plugin or cloud. Its module (testdata/pulumi-go) pins the
// Pulumi modules and builds against this checkout's SDK. The modules are
// downloaded on first use; set DOCUCONF_SKIP_PULUMI_GO=1 to skip.

// goHarness is placed in the generated package, so that it can feed each
// case's documents to the runtime directly: the typed Args are checked by
// the example program, which compiles against them.
const goHarness = `package kitchenapiconfig

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type mocks struct{}

func (mocks) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	return a.Name, a.Inputs, nil
}
func (mocks) Call(a pulumi.MockCallArgs) (resource.PropertyMap, error) { return a.Args, nil }

// k8sJSON is a value of the kubernetes provider's types as the Kubernetes
// API writes it: fields by their pulumi tag, nil pointers left out.
func k8sJSON(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return k8sJSON(v.Elem())
	case reflect.Struct:
		out := map[string]any{}
		for i := 0; i < v.NumField(); i++ {
			tag := v.Type().Field(i).Tag.Get("pulumi")
			if tag == "" {
				tag = strings.ToLower(v.Type().Field(i).Name[:1]) + v.Type().Field(i).Name[1:]
			}
			if x := k8sJSON(v.Field(i)); x != nil {
				out[tag] = x
			}
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = k8sJSON(v.Index(i))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := map[string]any{}
		for _, k := range v.MapKeys() {
			out[k.String()] = k8sJSON(v.MapIndex(k))
		}
		return out
	}
	return v.Interface()
}

func TestHarness(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("DOCUCONF_CASES"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string
		Args map[string]any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	results := map[string]map[string]any{}
	for _, c := range cases {
		res := map[string]any{}
		err := pulumi.RunErr(func(ctx *pulumi.Context) error {
			comp := &KitchenApiConfig{}
			if err := ctx.RegisterComponentResource("docuconf:test:Config", c.Name, comp); err != nil {
				return err
			}
			in := inputs{}
			var pod *PodMetadata
			var overlays map[string]map[string]any
			for k, v := range c.Args {
				switch k {
				case "podMetadata":
					m := v.(map[string]any)
					pod = &PodMetadata{Annotations: map[string]string{}, Labels: map[string]string{}}
					if l, ok := m["annotations"].(map[string]any); ok {
						for a, x := range l {
							pod.Annotations[a] = x.(string)
						}
					}
					if l, ok := m["labels"].(map[string]any); ok {
						for a, x := range l {
							pod.Labels[a] = x.(string)
						}
					}
				case "overlayValues":
					overlays = map[string]map[string]any{}
					for o, x := range v.(map[string]any) {
						overlays[o] = x.(map[string]any)
					}
				default:
					for kind, x := range v.(map[string]any) {
						if kind == "csi" {
							if m := x.(map[string]any); m["driver"] == nil {
								m["driver"] = "secrets-store.csi.k8s.io"
							}
						}
						in[k] = source{kind, x}
					}
				}
			}
			out, err := build(ctx, comp, in, overlays, pod)
			if err != nil {
				return err
			}
			cms := []any{}
			for _, cm := range out.configMaps {
				cms = append(cms, map[string]any{"name": cm.Name, "data": cm.Data, "immutable": cm.Immutable})
			}
			res["configMaps"] = cms
			ctx.Export("r", pulumi.All(out.env, out.volumes, out.volumeMounts, out.restartTriggers, out.podAnnotations, out.podLabels, out.checked).ApplyT(func(xs []any) error {
				for i, k := range []string{"env", "volumes", "volumeMounts", "restartTriggers", "podAnnotations", "podLabels"} {
					res[k] = k8sJSON(reflect.ValueOf(xs[i]))
				}
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
	if err := os.WriteFile(os.Getenv("DOCUCONF_OUT"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
`

type goResult struct {
	Error           string            `json:"error"`
	Env             any               `json:"env"`
	Volumes         any               `json:"volumes"`
	VolumeMounts    any               `json:"volumeMounts"`
	RestartTriggers any               `json:"restartTriggers"`
	PodAnnotations  any               `json:"podAnnotations"`
	PodLabels       any               `json:"podLabels"`
	ConfigMaps      []pulumiConfigMap `json:"configMaps"`
}

var (
	goResults     map[string]goResult
	goResultsOnce sync.Once
	goResultsErr  string
)

// goComponentResults runs every render and reject case through the
// generated Go component, once for the tests that need them.
func goComponentResults(t *testing.T) map[string]goResult {
	t.Helper()
	if os.Getenv("DOCUCONF_SKIP_PULUMI_GO") != "" {
		t.Skip("DOCUCONF_SKIP_PULUMI_GO is set")
	}
	goResultsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "docuconf-pulumi-go-")
		if err != nil {
			goResultsErr = err.Error()
			return
		}
		defer os.RemoveAll(dir)
		repo, _ := filepath.Abs(filepath.Join("..", ".."))
		mod, _ := os.ReadFile(filepath.Join("testdata", "pulumi-go", "go.mod.tmpl"))
		sum, _ := os.ReadFile(filepath.Join("testdata", "pulumi-go", "go.sum"))
		os.WriteFile(filepath.Join(dir, "go.mod"), append(mod, []byte("\nreplace github.com/docuconf/docuconf-go => "+repo+"\n")...), 0o644)
		os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644)
		pkg := filepath.Join(dir, "kitchenapiconfig")
		var stdout, stderr bytes.Buffer
		if code := run([]string{"pulumi", "-contract", kitchen, "-lang", "go", "-out", pkg}, &stdout, &stderr); code != 0 {
			goResultsErr = "docuconf pulumi -lang go: " + stderr.String()
			return
		}
		os.WriteFile(filepath.Join(pkg, "harness_test.go"), []byte(goHarness), 0o644)
		var list []map[string]any
		for _, tc := range renderCases {
			list = append(list, map[string]any{"name": "render/" + tc.name, "args": pulumiArgs(tc.doc)})
		}
		for _, tc := range rejectCases {
			list = append(list, map[string]any{"name": "reject/" + tc.name, "args": pulumiArgs(tc.doc)})
		}
		b, _ := json.Marshal(list)
		os.WriteFile(filepath.Join(dir, "cases.json"), b, 0o644)
		vet := exec.Command("go", "vet", "./...")
		vet.Dir = dir
		if out, err := vet.CombinedOutput(); err != nil {
			goResultsErr = "go vet on the generated component: " + err.Error() + "\n" + string(out)
			return
		}
		cmd := exec.Command("go", "test", "-count=1", "-run", "TestHarness", "./kitchenapiconfig")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "DOCUCONF_CASES="+filepath.Join(dir, "cases.json"), "DOCUCONF_OUT="+filepath.Join(dir, "out.json"))
		if out, err := cmd.CombinedOutput(); err != nil {
			goResultsErr = "the harness: " + err.Error() + "\n" + string(out)
			return
		}
		raw, err := os.ReadFile(filepath.Join(dir, "out.json"))
		if err != nil {
			goResultsErr = err.Error()
			return
		}
		goResults = map[string]goResult{}
		if err := json.Unmarshal(raw, &goResults); err != nil {
			goResultsErr = err.Error()
		}
	})
	if goResultsErr != "" {
		t.Fatal(goResultsErr)
	}
	return goResults
}

func TestPulumiGoMatchesRender(t *testing.T) {
	got := goComponentResults(t)
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(kitchen)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			g := got["render/"+tc.name]
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

func TestPulumiGoRejectsWhatVetRejects(t *testing.T) {
	got := goComponentResults(t)
	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(append([]string{"vet"}, vetArgs(t, tc.doc)...), &stdout, &stderr)
			g := got["reject/"+tc.name]
			switch {
			case code == 0 && g.Error != "":
				t.Errorf("vet accepts the case, but the component rejects it:\n%s", g.Error)
			case code != 0 && g.Error == "":
				t.Errorf("vet rejects the case, but the component accepts it; vet said:\n%s", stdout.String())
			case code != 0:
				if input := vetSubject(stdout.String(), tc.want); !strings.Contains(g.Error, input) {
					t.Errorf("the component's error does not name %s:\n%s\nvet said:\n%s", input, g.Error, stdout.String())
				}
			}
		})
	}
}
