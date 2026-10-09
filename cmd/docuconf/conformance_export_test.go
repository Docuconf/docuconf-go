package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"cuelang.org/go/encoding/yaml"

	"github.com/docuconf/docuconf-go/cmd/docuconf/internal/platform"
)

const (
	exportGolden  = "../../conformance/export/golden.cue"
	exportFixture = "../../conformance/export/fixture.yaml"
)

// TestConformanceExportGoSDK checks the Go SDK's export of the shared
// fixture (kept in the SDK's testdata by TestExportConformanceFixture)
// against the golden contract.
func TestConformanceExportGoSDK(t *testing.T) {
	out, errOut, code := docuconf(t, "conformance", "export", "--golden", exportGolden, "../../testdata/conformance-export.cue")
	if code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "matches") {
		t.Errorf("stdout: %s", out)
	}
}

func TestConformanceExportComparesAsData(t *testing.T) {
	golden, err := os.ReadFile(exportGolden)
	if err != nil {
		t.Fatal(err)
	}
	edit := func(t *testing.T, pairs ...string) string {
		t.Helper()
		s := string(golden)
		for i := 0; i < len(pairs); i += 2 {
			if !strings.Contains(s, pairs[i]) {
				t.Fatalf("golden.cue has no %q", pairs[i])
			}
			s = strings.Replace(s, pairs[i], pairs[i+1], 1)
		}
		return write(t, "exported.cue", s)
	}

	// What the comparison ignores: the generator, encodings (and with them
	// a separator), configKey, defaults written out, a number's form, and
	// schema annotations and the order of required.
	same := edit(t,
		`sdk:      "docuconf-fixture"`, `sdk:      "docuconf-python"`,
		`language: "go"`, `language: "python"`,
		"encoding:      \"csv\"\n\t\t\tseparator:     \";\"", `encoding:      "json"`,
		`encoding:    "go"`, `encoding:    "iso8601"`,
		`configKey: "App:Name"`, `configKey: "app.name"`,
		`description: "Serve the debug endpoints"`, "description: \"Serve the debug endpoints\"\n\t\t\trequired: false\n\t\t\tsecret: false",
		`default:     0.25`, `default:     2.5e-1`,
		`required: ["name", "replicas"]`, "title: \"Settings\"\n\t\t\t\trequired: [\"replicas\", \"name\"]",
	)
	out, errOut, code := docuconf(t, "conformance", "export", "--golden", exportGolden, same)
	if code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, out, errOut)
	}

	// What it does not.
	diff := edit(t,
		`max:         65535`, `max:         65536`,
		`default:     "1m30s"`, `default:     "90s"`,
		`values: ["debug", "info", "warn", "error"]`, `values: ["info", "debug", "warn", "error"]`,
		`minCertificates: 2`, `minCertificates: 1`,
		"\t\tSHARDS: {", "\t\tSHARDZ: {",
		`separator:    ","`+"\n\t\t\tminKeys", `separator:    " "`+"\n\t\t\tminKeys",
	)
	out, _, code = docuconf(t, "conformance", "export", "--golden", exportGolden, diff)
	if code == 0 {
		t.Fatalf("expected differences, got:\n%s", out)
	}
	for _, want := range []string{
		"vars.PORT.max: golden 65535, exported 65536",
		`vars.REQUEST_TIMEOUT.default: golden "1m30s", exported "90s"`,
		"vars.LOG_LEVEL.values[0]",
		"files.trust.minCertificates: golden 2, exported 1",
		"vars.SHARDS: missing",
		"vars.SHARDZ: not in the golden contract",
		`vars.WEBHOOK_KEYS.separator: golden ",", exported " "`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}

	if _, errOut, code := docuconf(t, "conformance", "export", "--golden", exportGolden); code == 0 || !strings.Contains(errOut, "usage") {
		t.Errorf("no exported contract: exit %d, %s", code, errOut)
	}
}

// TestFixtureMatchesGolden checks that fixture.yaml, the fixture's
// description, and golden.cue name the same inputs, with the same types,
// descriptions and details.
func TestFixtureMatchesGolden(t *testing.T) {
	p, err := platform.New()
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.LoadContract(exportGolden)
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.ContractJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	type input struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Details     string `json:"details"`
	}
	var golden struct {
		Metadata struct {
			Name       string `json:"name"`
			AppVersion string `json:"appVersion"`
		} `json:"metadata"`
		Vars  map[string]input `json:"vars"`
		Files map[string]input `json:"files"`
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}

	src, err := os.ReadFile(exportFixture)
	if err != nil {
		t.Fatal(err)
	}
	f, err := yaml.Extract(exportFixture, src)
	if err != nil {
		t.Fatal(err)
	}
	v, err := p.CompileFile(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Metadata struct {
			Name       string `json:"name"`
			AppVersion string `json:"appVersion"`
		} `json:"metadata"`
		Vars  map[string]input `json:"vars"`
		Files map[string]input `json:"files"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Metadata != golden.Metadata {
		t.Errorf("metadata: fixture %+v, golden %+v", fixture.Metadata, golden.Metadata)
	}
	compare := func(kind string, fx, gd map[string]input) {
		for n, want := range gd {
			if got, ok := fx[n]; !ok {
				t.Errorf("%s %s is in golden.cue but not fixture.yaml", kind, n)
			} else if got != want {
				t.Errorf("%s %s: fixture.yaml %+v, golden.cue %+v", kind, n, got, want)
			}
		}
		for n := range fx {
			if _, ok := gd[n]; !ok {
				t.Errorf("%s %s is in fixture.yaml but not golden.cue", kind, n)
			}
		}
	}
	compare("variable", fixture.Vars, golden.Vars)
	compare("file input", fixture.Files, golden.Files)
}
