package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// newApp writes a throwaway module, example.com/billing-api, that keeps
// its configuration in internal/config, the usual Go layout, and uses the
// SDK from this repository.
func newApp(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs go run")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	sdk, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/billing-api\n\ngo 1.24\n\nrequire github.com/docuconf/docuconf-go v0.0.0\n\nreplace github.com/docuconf/docuconf-go => " + sdk + "\n",
		"internal/config/config.go": `package config

import "github.com/docuconf/docuconf-go"

// Config is the app's configuration.
type Config struct {
	// HTTP listen port.
	Port int ` + "`env:\"PORT\" envDefault:\"8080\" min:\"1\" max:\"65535\"`" + `

	// Primary Postgres connection string.
	DatabaseURL docuconf.Secret ` + "`env:\"DATABASE_URL,required\" schemes:\"postgres\"`" + `
}

// DocuconfOptions makes the app and docuconf export agree on the prefix.
func (Config) DocuconfOptions() docuconf.Options {
	return docuconf.Options{Prefix: "BILLING_"}
}

// Routes is not the configuration struct.
type Routes struct{}
`,
		"main.go": "package main\n\nimport _ \"example.com/billing-api/internal/config\"\n\nfunc main() {}\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (offline?): %v\n%s", err, out)
	}
	return dir
}

func export(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return o.String(), e.String(), code
}

func TestExportInternalPackage(t *testing.T) {
	dir := newApp(t)
	out := filepath.Join(dir, "contract.cue")

	// No -name: it defaults to the module path's last element.
	_, errOut, code := export(t, "-C", dir, "-pkg", "./internal/config", "-o", out)
	if code != 0 {
		t.Fatalf("export: exit %d: %s", code, errOut)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		"package billing_api\n",
		`name: "billing-api"`,
		"BILLING_PORT: {",         // the prefix from DocuconfOptions
		"BILLING_DATABASE_URL: {", // a docuconf.Secret field...
		"secret:      true",       // ...is secret without a tag
	} {
		if !strings.Contains(got, want) {
			t.Errorf("contract lacks %q:\n%s", want, got)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "_docuconf_export_*"))
	if len(leftovers) > 0 {
		t.Errorf("left the generated program behind: %v", leftovers)
	}

	// -check passes on an up-to-date contract...
	if _, errOut, code := export(t, "-C", dir, "-pkg", "./internal/config", "-check", out); code != 0 {
		t.Fatalf("check of a fresh contract: exit %d: %s", code, errOut)
	}
	// ...ignores metadata.generator.version, which releases bump...
	bumped := strings.Replace(got, `version:  "`+docuconfVersion(t, got)+`"`, `version:  "99.0.0"`, 1)
	if bumped == got {
		t.Fatalf("test setup: no generator version in\n%s", got)
	}
	if err := os.WriteFile(out, []byte(bumped), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := export(t, "-C", dir, "-pkg", "./internal/config", "-check", out); code != 0 {
		t.Fatalf("check of a contract from another SDK version: exit %d: %s", code, errOut)
	}
	// ...and fails with a diff on a stale one.
	stale := strings.Replace(got, `max:         65535`, `max:         80`, 1)
	if stale == got {
		t.Fatalf("test setup: no max 65535 in\n%s", got)
	}
	if err := os.WriteFile(out, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, code = export(t, "-C", dir, "-pkg", "./internal/config", "-check", out)
	if code != 1 || !strings.Contains(errOut, "is out of date") || !strings.Contains(errOut, "\n-\t\t\tmax:         80\n") || !strings.Contains(errOut, "\n+\t\t\tmax:         65535\n") {
		t.Errorf("check of a stale contract: exit %d:\n%s", code, errOut)
	}
	if b, _ := os.ReadFile(out); string(b) != stale {
		t.Error("-check changed the file")
	}
}

func TestExportErrors(t *testing.T) {
	dir := newApp(t)
	for _, tc := range []struct {
		name  string
		args  []string
		want  string
		avoid []string
	}{{
		name: "unknown type",
		args: []string{"-pkg", "./internal/config", "-type", "Confg"},
		want: "docuconf-export: no type Confg in example.com/billing-api/internal/config (types: Config, Routes); pass the struct's name with -type\n",
	}, {
		name:  "bad name",
		args:  []string{"-pkg", "./internal/config", "-name", "Billing"},
		want:  `docuconf-export: invalid declaration: service name "Billing" must be a DNS label`,
		avoid: []string{"exit status", "_docuconf_export_", "docuconf: invalid"},
	}, {
		name: "package main",
		args: []string{"-pkg", "."},
		want: "is package main, which cannot be imported",
	}, {
		name: "missing package",
		args: []string{"-pkg", "./nope"},
		want: "docuconf-export: cannot find package ./nope:",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, errOut, code := export(t, append([]string{"-C", dir}, tc.args...)...)
			if code != 2 || !strings.Contains(errOut, tc.want) {
				t.Errorf("exit %d, want 2 and %q in:\n%s", code, tc.want, errOut)
			}
			for _, a := range tc.avoid {
				if strings.Contains(errOut, a) {
					t.Errorf("output contains %q:\n%s", a, errOut)
				}
			}
		})
	}
}

func TestNameFromModule(t *testing.T) {
	for mod, want := range map[string]string{
		"example.com/app":             "app",
		"github.com/acme/Billing_API": "billing-api",
		"github.com/acme/orders/v2":   "orders",
		"":                            "",
		"example.com/__":              "",
	} {
		if got := nameFromModule(mod); got != want {
			t.Errorf("nameFromModule(%q) = %q, want %q", mod, got, want)
		}
	}
}

// docuconfVersion returns metadata.generator.version of an exported contract.
func docuconfVersion(t *testing.T, contract string) string {
	t.Helper()
	m := regexp.MustCompile(`generator: \{[^{}]*?version:\s*"([^"]*)"`).FindStringSubmatch(contract)
	if m == nil {
		t.Fatalf("no generator version in\n%s", contract)
	}
	return m[1]
}
