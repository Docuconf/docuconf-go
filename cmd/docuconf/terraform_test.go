package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"cuelang.org/go/cue"
	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

// The module's outputs must mean what docuconf render writes for the same
// values, files and overlays, and its validations must reject what docuconf
// vet rejects. These tests apply generated modules, so they need terraform
// (or tofu) on PATH; DOCUCONF_TERRAFORM names another binary. Without one
// they are skipped.

const kitchen = "testdata/terraform/contract.cue"

func terraformBinary(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("DOCUCONF_TERRAFORM"); b != "" {
		return b
	}
	for _, b := range []string{"terraform", "tofu"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	t.Skip("terraform or tofu is not installed")
	return ""
}

// base is set in every case: the module requires it.
var base = map[string]any{
	"API_TOKEN":    map[string]any{"secretKeyRef": map[string]any{"name": "kitchen", "key": "token"}},
	"DATABASE_URL": map[string]any{"secretKeyRef": map[string]any{"name": "kitchen-db", "key": "url"}},
	"TENANT":       "acme",
}

// doc is what the platform supplies: the values, files and overlays
// documents of docuconf vet and render.
type doc struct {
	values, files, overlays map[string]any
	drop                    []string // base values to leave out
}

func (d doc) allValues() map[string]any {
	out := map[string]any{}
	for k, x := range base {
		out[k] = x
	}
	for _, k := range d.drop {
		delete(out, k)
	}
	for k, x := range d.values {
		out[k] = x
	}
	return out
}

func vals(v map[string]any) doc { return doc{values: v} }

const (
	// A real CA certificate, valid for a century: the Pulumi component
	// checks inline content with @docuconf/core, which parses it.
	cert = "-----BEGIN CERTIFICATE-----\n" +
		"MIIBizCCATGgAwIBAgIUNilHmxOCCuALVx//POpOUEjedfAwCgYIKoZIzj0EAwIw\n" +
		"GjEYMBYGA1UEAwwPa2l0Y2hlbiB0ZXN0IENBMCAXDTI2MTEwNTAyNDM0NloYDzIx\n" +
		"MjYxMDEyMDI0MzQ2WjAaMRgwFgYDVQQDDA9raXRjaGVuIHRlc3QgQ0EwWTATBgcq\n" +
		"hkjOPQIBBggqhkjOPQMBBwNCAAR1RzQTx7k5kpSG6uSryUDlNodKpwURHGImodDZ\n" +
		"2OQ7LatP2kd732AZadLqYwudF/u/7xlmrVoeQwlhdBV7qch+o1MwUTAdBgNVHQ4E\n" +
		"FgQUbpuZWXh0IT69wd0rf1LWgcdyTjUwHwYDVR0jBBgwFoAUbpuZWXh0IT69wd0r\n" +
		"f1LWgcdyTjUwDwYDVR0TAQH/BAUwAwEB/zAKBggqhkjOPQQDAgNIADBFAiAs4b9r\n" +
		"3LXios4Sjm4kFzoa6lI8pXIjiJiVRbq6b1wy9gIhAJWDmKZYnW7nPVeIR0VETKys\n" +
		"N5+wm2JxIsZAhMmqApfy\n" +
		"-----END CERTIFICATE-----\n"
	tagEsc = `\` + "u003c" // a literal backslash before u003c, which must stay as it is
)

var renderCases = []struct {
	name string
	doc
}{
	{"literals", vals(map[string]any{
		"DEBUG":     true,
		"GREETING":  "hi $(X) <&>",
		"LOG_LEVEL": "warn",
		"WORKERS":   8,
		"RATIO":     0.25,
		"UPSTREAM":  "https://a.example/x?y=1&z",
		"TIMEOUT":   "1m30s",
		"REGION":    "eu-west-1",
		"PROFILE":   "prod",
	})},
	{"durations", vals(map[string]any{
		"BACKOFF":   "1500ms",
		"CACHE_TTL": "1h30m",
		"IDLE":      "26h3m4s5ms",
	})},
	{"whole_durations", vals(map[string]any{
		"BACKOFF":   "2s",
		"CACHE_TTL": "250ms",
		"IDLE":      "59s",
	})},
	{"lists_and_json", vals(map[string]any{
		"HOSTS":    []any{"a.internal", "b.internal"},
		"PORTS":    []any{80, 443},
		"TAGS":     []any{"x", "y<z", `back\slash`, tagEsc, "line" + string(rune(0x2028)) + "sep"},
		"FEATURES": map[string]any{"a": true, "b": []any{1, 2}, "c": "<&> $"},
	})},
	{"references", vals(map[string]any{
		"NODE_NAME":    map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.nodeName"}},
		"MEMORY_LIMIT": map[string]any{"resourceFieldRef": map[string]any{"resource": "limits.memory", "divisor": "1Mi"}},
		"LOG_LEVEL":    map[string]any{"configMapKeyRef": map[string]any{"name": "kitchen", "key": "log-level"}},
		"SIGNING_KEYS": map[string]any{"secretKeyRef": map[string]any{"name": "kitchen-keys", "key": "keys"}},
	})},
	{"injected", vals(map[string]any{
		"podAnnotations": map[string]any{"team": "core"},
		"GREETING": map[string]any{"injected": map[string]any{
			"provider":       "bank-vaults",
			"ref":            "vault:secret/data/k#$greeting",
			"podAnnotations": map[string]any{"vault.security.banzaicloud.io/vault-role": "{input}-reader"},
			"podLabels":      map[string]any{"inject": "true"},
		}},
		"API_TOKEN": map[string]any{"injected": map[string]any{"provider": "op"}},
	})},
	{"code_points", vals(map[string]any{
		// Two code points, one grapheme cluster: minLength 2 is met.
		"GREETING": "\U0001F44D\U0001F3FD",
	})},
	{"files_inline", doc{files: map[string]any{
		"settings": map[string]any{"inline": map[string]any{"retries": 3, "hosts": []any{"a", "b"}, "nested": map[string]any{"k": "v <&> ${x}"}}},
		"rules":    map[string]any{"inline": `{"r": 1}`},
		"legacy":   map[string]any{"inline": "a = 1\n[t]\nb = \"x\"\n"},
		"motd":     map[string]any{"inline": "hello $(world)"},
		"ca":       map[string]any{"inline": cert + cert},
		"serving-tls": map[string]any{"certificate": map[string]any{
			"name": "kitchen-tls", "secretName": "kitchen-tls", "dnsNames": []any{"kitchen.example.com"},
			"privateKey": map[string]any{"algorithm": "ECDSA"}, "duration": "2160h", "renewBefore": "720h",
		}},
		"keystore": map[string]any{"secret": map[string]any{"name": "kitchen-ks", "key": "client.p12"}},
		"model":    map[string]any{"image": map[string]any{"reference": "ghcr.io/acme/model:1", "pullPolicy": "IfNotPresent"}},
	}}},
	{"files_refs", doc{files: map[string]any{
		"settings":    map[string]any{"configMap": map[string]any{"name": "kitchen-settings", "key": "settings.yaml"}},
		"rules":       map[string]any{"secret": map[string]any{"name": "kitchen-rules", "key": "rules.json"}},
		"motd":        map[string]any{"csi": map[string]any{"secretProviderClass": "kitchen-motd"}},
		"serving-tls": map[string]any{"secret": map[string]any{"name": "kitchen-tls"}},
		"ca": map[string]any{"injected": map[string]any{
			"provider":       "trust-manager",
			"podAnnotations": map[string]any{"trust.example.com/{input}": "{dir}/{file}"},
			"podLabels":      map[string]any{"inject": "true"},
		}},
		"keystore": map[string]any{"csi": map[string]any{"driver": "other.csi.example.com", "secretProviderClass": "kitchen-ks"}},
		"model":    map[string]any{"configMap": map[string]any{"name": "kitchen-model", "key": "rank.bin"}},
	}}},
	{"overlays", doc{drop: []string{"TENANT"}, overlays: map[string]any{
		"appjson": map[string]any{"WORKERS": 4, "BACKOFF": "1500ms", "FEATURES": map[string]any{"a": []any{1, map[string]any{"b": true}}}, "TENANT": "acme"},
		"appyaml": map[string]any{"LOG_LEVEL": "debug", "PORTS": []any{80, 443}, "CACHE_TTL": "90s", "REGION": "eu"},
		"apptoml": map[string]any{"GREETING": `hé "q" <&>`, "TIMEOUT": "2m"},
	}}},
	{"empty_overlays", doc{overlays: map[string]any{
		"appjson": map[string]any{},
		"apptoml": map[string]any{},
	}}},
}

// snake is a key of the values documents as the module's attribute.
func snake(k string) string {
	var b strings.Builder
	for i, r := range k {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// snakeKeys renames a source's keys, except inside content and pod
// metadata, which are data.
func snakeKeys(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, x := range m {
		switch k {
		case "inline", "podAnnotations", "podLabels":
			out[snake(k)] = x
		default:
			out[snake(k)] = snakeKeys(x)
		}
	}
	return out
}

// moduleArgs turns the documents into the module's arguments, as a
// .tf.json module block.
func moduleArgs(d doc) map[string]any {
	args := map[string]any{}
	opt := map[string]any{}
	for k, v := range d.allValues() {
		switch k {
		case "podAnnotations":
			opt["pod_annotations"] = v
			continue
		case "podLabels":
			opt["pod_labels"] = v
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			args[k] = map[string]any{"value": v}
			continue
		}
		switch {
		case m["secretKeyRef"] != nil:
			args[k] = map[string]any{"secret": m["secretKeyRef"]}
		case m["configMapKeyRef"] != nil:
			args[k] = map[string]any{"config_map": m["configMapKeyRef"]}
		case m["fieldRef"] != nil, m["resourceFieldRef"] != nil, m["injected"] != nil:
			s := snakeKeys(m).(map[string]any)
			if s["field_ref"] != nil {
				s = map[string]any{"field": s["field_ref"]}
			}
			if s["resource_field_ref"] != nil {
				s = map[string]any{"resource_field": s["resource_field_ref"]}
			}
			args[k] = s
		default: // a json input's object
			args[k] = map[string]any{"value": v}
		}
	}
	if len(opt) > 0 {
		args["docuconf_options"] = opt
	}
	for k, v := range d.files {
		args[k] = snakeKeys(v)
	}
	for k, v := range d.overlays {
		args["overlay_"+k] = v
	}
	args = escapeTemplates(args).(map[string]any)
	args["source"] = "./module"
	return args
}

// escapeTemplates keeps strings in .tf.json from being read as templates.
func escapeTemplates(v any) any {
	switch x := v.(type) {
	case string:
		return strings.NewReplacer("${", "$${", "%{", "%%{").Replace(x)
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if y != nil {
				out[k] = escapeTemplates(y)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = escapeTemplates(y)
		}
		return out
	}
	return v
}

// camel turns the module's output objects into #Render's shape: camelCase
// keys, no nulls, no empty items, and defaultMode as a number.
func camel(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, y := range x {
			if y == nil {
				continue
			}
			if l, ok := y.([]any); ok && k == "items" && len(l) == 0 {
				continue
			}
			ck := k
			if strings.Contains(k, "_") {
				parts := strings.Split(k, "_")
				for i := 1; i < len(parts); i++ {
					parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
				}
				ck = strings.Join(parts, "")
			}
			if ck == "defaultMode" {
				n, _ := strconv.ParseInt(y.(string), 8, 64)
				out[ck] = n
				continue
			}
			if k == "volume_attributes" {
				out[ck] = y
				continue
			}
			out[ck] = camel(y)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = camel(y)
		}
		return out
	}
	return v
}

var hashSuffix = regexp.MustCompile(`-[0-9a-f]{10}"`)

// maskHashes hides content hashes in ConfigMap names: the module encodes
// YAML and JSON its own way, so its hashes differ from #Render's.
func maskHashes(s string) string { return hashSuffix.ReplaceAllString(s, `-HASH"`) }

// parseContent reads a ConfigMap entry by its file's format, so that the
// module's and #Render's encodings compare by meaning.
func parseContent(t *testing.T, key, content string) any {
	t.Helper()
	var v any
	var err error
	switch path.Ext(key) {
	case ".json":
		err = json.Unmarshal([]byte(content), &v)
	case ".yaml":
		err = yaml.Unmarshal([]byte(content), &v)
	case ".toml":
		var m map[string]any
		err = toml.Unmarshal([]byte(content), &m)
		v = m
	default:
		return content
	}
	if err != nil {
		t.Errorf("%s: %v\n%s", key, err, content)
	}
	return v
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// vetArgs writes the documents and returns the flags that pass them.
func vetArgs(t *testing.T, d doc) []string {
	t.Helper()
	dir := t.TempDir()
	args := []string{"-contract", kitchen}
	for _, f := range []struct {
		flag string
		v    map[string]any
	}{{"-values", d.allValues()}, {"-files", d.files}, {"-overlays", d.overlays}} {
		if f.v == nil {
			continue
		}
		p := filepath.Join(dir, f.flag[1:]+".json")
		writeJSON(t, p, f.v)
		args = append(args, f.flag, p)
	}
	return args
}

func tf(t *testing.T, bin, dir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return append(out.Bytes(), errb.Bytes()...), err
	}
	return out.Bytes(), nil
}

// registryUnreachable matches the errors of an init that could not reach a
// provider registry, or the release host behind it (a 502, 503 or 504), as
// opposed to a problem with the configuration.
var registryUnreachable = regexp.MustCompile(`could not connect to registry|Client\.Timeout exceeded|connection reset by peer|TLS handshake timeout|i/o timeout|50[234]\s+(Bad Gateway|Service Unavailable|Gateway Timeout)`)

// tfInit runs init, retrying when a provider registry could not be
// reached: the tests that plan with providers download them, and a
// registry that times out says nothing about the code under test.
func tfInit(t *testing.T, bin, dir string, args ...string) ([]byte, error) {
	t.Helper()
	args = append([]string{"init", "-input=false", "-no-color"}, args...)
	var out []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if out, err = tf(t, bin, dir, args...); err == nil || !registryUnreachable.Match(out) {
			return out, err
		}
		t.Logf("init could not reach a provider registry (attempt %d of 3)", attempt)
		time.Sleep(time.Duration(attempt*5) * time.Second)
	}
	return out, err
}

// newRoot generates the kitchen module and a root module that calls it.
func newRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-out", filepath.Join(dir, "module")}, &stdout, &stderr); code != 0 {
		t.Fatalf("docuconf terraform: exit %d: %s", code, stderr.String())
	}
	return dir
}

