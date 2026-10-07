package docuconf_test

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

const golden = "testdata/gateway.golden.cue"

// generatorVersion matches the value of metadata.generator.version. It is
// docuconf.Version, which every release PR bumps, so golden comparisons
// ignore it.
var generatorVersion = regexp.MustCompile(`(generator:\s*\{[^{}]*?\bversion:\s*)"[^"]*"`)

func withoutGeneratorVersion(cue []byte) string {
	return generatorVersion.ReplaceAllString(string(cue), `${1}"<generator-version>"`)
}

func TestExportGolden(t *testing.T) {
	out, err := docuconf.Export[Gateway](docuconf.Meta{Name: "gateway"})
	require.NoError(t, err)
	if *update {
		require.NoError(t, os.WriteFile(golden, out, 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, withoutGeneratorVersion(want), withoutGeneratorVersion(out), "run go test -run TestExportGolden -update to accept")

	again, err := docuconf.Export[Gateway](docuconf.Meta{Name: "gateway"})
	require.NoError(t, err)
	require.Equal(t, string(out), string(again), "export must be deterministic")
}

func TestGoldenComparisonIgnoresOnlyTheGeneratorVersion(t *testing.T) {
	out, err := docuconf.Export[Gateway](docuconf.Meta{Name: "gateway"})
	require.NoError(t, err)
	bumped := strings.Replace(string(out), `"`+docuconf.Version+`"`, `"99.0.0"`, 1)
	require.NotEqual(t, string(out), bumped)
	require.Equal(t, withoutGeneratorVersion(out), withoutGeneratorVersion([]byte(bumped)))
	renamed := strings.Replace(string(out), `sdk:      "docuconf-go"`, `sdk:      "other"`, 1)
	require.NotEqual(t, string(out), renamed)
	require.NotEqual(t, withoutGeneratorVersion(out), withoutGeneratorVersion([]byte(renamed)))
}

// TestExportCueVet checks the exported contract against the meta-schema
// with the cue CLI, in a temporary copy of the spec's CUE module.
func TestExportCueVet(t *testing.T) {
	cue := cueBinary(t)
	out, err := docuconf.Export[Gateway](docuconf.Meta{Name: "gateway", AppVersion: "1.2.3"})
	require.NoError(t, err)

	dir := t.TempDir()
	copyDir(t, "spec/cue/cue.mod", filepath.Join(dir, "cue.mod"))
	copyDir(t, "spec/cue/contract", filepath.Join(dir, "contract"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "gateway"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gateway", "contract.cue"), out, 0o644))

	cmd := exec.Command(cue, "vet", "-c", "./gateway/contract.cue")
	cmd.Dir = dir
	msg, err := cmd.CombinedOutput()
	require.NoError(t, err, "cue vet failed:\n%s", msg)

	// And a broken contract is rejected, so the test can fail.
	bad := strings.Replace(string(out), `type:        "int"`, `type:        "integer"`, 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gateway", "contract.cue"), []byte(bad), 0o644))
	cmd = exec.Command(cue, "vet", "-c", "./gateway/contract.cue")
	cmd.Dir = dir
	_, err = cmd.CombinedOutput()
	require.Error(t, err)
}

func cueBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CUE"); p != "" {
		return p
	}
	if p, err := exec.LookPath("cue"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, "go", "bin", "cue")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	t.Skip("cue binary not found; install cuelang.org/go/cmd/cue@v0.17.1")
	return ""
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.CopyFS(dst, os.DirFS(src)))
}

func TestExportNeedsDescriptions(t *testing.T) {
	type noDocs struct {
		Port int    `env:"PORT"`
		Name string `env:"NAME" desc:"abc"`
	}
	_, err := docuconf.Export[noDocs](docuconf.Meta{Name: "svc"})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	require.Len(t, de.Problems, 2)
	require.Contains(t, de.Problems[0], "NAME")
	require.Contains(t, de.Problems[1], "PORT")
}

func TestDeclarationErrors(t *testing.T) {
	type bad struct {
		// Lower-case names are not environment variable names.
		Lower string `env:"lower"`
		// A required variable cannot have a default.
		Both int `env:"BOTH,required" envDefault:"1"`
		// Defaults must satisfy their own constraints.
		Port int `env:"PORT" envDefault:"0" min:"1"`
		// Patterns must be RE2.
		Look string `env:"LOOK" pattern:"(?=x)"`
		// min does not apply to strings.
		Str string `env:"STR" min:"1"`
		// Secrets have no defaults.
		Token string `env:"TOKEN" secret:"true" envDefault:"x"`
		// Maps are not a contract type.
		Labels map[string]string `env:"LABELS"`
		// A file input under a reserved directory.
		CA docuconf.CABundle `file:"ca" path:"/etc/ca.pem"`
		// Item bounds apply to integer lists only.
		Names []string `env:"NAMES" itemMin:"1"`
		// Item bounds must fit the element type.
		Small []int8 `env:"SMALL" itemMax:"300"`
		// itemMin must not exceed itemMax.
		Ids []int64 `env:"IDS" itemMin:"5" itemMax:"4"`
	}
	_, err := docuconf.ParseWithOptions[bad](docuconf.Options{Environment: map[string]string{}})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	all := strings.Join(de.Problems, "\n")
	for _, want := range []string{
		"lower (bad.Lower): variable name must match",
		"BOTH (bad.Both): a required variable must not have a default",
		"PORT (bad.Port): default 0 is below min 1",
		"LOOK (bad.Look): pattern is not valid RE2",
		"STR (bad.Str): tag min does not apply to a string variable",
		"TOKEN (bad.Token): a secret variable must not have a default",
		"LABELS (bad.Labels): map[string]string is not a contract type",
		"file input ca would be mounted at /etc",
		"NAMES (bad.Names): itemMin and itemMax apply only to lists of integers",
		"SMALL (bad.Small): itemMax 300 is outside the range of int8",
		"IDS (bad.Ids): itemMin is greater than itemMax",
	} {
		require.Contains(t, all, want)
	}
}

const contractCUEGolden = "testdata/contractcue.golden.cue"

// TestContractCUE formats a contract built outside Go, with its keys in
// no particular order, as Export would write it.
func TestContractCUE(t *testing.T) {
	in, err := os.ReadFile("testdata/contractcue.json")
	require.NoError(t, err)
	out, err := docuconf.ContractCUE(in, "")
	require.NoError(t, err)
	if *update {
		require.NoError(t, os.WriteFile(contractCUEGolden, out, 0o644))
	}
	want, err := os.ReadFile(contractCUEGolden)
	require.NoError(t, err)
	require.Equal(t, string(want), string(out))

	cue := cueBinary(t)
	dir := t.TempDir()
	copyDir(t, "spec/cue/cue.mod", filepath.Join(dir, "cue.mod"))
	copyDir(t, "spec/cue/contract", filepath.Join(dir, "contract"))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "orders"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orders", "contract.cue"), out, 0o644))
	cmd := exec.Command(cue, "vet", "-c", "./orders/contract.cue")
	cmd.Dir = dir
	msg, err := cmd.CombinedOutput()
	require.NoError(t, err, "cue vet failed:\n%s", msg)
}

func TestContractCUERejectsInvalidContracts(t *testing.T) {
	_, err := docuconf.ContractCUE([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "Orders"}, "vars": {"PORT": {"type": "int", "description": "Port", "min": 2, "max": 1}}}`), "")
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	require.Contains(t, de.Error(), "PORT: description must be at least 5 characters")
	require.Contains(t, de.Error(), "PORT: min is greater than max")

	_, err = docuconf.ContractCUE([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "Orders"}, "vars": {}}`), "")
	require.ErrorContains(t, err, `metadata.name "Orders" must be a DNS label`)
}

