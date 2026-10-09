package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	examples = "../../spec/cue/examples"
	gateway  = examples + "/gateway_contract.cue"
	billing  = examples + "/billing_contract.cue"
	catalog  = examples + "/catalog_contract.cue"
	ledger   = examples + "/ledger_contract.cue"
)

func docuconf(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return o.String(), e.String(), code
}

// write puts content in a temporary file and returns its path.
func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// requireLines checks that vet failed and printed exactly want.
func requireLines(t *testing.T, out string, code int, want ...string) {
	t.Helper()
	if code != 1 {
		t.Fatalf("exit code %d, want 1; output:\n%s", code, out)
	}
	got := strings.Split(strings.TrimSpace(out), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("vet output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestVetGatewayValid(t *testing.T) {
	out, errOut, code := docuconf(t, "vet", "-contract", gateway,
		"-values", "testdata/gateway/values.yaml", "-files", "testdata/gateway/files.yaml",
		"-policy", "testdata/gateway/policy.cue")
	if code != 0 || out != "gateway: ok\n" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestVetGatewayCertificateMissingDNSName(t *testing.T) {
	files := strings.Replace(read(t, "testdata/gateway/files.yaml"), `"*.example.com"`, `"*.example.org"`, 1)
	out, _, code := docuconf(t, "vet", "-contract", gateway,
		"-values", "testdata/gateway/values.yaml", "-files", write(t, "files.yaml", files))
	requireLines(t, out, code, "serving-tls: certificate does not cover api.example.com")
}

func TestVetGatewayProblems(t *testing.T) {
	values := `LOG_LEVEL: debug
LOG_LEVLE: info
POD_NAMESPACE: {fieldRef: {fieldPath: metadata.namespace}}
GOMEMLIMIT: {fieldRef: {fieldPath: metadata.name}}
RATE_LIMITS: {perMinute: 0, extra: 1}
PARTNER_KEYSTORE_PASSWORD: "hunter2-pasted-literal"
`
	files := read(t, "testdata/gateway/files.yaml") +
		"unknown-file: {inline: x}\n"
	files = strings.Replace(files, "algorithm: ECDSA", "algorithm: Ed25519", 1)
	files = strings.Replace(files, "renewBefore: 720h", "renewBefore: 240h", 1)
	files = strings.Replace(files, "ABCDE-12345-FGHIJ-67890", "bad licence", 1)
	out, _, code := docuconf(t, "vet", "-contract", gateway,
		"-values", write(t, "values.yaml", values), "-files", write(t, "files.yaml", files),
		"-policy", "testdata/gateway/policy.cue")
	requireLines(t, out, code,
		"GOMEMLIMIT: a fieldRef always yields a string, but GOMEMLIMIT is an integer",
		`LOG_LEVEL: "debug" is not allowed by policy`,
		"LOG_LEVLE: is not declared in the contract (check the spelling)",
		"PARTNER_KEYSTORE_PASSWORD: is secret, so it must come from a secretKeyRef, written {secretKeyRef: {name: <secret>, key: <key>}}, or an injector, never a literal or another reference",
		"RATE_LIMITS: does not match its schema: at perMinute: invalid value 0 (out of bound >=1)",
		"license: inline text does not match pattern ^[A-Z0-9]{5}(-[A-Z0-9]{5}){3}\\n?$",
		"serving-tls: certificate key algorithm Ed25519 is not one of ECDSA, RSA",
		"serving-tls: certificate renewBefore 240h is less than minRemaining 720h, so the app could see a certificate with too little time left",
		"unknown-file: is not a file input declared in the contract",
	)
	if strings.Contains(out, "hunter2") {
		t.Fatal("secret value printed")
	}
}

func TestVetBilling(t *testing.T) {
	values := `DATABASE_URL: "postgres://app:hunter2@db/billing"
PORT: 70000
REQUEST_TIMEOUT: 10m
ALLOWED_ORIGINS: []
STRIPE_API_BASE: "http://api.stripe.com"
LOG_LEVEL: 3
`
	out, _, code := docuconf(t, "vet", "-contract", billing, "-values", write(t, "values.yaml", values))
	requireLines(t, out, code,
		"ALLOWED_ORIGINS: has 0 items, below minItems 1",
		"DATABASE_URL: is secret, so it must come from a secretKeyRef, written {secretKeyRef: {name: <secret>, key: <key>}}, or an injector, never a literal or another reference",
		"LOG_LEVEL: 3 is not one of debug, info, warn, error",
		"PORT: 70000 is above max 65535",
		"REQUEST_TIMEOUT: 10m is above max 5m",
		"STRIPE_API_BASE: scheme \"http\" is not one of https",
	)
	if strings.Contains(out, "hunter2") {
		t.Fatal("secret value printed")
	}

	// A required variable left out.
	out, _, code = docuconf(t, "vet", "-contract", billing, "-values", write(t, "v.yaml", "PORT: 8080\n"))
	requireLines(t, out, code,
		"ALLOWED_ORIGINS: is required, and set neither by the platform nor by the selected profile",
		"DATABASE_URL: is required, and set neither by the platform nor by the selected profile",
	)
}

// Values supplied by injectors (SPEC §4.5.1): checked for shape only,
// rendered as the reference or not at all.
func TestInjected(t *testing.T) {
	values := `DATABASE_URL:
  injected: {provider: bank-vaults, ref: "vault:secret/data/billing/db#url"}
ALLOWED_ORIGINS:
  injected: {provider: origins-operator}
`
	out, errOut, code := docuconf(t, "vet", "-contract", billing, "-values", write(t, "values.yaml", values))
	if code != 0 || out != "billing-api: ok\n" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	out, errOut, code = docuconf(t, "render", "-contract", billing, "-values", write(t, "values.yaml", values))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "- name: DATABASE_URL\n    value: vault:secret/data/billing/db#url\n") {
		t.Errorf("reference not rendered as the env value:\n%s", out)
	}
	if strings.Contains(out, "ALLOWED_ORIGINS") {
		t.Errorf("a variable the injector sets itself was rendered:\n%s", out)
	}

	bad := `DATABASE_URL:
  injected: {provider: "Bank Vaults", ref: ""}
ALLOWED_ORIGINS: ["https://a.example.com"]
`
	out, _, code = docuconf(t, "vet", "-contract", billing, "-values", write(t, "bad.yaml", bad))
	requireLines(t, out, code,
		"DATABASE_URL: injected.provider must name the injector as a lowercase label, such as bank-vaults",
		"DATABASE_URL: injected.ref, when given, must be a non-empty string",
	)
}

// Pod annotations and labels for injectors (SPEC §4.5.2): expanded,
// merged into podAnnotations and podLabels, and checked for conflicts.
func TestInjectorPodMetadata(t *testing.T) {
	values := write(t, "values.yaml", `LOG_LEVEL: warn
podAnnotations:
  vault.hashicorp.com/agent-inject: "true"
  vault.hashicorp.com/role: ledger
`)
	files := write(t, "files.yaml", `db-creds:
  injected:
    provider: vault-agent
    podAnnotations:
      vault.hashicorp.com/agent-inject-secret-{input}: database/creds/ledger
      vault.hashicorp.com/agent-inject-file-{input}: "{file}"
      vault.hashicorp.com/secret-volume-path-{input}: "{dir}"
    podLabels:
      example.com/secrets-from: "{input}"
`)
	out, errOut, code := docuconf(t, "vet", "-contract", ledger, "-values", values, "-files", files)
	if code != 0 || out != "ledger-api: ok\n" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	out, errOut, code = docuconf(t, "render", "-contract", ledger, "-values", values, "-files", files)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := `restartTriggers: []
podAnnotations:
  vault.hashicorp.com/agent-inject: "true"
  vault.hashicorp.com/role: ledger
  vault.hashicorp.com/agent-inject-secret-db-creds: database/creds/ledger
  vault.hashicorp.com/agent-inject-file-db-creds: db.json
  vault.hashicorp.com/secret-volume-path-db-creds: /vault/secrets
podLabels:
  example.com/secrets-from: db-creds
`
	if !strings.HasSuffix(out, want) || !strings.Contains(out, "volumes: []\n") {
		t.Fatalf("render output:\n%s\nwant it to end with:\n%s", out, want)
	}

	// Nothing injected: empty maps, so the keys are always there.
	out, _, _ = docuconf(t, "render", "-contract", ledger, "-values", write(t, "v.yaml", "LOG_LEVEL: warn\n"))
	if !strings.HasSuffix(out, "podAnnotations: {}\npodLabels: {}\n") {
		t.Fatalf("render output:\n%s", out)
	}

	bad := write(t, "bad.yaml", `podAnnotations:
  vault.hashicorp.com/role: ledger
podLabels:
  team: payments
DB_PASSWORD:
  injected:
    provider: bank-vaults
    ref: "vault:database/creds/ledger#password"
    podAnnotations:
      vault.hashicorp.com/role: ledger-ro
      vault.hashicorp.com/agent-inject-file-{input}: "{file}"
OTEL_EXPORTER_OTLP_ENDPOINT:
  injected:
    provider: otel-operator
    podAnnotations:
      instrumentation.opentelemetry.io/inject java: "true"
    podLabels:
      team: "{input}/primary"
AZURE_CLIENT_ID:
  injected: {provider: azure-workload-identity, podLabels: {azure.workload.identity/use: true}}
`)
	out, _, code = docuconf(t, "vet", "-contract", ledger, "-values", bad, "-files", files)
	requireLines(t, out, code,
		"AZURE_CLIENT_ID: injected.podLabels azure.workload.identity/use: must be a string (quote true, false and numbers in YAML)",
		"DB_PASSWORD: podAnnotations vault.hashicorp.com/agent-inject-file-DB_PASSWORD: uses {path}, {dir} or {file}, which only a file input defines; a variable has {input}",
		`OTEL_EXPORTER_OTLP_ENDPOINT: pod annotation key "instrumentation.opentelemetry.io/inject java" is not a Kubernetes qualified name: an optional DNS-subdomain prefix (at most 253 characters) and "/", then at most 63 letters, digits, "-", "_" and ".", starting and ending with a letter or digit`,
		`OTEL_EXPORTER_OTLP_ENDPOINT: pod label team: value "OTEL_EXPORTER_OTLP_ENDPOINT/primary" is not a label value: at most 63 letters, digits, "-", "_" and ".", starting and ending with a letter or digit, or empty`,
	)

	// Once each source is valid on its own, conflicts between them.
	bad = write(t, "bad.yaml", `podAnnotations:
  vault.hashicorp.com/role: ledger
DB_PASSWORD:
  injected:
    provider: bank-vaults
    ref: "vault:database/creds/ledger#password"
    podAnnotations: {vault.hashicorp.com/role: ledger-ro}
`)
	conflicting := write(t, "files.yaml", `db-creds:
  injected:
    provider: vault-agent
    podAnnotations: {vault.hashicorp.com/role: ledger-rw}
`)
	out, _, code = docuconf(t, "vet", "-contract", ledger, "-values", bad, "-files", conflicting)
	requireLines(t, out, code,
		"podAnnotations vault.hashicorp.com/role: set to different values by DB_PASSWORD and db-creds",
		"podAnnotations vault.hashicorp.com/role: set to different values by the values document's shared podAnnotations and DB_PASSWORD",
		"podAnnotations vault.hashicorp.com/role: set to different values by the values document's shared podAnnotations and db-creds",
	)
}

// Config-file overlays (SPEC §4.7): values rendered into the app's own
// appsettings format, checked like env values.
func TestOverlays(t *testing.T) {
	values := write(t, "values.yaml", `CATALOG__DBPASSWORD:
  injected: {provider: bank-vaults, ref: "vault:secret/data/catalog/db#password"}
`)
	overlays := write(t, "overlays.yaml", `platform:
  CATALOG__CACHETTL: 90s
  CATALOG__FEATUREDCATEGORIES: [books, games]
  CATALOG__PAGESIZE: 50
  CATALOG__SEARCH__URL: https://search.internal
  LOGGING__LOGLEVEL__DEFAULT: Warning
`)
	out, errOut, code := docuconf(t, "vet", "-contract", catalog, "-values", values, "-overlays", overlays)
	if code != 0 || out != "catalog-api: ok\n" {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	out, errOut, code = docuconf(t, "render", "-contract", catalog, "-values", values, "-overlays", overlays)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if want := read(t, "../../spec/cue/testdata/render/catalogOut.yaml"); out != want {
		t.Fatalf("render differs from spec/cue/testdata/render/catalogOut.yaml:\n%s", out)
	}

	bad := write(t, "bad.yaml", `platform:
  CATALOG__DBPASSWORD: hunter2
  CATALOG__PAGESIZE: 1000
  CATALOG__TRACEHEADER: x-trace
  CATALOG__CACHETTL: {configMapKeyRef: {name: c, key: k}}
  CATALOG__PAGESZE: 5
  CATALOG__SEARCH__URL: https://search.internal
staging:
  LOGGING__LOGLEVEL__DEFAULT: Debug
`)
	env := write(t, "env.yaml", `CATALOG__DBPASSWORD: {secretKeyRef: {name: db, key: pw}}
CATALOG__SEARCH__URL: https://search.internal
`)
	out, _, code = docuconf(t, "vet", "-contract", catalog, "-values", env, "-overlays", bad)
	requireLines(t, out, code,
		"CATALOG__CACHETTL: overlay platform holds values, not references; give a literal, or supply the reference in the environment",
		"CATALOG__DBPASSWORD: is secret, so it cannot go in overlay platform (a ConfigMap); supply it in the environment as a secretKeyRef or injected",
		"CATALOG__PAGESIZE: 1000 is above max 500 (in overlay platform)",
		"CATALOG__PAGESZE: is not declared in the contract (check the spelling; in overlay platform)",
		"CATALOG__SEARCH__URL: is set both in the environment and in overlay platform; set it in one place (the environment would win)",
		"CATALOG__TRACEHEADER: has no configKey in the contract, so overlay platform has nowhere to put it",
		"overlay staging: is not declared in the contract",
	)
	if strings.Contains(out, "hunter2") {
		t.Fatal("secret value printed")
	}
}

func TestRenderGateway(t *testing.T) {
	out, errOut, code := docuconf(t, "render", "-contract", gateway,
		"-values", "testdata/gateway/values.yaml", "-files", "testdata/gateway/files.yaml")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if want := read(t, "../../spec/cue/testdata/render/gatewayOut.yaml"); out != want {
		t.Fatalf("render output differs from spec/cue/testdata/render/gatewayOut.yaml:\n%s", out)
	}
}

// The example chart's generated files must be what the CLI writes, so
// the chart cannot drift from the contract or the meta-schema.
func TestHelmMatchesExampleChart(t *testing.T) {
	dir := t.TempDir()
	out, errOut, code := docuconf(t, "helm", "-contract", gateway, "-chart", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, f := range []string{"values.schema.json", "files/docuconf/contract.json"} {
		if !strings.Contains(out, filepath.Join(dir, f)) {
			t.Errorf("output does not mention %s:\n%s", f, out)
		}
		if got, want := read(t, filepath.Join(dir, f)), read(t, "../../examples/helm/gateway/"+f); got != want {
			t.Errorf("%s differs from examples/helm/gateway/%s; run examples/helm/gateway/generate.sh", f, f)
		}
	}
}

func TestRenderRefusesInvalidValues(t *testing.T) {
	out, errOut, code := docuconf(t, "render", "-contract", billing, "-values", write(t, "v.yaml", "PORT: 0\n"))
	if code != 1 || out != "" || !strings.Contains(errOut, "PORT: 0 is below min 1") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
}

func TestBadContract(t *testing.T) {
	_, errOut, code := docuconf(t, "vet", "-contract", write(t, "c.cue", `
import "docuconf.dev/contract"
contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind: "ConfigContract"
	metadata: {name: "svc", generator: {language: "go", sdk: "x", version: "1"}}
	vars: PORT: {type: "int", description: "port"}
}`))
	want := `vars.PORT.description: invalid value "port" (does not satisfy strings.MinRunes(5))`
	if code != 2 || !strings.Contains(errOut, "is not a valid contract") || !strings.Contains(errOut, want) ||
		strings.Contains(errOut, "disjunction") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

// TestVetSDKContract validates the Go SDK's exported golden contract,
// so the SDK's output and the platform tooling are checked together.
func TestVetSDKContract(t *testing.T) {
	contract := "../../testdata/gateway.golden.cue"
	values := `POD_NAMESPACE: {fieldRef: {fieldPath: metadata.namespace}}
PARTNER_KEYSTORE_PASSWORD: {secretKeyRef: {name: partner, key: password}}
DATABASE_URL: {secretKeyRef: {name: db, key: url}}
ALLOWED_ORIGINS: [https://app.example.com]
EXTRA_PORTS: [9090, 9091]
REQUEST_TIMEOUT: 45s
RATE_LIMITS: {perMinute: 100}
WORKER_COUNT: 8
`
	out, errOut, code := docuconf(t, "vet", "-contract", contract,
		"-values", write(t, "values.yaml", values), "-files", "testdata/gateway/files.yaml")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	out, _, code = docuconf(t, "vet", "-contract", contract,
		"-values", write(t, "values.yaml", strings.Replace(values, "WORKER_COUNT: 8", "WORKER_COUNT: 300", 1)),
		"-files", "testdata/gateway/files.yaml")
	requireLines(t, out, code, "WORKER_COUNT: 300 is above max 127")

	out, errOut, code = docuconf(t, "render", "-contract", contract,
		"-values", write(t, "values.yaml", values), "-files", "testdata/gateway/files.yaml")
	if code != 0 || !strings.Contains(out, "- name: EXTRA_PORTS\n    value: 9090;9091\n") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
}

// TestExport runs docuconf export against a throwaway module that uses
// the SDK from this repository.
func TestExport(t *testing.T) {
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
		"go.mod": "module example.com/app\n\ngo 1.24\n\nrequire github.com/docuconf/docuconf-go v0.0.0\n\nreplace github.com/docuconf/docuconf-go => " + sdk + "\n",
		"config/config.go": `package config

import "github.com/docuconf/docuconf-go"

// Config is the app's configuration.
type Config struct {
	// HTTP listen port.
	Port int ` + "`env:\"PORT\" envDefault:\"8080\" min:\"1\" max:\"65535\"`" + `

	// Licence key for the app.
	License docuconf.TextFile ` + "`file:\"license\" path:\"/etc/app/license/key.txt\"`" + `
}
`,
		"main.go": "package main\n\nimport _ \"example.com/app/config\"\n\nfunc main() {}\n",
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

	out := filepath.Join(dir, "contract.cue")
	_, errOut, code := docuconf(t, "export", "-C", dir, "-pkg", "./config", "-type", "Config",
		"-name", "app", "-app-version", "1.0.0", "-o", out)
	if code != 0 {
		t.Fatalf("export: exit %d: %s", code, errOut)
	}
	got := read(t, out)
	for _, want := range []string{
		"// Code generated by docuconf. DO NOT EDIT.\npackage app\n",
		`description: "HTTP listen port"`,
		`description: "Licence key for the app"`,
		`appVersion: "1.0.0"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("contract lacks %q:\n%s", want, got)
		}
	}
	vo, ve, code := docuconf(t, "vet", "-contract", out, "-values", write(t, "v.yaml", "PORT: 9090\n"))
	if code != 0 {
		t.Fatalf("vet of exported contract: exit %d\n%s%s", code, vo, ve)
	}

	// package main cannot be imported.
	_, errOut, code = docuconf(t, "export", "-C", dir, "-pkg", ".", "-name", "app")
	if code != 2 || !strings.Contains(errOut, "package main") {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	// Config under internal/, the usual Go layout, exports too: the
	// generated program runs inside the module.
	internal := filepath.Join(dir, "internal", "config")
	if err := os.MkdirAll(internal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internal, "config.go"), []byte(files["config/config.go"]), 0o644); err != nil {
		t.Fatal(err)
	}
	internalOut := filepath.Join(dir, "internal.cue")
	_, errOut, code = docuconf(t, "export", "-C", dir, "-pkg", "./internal/config", "-name", "app", "-app-version", "1.0.0", "-o", internalOut)
	if code != 0 {
		t.Fatalf("export of internal/config: exit %d: %s", code, errOut)
	}
	if read(t, internalOut) != got {
		t.Errorf("internal/config exported differently:\n%s", read(t, internalOut))
	}

	// -check: 0 when up to date, 1 with a diff when stale.
	if _, errOut, code := docuconf(t, "export", "-C", dir, "-pkg", "./config", "-name", "app", "-app-version", "1.0.0", "-check", out); code != 0 {
		t.Fatalf("check: exit %d: %s", code, errOut)
	}
	_, errOut, code = docuconf(t, "export", "-C", dir, "-pkg", "./config", "-name", "app", "-app-version", "2.0.0", "-check", out)
	if code != 1 || !strings.Contains(errOut, `+		appVersion: "2.0.0"`) {
		t.Fatalf("check of a stale contract: exit %d: %s", code, errOut)
	}
}

func TestListItemBounds(t *testing.T) {
	values := write(t, "values.yaml", "ORDERS__BROKERS: [kafka-0:9092]\nORDERS__PARTITIONS: [-1, 4294967296]\n")
	out, _, code := docuconf(t, "vet", "-contract", examples+"/orders_contract.cue", "-values", values)
	requireLines(t, out, code, "ORDERS__PARTITIONS: item 1: 4294967296 is above itemMax 2147483647")
}

// cases.json is generated from conformance/load and checked in; SDK
// runners read it, so it must not drift.
func TestConformanceCasesUpToDate(t *testing.T) {
	out, errOut, code := docuconf(t, "conformance", "-dir", "../../conformance", "-check")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
}

// TestVetDeprecated checks that a deprecated input the platform still sets
// is a warning, one line each with its message, and that warnings alone
// do not fail vet (SPEC §4.2).
func TestVetDeprecated(t *testing.T) {
	contract := write(t, "contract.cue", `package svc

import "docuconf.dev/contract"

contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {name: "svc", generator: {language: "go", sdk: "docuconf-go", version: "0.1.0"}}
	vars: {
		PORT: {type: "int", description: "Port to listen on", default: 8080}
		OLD_PORT: {type: "int", description: "Old name of the listen port", deprecated: {message: "Use PORT", replacedBy: "PORT"}}
		WEBHOOK_KEYS: {type: "keySet", description: "Keys that verify webhooks", secret: true, keyMinLength: 32}
	}
	files: licence: {type: "text", description: "Licence key file", path: "/etc/svc/licence/licence.txt", deprecated: message: "Licences are checked online now"}
}
`)
	values := write(t, "values.yaml", "OLD_PORT: 9090\nWEBHOOK_KEYS: {secretKeyRef: {name: webhooks, key: keys}}\n")
	files := write(t, "files.yaml", "licence: {inline: ABC}\n")
	out, errOut, code := docuconf(t, "vet", "-contract", contract, "-values", values, "-files", files)
	want := "warning: OLD_PORT is deprecated, and the platform still sets it: Use PORT (replaced by PORT)\n" +
		"warning: file input licence is deprecated, and the platform still sets it: Licences are checked online now\n" +
		"svc: ok\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n%s\nstderr: %s", code, out, want, errOut)
	}

	// With a problem as well, vet fails and prints both.
	bad := write(t, "bad.yaml", "OLD_PORT: x\nWEBHOOK_KEYS: not-a-reference-0123456789abcdef0123\n")
	out, _, code = docuconf(t, "vet", "-contract", contract, "-values", bad)
	if code != 1 || !strings.HasPrefix(out, "warning: OLD_PORT is deprecated") || !strings.Contains(out, "WEBHOOK_KEYS: is secret") {
		t.Fatalf("exit %d, stdout:\n%s", code, out)
	}
	if strings.Contains(out, "not-a-reference") {
		t.Fatalf("vet printed a key:\n%s", out)
	}

	// Unset, there is nothing to warn about.
	out, _, code = docuconf(t, "vet", "-contract", contract)
	if code != 0 || out != "svc: ok\n" {
		t.Fatalf("exit %d, stdout:\n%s", code, out)
	}
}