func initRoot(t *testing.T, bin, dir string, root any) {
	t.Helper()
	writeJSON(t, filepath.Join(dir, "main.tf.json"), root)
	if out, err := tfInit(t, bin, dir, "-backend=false"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
}

var renderOutputs = []string{"kubernetes_env", "kubernetes_volumes", "kubernetes_volume_mounts", "kubernetes_config_maps", "restart_triggers", "pod_annotations", "pod_labels", "bindings"}

func TestTerraformMatchesRender(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
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
		modules[tc.name] = moduleArgs(tc.doc)
		o := map[string]any{}
		for _, name := range renderOutputs {
			o[name] = "${module." + tc.name + "." + name + "}"
		}
		outputs[tc.name] = map[string]any{"value": o}
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
		Value map[string]any `json:"value"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(append([]string{"vet"}, vetArgs(t, tc.doc)...), &stdout, &stderr); code != 0 {
				t.Fatalf("the case does not pass vet: %s%s", stdout.String(), stderr.String())
			}
			want := renderDoc(t, p, c, tc.doc)
			g := got[tc.name].Value

			compare := func(what string, module, render any) {
				t.Helper()
				if gj, wj := maskHashes(canon(t, module)), maskHashes(canon(t, render)); gj != wj {
					t.Errorf("%s differs from docuconf render:\n module: %s\n render: %s", what, gj, wj)
				}
			}
			compare("kubernetes_env", camel(g["kubernetes_env"]), want.Env)
			compare("kubernetes_volumes", camel(g["kubernetes_volumes"]), want.Volumes)
			compare("kubernetes_volume_mounts", camel(g["kubernetes_volume_mounts"]), want.VolumeMounts)
			compare("restart_triggers", g["restart_triggers"], want.RestartTriggers)
			compare("pod_annotations", g["pod_annotations"], want.PodAnnotations)
			compare("pod_labels", g["pod_labels"], want.PodLabels)

			// ConfigMaps by meaning: the same names but for the hash, the same
			// immutability, and content that parses to the same data.
			cms := map[string]any{}
			for _, cm := range want.ConfigMaps {
				data := map[string]any{}
				for k, x := range cm.Data {
					data[k] = parseContent(t, k, x)
				}
				cms[cm.Metadata.Name] = map[string]any{"data": data, "immutable": cm.Immutable}
			}
			mcms := map[string]any{}
			for name, x := range g["kubernetes_config_maps"].(map[string]any) {
				m := x.(map[string]any)
				data := map[string]any{}
				for k, content := range m["data"].(map[string]any) {
					data[k] = parseContent(t, k, content.(string))
					// A hashed name carries the hash of the content the module wrote.
					if mm := regexp.MustCompile(`-([0-9a-f]{10})$`).FindStringSubmatch(name); mm != nil {
						sum := sha256.Sum256([]byte(content.(string)))
						if h := hex.EncodeToString(sum[:])[:10]; h != mm[1] {
							t.Errorf("ConfigMap %s: hash %s, but its content hashes to %s", name, mm[1], h)
						}
					}
				}
				mcms[name] = map[string]any{"data": data, "immutable": m["immutable"]}
			}
			compare("kubernetes_config_maps", mcms, cms)

			b := g["bindings"].(map[string]any)
			set := func(name string) {
				if x, _ := b[name].(map[string]any); x["set"] != true || x["source"] == nil {
					t.Errorf("bindings[%s] = %v, want it set with a source", name, b[name])
				}
			}
			for name := range tc.allValues() {
				if name != "podAnnotations" && name != "podLabels" {
					set(name)
				}
			}
			for name := range tc.files {
				set(name)
			}
			for _, o := range tc.overlays {
				for name := range o.(map[string]any) {
					set(name)
				}
			}
		})
	}
}

type rendered struct {
	Env             []any          `yaml:"env"`
	Volumes         []any          `yaml:"volumes"`
	VolumeMounts    []any          `yaml:"volumeMounts"`
	RestartTriggers []any          `yaml:"restartTriggers"`
	PodAnnotations  map[string]any `yaml:"podAnnotations"`
	PodLabels       map[string]any `yaml:"podLabels"`
	ConfigMaps      []struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Immutable bool              `yaml:"immutable"`
		Data      map[string]string `yaml:"data"`
	} `yaml:"configMaps"`
}

func renderDoc(t *testing.T, p *platform.Platform, c *platform.Contract, d doc) rendered {
	t.Helper()
	load := func(m map[string]any) cue.Value {
		f := filepath.Join(t.TempDir(), "d.json")
		if m == nil {
			m = map[string]any{}
		}
		writeJSON(t, f, m)
		v, err := p.LoadData(f)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	out, err := p.Render(c, load(d.allValues()), load(d.files), load(d.overlays))
	if err != nil {
		t.Fatal(err)
	}
	var r rendered
	if err := yaml.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// canon is JSON with sorted keys and numbers as written, so YAML and
// Terraform values compare equal when they mean the same. Empty and
// missing collections are alike.
func canon(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var x any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&x); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(x)
	if s := string(b); s == "null" || s == "[]" || s == "{}" {
		return "{}"
	}
	return string(b)
}

type rejectCase struct {
	name string
	doc
	want string // in the module's error
	// vetAccepts marks what only the module rejects: TOML it cannot encode.
	vetAccepts bool
}

func v(name string, values map[string]any, want string) rejectCase {
	return rejectCase{name: name, doc: vals(values), want: want}
}

func injected(extra map[string]any) map[string]any {
	m := map[string]any{"provider": "op"}
	for k, x := range extra {
		m[k] = x
	}
	return map[string]any{"injected": m}
}

var rejectCases = []rejectCase{
	v("secret_as_value", map[string]any{"API_TOKEN": "hunter2"}, "API_TOKEN: set exactly one of secret or injected (a secret is never a literal value"),
	v("list_from_config_map", map[string]any{"PORTS": map[string]any{"configMapKeyRef": map[string]any{"name": "a", "key": "b"}}}, "PORTS: set exactly one of value or injected (a list cannot come from a ConfigMap key"),
	v("int_below_min", map[string]any{"WORKERS": 0}, "WORKERS: 0 is below min 1"),
	v("int_fraction", map[string]any{"WORKERS": 1.5}, "WORKERS: expected an integer, got 1.5"),
	v("string_too_short", map[string]any{"GREETING": "x"}, `GREETING: "x" is 1 characters, below minLength 2`),
	v("string_too_long", map[string]any{"GREETING": "abcdefghijklm"}, "above maxLength 12"),
	v("string_pattern", map[string]any{"GREETING": "abc1"}, `GREETING: "abc1" does not match pattern ^[^0-9]+$`),
	v("enum", map[string]any{"LOG_LEVEL": "trace"}, `LOG_LEVEL: "trace" is not one of debug, info, warn`),
	v("duration_syntax", map[string]any{"TIMEOUT": "ten"}, `TIMEOUT: "ten" is not a duration such as 1m30s`),
	v("duration_above_max", map[string]any{"TIMEOUT": "10m"}, "TIMEOUT: 10m is above max 5m"),
	v("duration_below_min", map[string]any{"TIMEOUT": "999ms"}, "TIMEOUT: 999ms is below min 1s"),
	v("duration_sub_ms", map[string]any{"BACKOFF": "1500us"}, "BACKOFF: 1500us is finer than a millisecond, which the seconds encoding cannot carry"),
	v("url_syntax", map[string]any{"UPSTREAM": "example.com"}, `UPSTREAM: "example.com" is not a URL of the form scheme://...`),
	v("url_scheme", map[string]any{"UPSTREAM": "ftp://example.com"}, `UPSTREAM: scheme "ftp" is not one of http, https`),
	v("float_above_max", map[string]any{"RATIO": 1.5}, "RATIO: 1.5 is above max 1"),
	v("list_min_items", map[string]any{"PORTS": []any{}}, "PORTS: has 0 items, below minItems 1"),
	v("list_max_items", map[string]any{"PORTS": []any{1, 2, 3, 4}}, "PORTS: has 4 items, above maxItems 3"),
	v("list_item_max", map[string]any{"PORTS": []any{70000}}, "PORTS: item 0: 70000 is above itemMax 65535"),
	v("list_item_int", map[string]any{"PORTS": []any{1.5}}, "PORTS: item 0: expected an integer, got 1.5"),
	v("list_item_length", map[string]any{"HOSTS": []any{""}}, `HOSTS: item 0: "" is 0 characters, below itemMinLength 1`),
	v("json_max_length", map[string]any{"FEATURES": map[string]any{"k": strings.Repeat("x", 200)}}, "FEATURES: is 208 characters as compact JSON, above maxLength 200"),
	v("field_path", map[string]any{"NODE_NAME": map[string]any{"fieldRef": map[string]any{"fieldPath": "spec.bogus"}}}, `NODE_NAME: field.field_path "spec.bogus" is not a Downward API field`),
	v("resource", map[string]any{"MEMORY_LIMIT": map[string]any{"resourceFieldRef": map[string]any{"resource": "limits.gpu"}}}, `MEMORY_LIMIT: resource_field.resource "limits.gpu" is not a container resource`),
	v("indexed_injected", map[string]any{"HOSTS": injected(map[string]any{"ref": "op://x"})}, "HOSTS: an indexed list is spread over NAME__0, NAME__1, …, so one injected reference cannot carry it; let the injector set the variables, without ref"),
	v("keyset_indexed_injected", map[string]any{"SIGNING_KEYS": injected(map[string]any{"ref": "op://x"})}, "SIGNING_KEYS: an indexed list is spread over NAME__0"),
	v("provider", map[string]any{"GREETING": map[string]any{"injected": map[string]any{"provider": "Bank Vaults"}}}, "GREETING: injected.provider must name the injector as a lowercase label, such as bank-vaults"),
	v("file_placeholder", map[string]any{"GREETING": injected(map[string]any{"podAnnotations": map[string]any{"a": "{path}"}})}, "only a file input defines"),
	v("shared_placeholder", map[string]any{"podAnnotations": map[string]any{"a": "{input}"}}, "the shared pod annotations and labels cannot use placeholders"),
	v("label_value", map[string]any{"GREETING": injected(map[string]any{"podLabels": map[string]any{"a": "not ok"}})}, `GREETING: pod label a: value "not ok" is not a label value: at most 63 letters`),
	v("annotation_key", map[string]any{"GREETING": injected(map[string]any{"podAnnotations": map[string]any{"bad key": "x"}})}, `GREETING: pod annotation key "bad key" is not a Kubernetes qualified name: an optional DNS-subdomain prefix`),
	v("pod_conflict", map[string]any{
		"GREETING":  injected(map[string]any{"podAnnotations": map[string]any{"a": "1"}}),
		"NODE_NAME": injected(map[string]any{"podAnnotations": map[string]any{"a": "2"}}),
	}, `two inputs set pod annotation ["a"] to different values`),
	v("shared_label_value", map[string]any{"podLabels": map[string]any{"team": "a b"}}, `docuconf_options: pod label team: value "a b" is not a label value`),

	// File inputs.
	{name: "secret_file_inline", doc: doc{files: map[string]any{"keystore": map[string]any{"inline": "x"}}},
		want: "keystore: set exactly one of secret, csi or injected (Terraform ignores inline, config_map, certificate or image here: a secret file comes from a secret store)"},
	{name: "binary_inline", doc: doc{files: map[string]any{"model": map[string]any{"inline": "x"}}},
		want: "model: set exactly one of config_map, secret, csi, image or injected (Terraform ignores inline or certificate here: a binary input cannot take them)"},
	{name: "config_map_key", doc: doc{files: map[string]any{"settings": map[string]any{"configMap": map[string]any{"name": "a"}}}},
		want: `attribute "key" is required`},
	{name: "tls_secret_key", doc: doc{files: map[string]any{"serving-tls": map[string]any{"secret": map[string]any{"name": "a", "key": "tls.crt"}}}},
		want: "serving-tls: a tls input mounts the whole Secret; remove secret.key"},
	{name: "max_size", doc: doc{files: map[string]any{"rules": map[string]any{"inline": `{"k":"` + strings.Repeat("x", 60) + `"}`}}},
		want: "rules: inline content is 68 bytes, above maxSize 60"},
	{name: "invalid_json", doc: doc{files: map[string]any{"rules": map[string]any{"inline": "{nope"}}},
		want: "rules: inline content is not valid json"},
	{name: "invalid_yaml", doc: doc{files: map[string]any{"settings": map[string]any{"inline": "a: [1"}}},
		want: "settings: inline content is not valid yaml"},
	{name: "structured_toml", doc: doc{files: map[string]any{"legacy": map[string]any{"inline": map[string]any{"a": 1}}}},
		want: "legacy: Terraform has no TOML encoder, so give a TOML file's inline content as text", vetAccepts: true},
	{name: "text_too_short", doc: doc{files: map[string]any{"motd": map[string]any{"inline": "hi"}}},
		want: "motd: inline text is 2 characters, below minLength 3"},
	{name: "text_pattern", doc: doc{files: map[string]any{"motd": map[string]any{"inline": "a<b"}}},
		want: "motd: inline text does not match pattern ^[^<]*$"},
	{name: "ca_bundle", doc: doc{files: map[string]any{"ca": map[string]any{"inline": cert}}},
		want: "ca: inline bundle holds 1 certificate, need at least 2"},
	{name: "dns_names", doc: doc{files: map[string]any{"serving-tls": map[string]any{"certificate": map[string]any{"name": "a", "secretName": "a", "dnsNames": []any{"other.example.com"}}}}},
		want: "serving-tls: certificate does not cover kitchen.example.com"},
	{name: "key_algorithm", doc: doc{files: map[string]any{"serving-tls": map[string]any{"certificate": map[string]any{"name": "a", "secretName": "a", "privateKey": map[string]any{"algorithm": "RSA"}}}}},
		want: "serving-tls: certificate key algorithm RSA is not one of ECDSA"},
	{name: "renew_before", doc: doc{files: map[string]any{"serving-tls": map[string]any{"certificate": map[string]any{"name": "a", "secretName": "a", "renewBefore": "24h"}}}},
		want: "serving-tls: certificate renewBefore 24h is less than minRemaining 720h, so the app could see a certificate with too little time left"},
	{name: "pull_policy", doc: doc{files: map[string]any{"model": map[string]any{"image": map[string]any{"reference": "x", "pullPolicy": "Sometimes"}}}},
		want: `model: image.pull_policy "Sometimes" is not one of Always, IfNotPresent, Never`},
	{name: "file_pod_key", doc: doc{files: map[string]any{"ca": injected(map[string]any{"podAnnotations": map[string]any{"bad key/{file}": "x"}})}},
		want: `ca: pod annotation key "bad key/ca.pem" is not a Kubernetes qualified name`},

	// Overlays.
	{name: "overlay_secret", doc: doc{overlays: map[string]any{"appjson": map[string]any{"API_TOKEN": "x"}}},
		want: "API_TOKEN: is secret, so it cannot go in overlay appjson (a ConfigMap); supply it in the environment as a secretKeyRef or injected"},
	{name: "overlay_unknown", doc: doc{overlays: map[string]any{"appjson": map[string]any{"NOPE": 1}}},
		want: "NOPE: is not declared in the contract (check the spelling; in overlay appjson)"},
	{name: "overlay_no_config_key", doc: doc{overlays: map[string]any{"appjson": map[string]any{"DEBUG": true}}},
		want: "DEBUG: has no configKey in the contract, so overlay appjson has nowhere to put it"},
	{name: "overlay_selector", doc: doc{overlays: map[string]any{"appjson": map[string]any{"PROFILE": "dev"}}},
		want: "PROFILE: is the profile selector, which only the environment can set, not overlay appjson"},
	{name: "overlay_and_env", doc: doc{values: map[string]any{"WORKERS": 2}, overlays: map[string]any{"appjson": map[string]any{"WORKERS": 3}}},
		want: "WORKERS: is set both in the environment and in overlay appjson; set it in one place (the environment would win)"},
	{name: "overlay_two", doc: doc{overlays: map[string]any{"appjson": map[string]any{"WORKERS": 3}, "appyaml": map[string]any{"WORKERS": 3}}},
		want: "WORKERS: is set in more than one overlay; set it in one place"},
	{name: "overlay_value", doc: doc{overlays: map[string]any{"appjson": map[string]any{"WORKERS": 0}}},
		want: "WORKERS: 0 is below min 1 (in overlay appjson)"},
	{name: "overlay_type", doc: doc{overlays: map[string]any{"appjson": map[string]any{"WORKERS": "8"}}},
		want: `WORKERS: expected an integer, got "8" (in overlay appjson)`},
	{name: "overlay_reference", doc: doc{overlays: map[string]any{"appyaml": map[string]any{"LOG_LEVEL": map[string]any{"configMapKeyRef": map[string]any{"name": "a", "key": "b"}}}}},
		want: "LOG_LEVEL: overlay appyaml holds values, not references; give a literal, or supply the reference in the environment"},
	{name: "overlay_required", doc: doc{drop: []string{"TENANT"}},
		want: "TENANT: is required, and set neither by the platform nor by the selected profile"},
	{name: "overlay_toml_object", doc: doc{overlays: map[string]any{"apptoml": map[string]any{"FEATURES": map[string]any{"a": 1}}}},
		want: "FEATURES: Terraform has no TOML encoder for objects, so a json value with objects cannot go in TOML overlay apptoml", vetAccepts: true},
}

// ownWording are the cases whose module message names a module attribute
// (field.field_path, docuconf_options), Terraform's own type error, or a
// problem only the module reports this way.
var ownWording = map[string]bool{
	"field_path": true, "resource": true, "shared_placeholder": true,
	"shared_label_value": true, "pod_conflict": true,
	"secret_as_value": true, "list_from_config_map": true,
	"secret_file_inline": true, "binary_inline": true, "config_map_key": true,
	"pull_policy": true, "overlay_selector": true, "overlay_two": true,
}

func TestTerraformRejectsWhatVetRejects(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(append([]string{"vet"}, vetArgs(t, tc.doc)...), &stdout, &stderr)
			switch {
			case tc.vetAccepts && code != 0:
				t.Errorf("docuconf vet rejects the case (exit %d): %s%s", code, stdout.String(), stderr.String())
			case !tc.vetAccepts && code != 1:
				t.Errorf("docuconf vet accepts the case (exit %d), so the module should too: %s", code, stderr.String())
			case !tc.vetAccepts && !ownWording[tc.name] && !strings.Contains(stdout.String(), tc.want):
				// The module words each problem as vet does, except where it
				// names its own attributes rather than the documents' keys.
				t.Errorf("vet words it differently; the module says %q, vet says:\n%s", tc.want, stdout.String())
			}
			writeJSON(t, filepath.Join(dir, "main.tf.json"), map[string]any{"module": map[string]any{"c": moduleArgs(tc.doc)}})
			out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-json")
			if err == nil {
				t.Fatalf("plan succeeded; want an error containing %q", tc.want)
			}
			msgs := diagnostics(out)
			for _, m := range msgs {
				if strings.Contains(m, tc.want) {
					return
				}
			}
			t.Errorf("no error contains %q; got:\n  %s\nvet said:\n%s", tc.want, strings.Join(msgs, "\n  "), stdout.String())
		})
	}
}