// TestExportDetails checks that a doc comment's first paragraph is the
// description and the rest is details, and that details are bounded.
func TestExportDetails(t *testing.T) {
	out, err := docuconf.Export[Gateway](docuconf.Meta{Name: "gateway"})
	require.NoError(t, err)
	require.Contains(t, string(out), `description: "Upstream request timeout"
			details:     "The gateway gives up on an upstream after this long and answers 504. Raise it for slow batch endpoints; keep it below the load balancer's idle timeout.\n\n# Choosing a value\n\nMeasure the upstream's p99 latency first:\n\n\thistogram_quantile(0.99, upstream_seconds_bucket)"`)
	// A one-paragraph comment has no details.
	require.NotContains(t, string(out), `description: "HTTP listen port"
			details:`)

	_, err = docuconf.Export[longDetails](docuconf.Meta{Name: "svc"})
	var de *docuconf.DeclarationError
	require.True(t, errors.As(err, &de), "%v", err)
	require.Len(t, de.Problems, 1)
	require.Contains(t, de.Problems[0], "PORT")
	require.Contains(t, de.Problems[0], "details may have at most 4000")
}

func TestContractDetails(t *testing.T) {
	for _, c := range []struct{ details, problem string }{
		{`"  \n "`, "PORT: details must not be blank"},
		{`"` + strings.Repeat("é", 4001) + `"`, "PORT: details must be at most 4000 characters"},
	} {
		_, err := docuconf.ContractCUE([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
			"metadata": {"name": "orders"}, "vars": {"PORT": {"type": "int", "description": "Listen port", "details": `+c.details+`}}}`), "")
		require.ErrorContains(t, err, c.problem)
	}
	out, err := docuconf.ContractCUE([]byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "orders"}, "vars": {"PORT": {"type": "int", "description": "Listen port", "details": "Why: *because*."}},
		"files": {"routes": {"type": "text", "description": "Routing table", "path": "/etc/app/routes.txt", "details": "Reloaded on change."}}}`), "")
	require.NoError(t, err)
	require.Contains(t, string(out), `description: "Listen port"
			details:     "Why: *because*."`)
	require.Contains(t, string(out), `details:     "Reloaded on change."`)
}