// diagnostics are the error messages in terraform's -json output.
func diagnostics(out []byte) []string {
	var msgs []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		var m struct {
			Diagnostic *struct {
				Severity, Summary, Detail string
			} `json:"diagnostic"`
		}
		if json.Unmarshal(line, &m) == nil && m.Diagnostic != nil && m.Diagnostic.Severity == "error" {
			msgs = append(msgs, fmt.Sprintf("%s: %s", m.Diagnostic.Summary, strings.Join(strings.Fields(m.Diagnostic.Detail), " ")))
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, string(out))
	}
	return msgs
}

func TestTerraformRequiredInputs(t *testing.T) {
	bin := terraformBinary(t)
	dir := newRoot(t)
	// REGION is required, but the dev profile supplies it, and TENANT can
	// come from an overlay, so the module cannot insist on either;
	// API_TOKEN and DATABASE_URL can come from nowhere else.
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": map[string]any{"source": "./module"}}})
	out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color", "-json")
	if err == nil {
		t.Fatal("plan succeeded without the required secrets")
	}
	got := strings.Join(diagnostics(out), "\n")
	for _, want := range []string{"API_TOKEN", "DATABASE_URL"} {
		if !strings.Contains(got, want) {
			t.Errorf("no error names %s:\n%s", want, got)
		}
	}
	for _, not := range []string{`"REGION"`, `"TENANT"`} {
		if strings.Contains(got, not) {
			t.Errorf("the module requires %s as an argument:\n%s", not, got)
		}
	}
}

func TestTerraformDeprecatedWarns(t *testing.T) {
	bin := terraformBinary(t)
	for _, tc := range []struct {
		name string
		doc
		want string
	}{
		{"var", vals(map[string]any{"LEGACY_MODE": true}), "warning: LEGACY_MODE is deprecated, and the platform still sets it: use MODE instead"},
		{"var_in_overlay", doc{overlays: map[string]any{"apptoml": map[string]any{"LEGACY_MODE": true}}}, "warning: LEGACY_MODE is deprecated"},
		{"file", doc{files: map[string]any{"motd": map[string]any{"inline": "hello"}}}, "warning: motd is deprecated, and the platform still sets it: use GREETING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRoot(t)
			initRoot(t, bin, dir, map[string]any{"module": map[string]any{"c": moduleArgs(tc.doc)}})
			out, err := tf(t, bin, dir, "plan", "-input=false", "-no-color")
			if err != nil {
				t.Fatalf("a deprecated input should warn, not fail: %v\n%s", err, out)
			}
			if !strings.Contains(strings.Join(strings.Fields(string(out)), " "), tc.want) {
				t.Errorf("no deprecation warning %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestTerraformCheck(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check of a fresh module: exit %d: %s", code, stderr.String())
	}
	// Another generator version alone does not make it stale.
	meta := filepath.Join(dir, "docuconf.json")
	b, _ := os.ReadFile(meta)
	os.WriteFile(meta, bytes.Replace(b, []byte(`"version": "`+version+`"`), []byte(`"version": "0.0.1"`), 1), 0o644)
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-check after a version change: exit %d: %s", code, stderr.String())
	}
	os.WriteFile(filepath.Join(dir, "variables.tf"), []byte("# edited\n"), 0o644)
	stderr.Reset()
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir, "-check"}, &stdout, &stderr); code != 1 {
		t.Fatalf("-check of an edited module: exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "variables.tf") || !strings.Contains(stderr.String(), "-# edited") {
		t.Errorf("-check does not name the stale file and show a diff: %s", stderr.String())
	}
	// A generated file the generator no longer writes is stale, and is
	// removed on the next run; a file the platform wrote is left alone.
	old := filepath.Join(dir, "kubernetes_volumes.tf")
	os.WriteFile(old, []byte("# Code generated by docuconf. DO NOT EDIT.\n"), 0o644)
	mine := filepath.Join(dir, "extra.tf")
	os.WriteFile(mine, []byte("locals {}\n"), 0o644)
	stdout.Reset()
	if code := run([]string{"terraform", "-contract", kitchen, "-out", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("%s was not removed", old)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("%s was removed: %v", mine, err)
	}
}

// The orders example has file inputs of its own; its module must apply.
func TestTerraformOrdersExample(t *testing.T) {
	bin := terraformBinary(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"terraform", "-contract", "../../examples/orders/contract.cue", "-out", filepath.Join(dir, "module")}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	args := map[string]any{
		"source":       "./module",
		"DATABASE_URL": map[string]any{"secret": map[string]any{"name": "orders-db", "key": "url"}},
		"discounts":    map[string]any{"inline": map[string]any{"codes": map[string]any{"WELCOME10": 10}}},
		"serving-tls":  map[string]any{"secret": map[string]any{"name": "orders-tls"}},
	}
	initRoot(t, bin, dir, map[string]any{"module": map[string]any{"orders": args}})
	if out, err := tf(t, bin, dir, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
}
