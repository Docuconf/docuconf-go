# docuconf-go

The Go SDK for [docuconf](https://docuconf.dev): typed configuration contracts between an app and the Kubernetes platform that runs it. Your config struct stays a normal [caarlos0/env](https://github.com/caarlos0/env) struct. docuconf adds descriptions, secrets, constraints and file inputs, checks everything at boot, and exports a CUE contract the platform validates before it deploys.

Documentation: [docuconf.dev](https://docuconf.dev) · [Go guide](https://docuconf.dev/languages/go/)

The CLI is also released as binaries for Linux, macOS and Windows (with `SHA256SUMS`) on the
[GitHub Releases](https://github.com/docuconf/docuconf-go/releases) page, and as a multi-arch image,
`ghcr.io/docuconf/docuconf`, holding a single static binary at `/docuconf`. To add it to your own image:

```dockerfile
COPY --from=ghcr.io/docuconf/docuconf:<v> /docuconf /usr/local/bin/docuconf
```

See [RELEASING.md](RELEASING.md) for the tags and everything that is published.

> **Status:** early, spec `v1alpha1`. Expect breaking changes until v1.

Every Go snippet and YAML file below is taken from [`examples/orders`](examples/orders), which CI builds, tests, exports and smoke-tests on every push.

## 1. Install

```sh
go get github.com/docuconf/docuconf-go@main
go get -tool github.com/docuconf/docuconf-go/cmd/docuconf-export@main
```

The first line adds the SDK (Go 1.24 or later). The second pins the exporter as a [Go tool](https://go.dev/doc/modules/managing-dependencies#tools). It needs only the SDK, so it adds nothing else to your `go.mod`.

No version is tagged yet, so `@main` resolves to a pseudo-version. `go get github.com/docuconf/docuconf-go@v0.1.0` will work from the first release. To build against a local checkout instead:

```sh
go mod edit -replace github.com/docuconf/docuconf-go=../docuconf-go
```

The platform side (`vet`, `render`, `helm`) and the docs generator (`docs`) are the `docuconf` CLI. It needs Go 1.25 because of CUE: `go install github.com/docuconf/docuconf-go/cmd/docuconf@main`.

## 2. Declare

Put the struct in its own package, usually `internal/config`, because the exporter imports it:

```go
package config

import (
	"time"

	"github.com/docuconf/docuconf-go"
)

// Config is everything the orders service reads at boot.
// Doc comments become the descriptions in the contract: the first
// paragraph is the description, and any later paragraphs are its details.
type Config struct {
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error"`

	// Postgres connection string for the orders database.
	DatabaseURL docuconf.Secret `env:"DATABASE_URL,required" schemes:"postgres" maxLength:"2048"`

	// Origins allowed to call the API from a browser.
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" envDefault:"http://localhost:3000" minItems:"1"`

	// Time limit for handling one request.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s" min:"1s" max:"5m"`

	// Number of background workers processing orders.
	//
	// Each worker holds one database connection, so keep it below the
	// database's connection limit divided by the number of replicas.
	// Raise it when the order queue grows faster than it drains.
	WorkerCount int `env:"WORKER_COUNT" envDefault:"4" min:"1" max:"64"`

	// Keys that verify the signature on incoming payment webhooks.
	//
	// A webhook is accepted when it is signed with any key in the list, so
	// the key can be rotated without turning webhooks away. To rotate:
	//
	//  1. add the new key as the second item, and roll out;
	//  2. switch the sender to the new key;
	//  3. remove the old key, and roll out.
	//
	// Each key is 32 to 256 characters, so an empty or truncated key fails
	// at boot. Without this variable, the service rejects every webhook.
	WebhookKeys []docuconf.Secret `env:"WEBHOOK_KEYS" secret:"true" minItems:"1" maxItems:"2" itemMinLength:"32" itemMaxLength:"256"`
}
```

Every input needs a description: the first paragraph of the field's doc comment, or a `desc` tag. Later paragraphs become the input's optional `details`, Markdown that says why the input exists and when to change it. Headings (`# Heading`), lists and indented code blocks in the comment carry over as Markdown. Details only go into generated docs; nothing reads them at runtime.

`docuconf.Secret` is a string that prints `***` everywhere: `%v`, `%+v`, `slog` and JSON. A field of that type is secret in the contract. On a plain `string`, `secret:"true"` does the same for the contract but not for printing. A list of secrets, such as the webhook key set above, is a `[]docuconf.Secret` with `secret:"true"`: the tag makes the list secret in the contract, and the item type keeps each key from printing. Accepting either of two keys is how a key is rotated without downtime ([spec section 6.1](spec/SPEC.md#61-rotation)). A misspelled tag (`secrte`, `mni`) is an error, not a silently dropped rule.

## 3. Run

```go
func main() {
	// On bad configuration, ParseOrExit prints every problem and exits 1.
	cfg := docuconf.ParseOrExit[config.Config]()
	slog.Info("config loaded", "config", docuconf.LogValue(cfg)) // secrets print as ***
```

Use `cfg.DatabaseURL.Reveal()` where you need the secret's value. `docuconf.Redacted(cfg)` returns the configuration as a map with secrets as `***`, ready for a debug endpoint:

```go
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(docuconf.Redacted(cfg))
	})
```

```sh
DATABASE_URL=postgres://orders:pw@localhost:5432/orders go run .
```

## 4. See an error

With `PORT=0` and no `DATABASE_URL`, the service lists every problem, not only the first, and exits 1:

```text
$ PORT=0 DATABSE_URL=x go run .
2026/10/07 13:30:43 WARN docuconf: DATABSE_URL is set but not declared; did you mean DATABASE_URL?
docuconf: 2 configuration problems:
  PORT: 0 is below min 1 (out_of_range)
  DATABASE_URL: is required but not set (missing_required)
```

Each line names the variable and a stable code (`missing_required`, `out_of_range`, `certificate_expiring`, ...). The same lines go to `/dev/termination-log`, so `kubectl describe pod` shows them. Secret values never appear in a message, including messages from your own `TextUnmarshaler` or `FuncMap` parsers. A set variable that looks like a typo of a declared one gets a warning, as `DATABSE_URL` does above.

When you want the error yourself instead of exiting, call `docuconf.Parse[config.Config]()`. It returns the zero `Config` and a `*docuconf.ValidationError` with every violation.

## 5. Test your config

`ParseWithOptions` loads from a map. It never reads or changes the process environment, and it starts nothing you have to stop:

```go
func TestConfig(t *testing.T) {
	_, err := docuconf.ParseWithOptions[config.Config](docuconf.Options{
		// Read these variables instead of the process environment.
		Environment:    map[string]string{"DATABASE_URL": "postgres://u:p@db/orders", "PORT": "0"},
		TerminationLog: "-", // do not write /dev/termination-log
	})
	var verr *docuconf.ValidationError
	if !errors.As(err, &verr) || !verr.Has(docuconf.CodeOutOfRange) {
		t.Fatalf("want out_of_range for PORT=0, got %v", err)
	}
}
```

`Options.FileRoot` points file inputs at a test directory, and `Options.Now` fixes the clock for certificate checks.

## 6. Export the contract

```sh
go tool docuconf-export -pkg ./internal/config -name orders-api -package orders -o contract.cue
```

Commit `contract.cue` and check in CI that it is up to date. `-check` exits 1 and prints a diff when it is stale:

```sh
go tool docuconf-export -pkg ./internal/config -name orders-api -package orders -check contract.cue
```

`-type` defaults to `Config`, and `-name` (the service name, a DNS label) to the last element of your module path. Export compiles and runs a tiny program inside your module (with `go run`), so it needs the Go toolchain and your module's dependencies, but no environment values. The `init` functions of your config package and everything it imports run during export, so keep that package free of side-effecting imports. From Go code, `docuconf.Export[config.Config](docuconf.Meta{Name: "orders-api"})` returns the same contract.

If the app parses with a `Prefix` or a `FuncMap`, declare them on the struct so the app and the exporter cannot disagree:

```go
func (Config) DocuconfOptions() docuconf.Options {
	return docuconf.Options{Prefix: "ORDERS_", FuncMap: parsers}
}
```

## 7. Generate docs

`docuconf docs` turns the contract into documentation, so the docs cannot drift from the code either. Commit the output next to `contract.cue`:

```sh
docuconf docs contract.cue -o CONFIG.md
docuconf docs contract.cue --format agents -o CONFIG.agents.md
```

- [`CONFIG.md`](examples/orders/CONFIG.md) is the reference for developers: a table of contents by group, then each input's type, default, constraints, wire format, allowed sources, examples and details, the file inputs, and what each boot error means.
- [`CONFIG.agents.md`](examples/orders/CONFIG.agents.md) is for AI agents, both coding agents in the app's repository and agents that set deployment values. It starts with hard rules (never write a secret's value anywhere, use each input's wire format, run `docuconf vet` before proposing a change, invent no inputs), then gives one block of `key: value` facts per input. Include it from your `AGENTS.md`, or serve it as an `llms.txt`.
- `--format model` writes [`docs.json`](examples/orders/docs.json), the docs model both renderers read (spec section 14). A website, an MCP server or a Backstage plugin can render from it, and `docuconf docs docs.json` renders it like a contract.

Like the contract, CI can check that the committed docs are current. `--check` exits 1 and prints a diff when a file is stale:

```sh
docuconf docs contract.cue --check CONFIG.md
docuconf docs contract.cue --format agents --check CONFIG.agents.md
```

## 8. Deploy

The platform validates its inputs against `contract.cue` before anything reaches the cluster. It writes a values file, with secrets as references:

```yaml
# values.yaml: what the platform sets each variable to.
LOG_LEVEL: warn
ALLOWED_ORIGINS: [https://shop.example.com]
WORKER_COUNT: 8
DATABASE_URL: # a secret: always a reference, never a literal
  secretKeyRef: {name: orders-db, key: url}
WEBHOOK_KEYS: # a key set: one Secret key holding "old,new" while rotating
  secretKeyRef: {name: orders-webhooks, key: keys}
```

and, when the app has [file inputs](#file-inputs), a files file:

```yaml
# files.yaml: where each file input comes from.
serving-tls:
  certificate: # a cert-manager Certificate
    name: orders-tls
    secretName: orders-tls
    dnsNames: [orders.example.com]
    duration: 2160h
    renewBefore: 720h
discounts:
  inline:
    codes: {WELCOME10: 10, SPRING25: 25}
```

```sh
docuconf vet -contract contract.cue -values deploy/values.yaml -files deploy/files.yaml
docuconf render -contract contract.cue -values deploy/values.yaml -files deploy/files.yaml
```

`vet` prints one line per problem, such as `PORT: 70000 is above max 65535` or `LOG_LEVL: is not declared in the contract (check the spelling)`, and exits 1. `-policy prod.cue` adds an environment's own rules. `render` turns valid inputs into the pod's `env`, volumes and ConfigMaps. A Helm-based platform can use the [docuconf Helm chart](helm), and a Crossplane composition can evaluate the contract in plain CUE. The [walkthrough](examples/walkthrough) shows all three paths end to end, including a policy file.

## Reference

### File inputs

A field of a docuconf file type is a file input, mounted by the platform:

```go
	// Certificate to serve HTTPS with. Without it, the service serves HTTP.
	TLS docuconf.TLSKeyPair `file:"serving-tls" path:"/etc/orders/tls" dnsNames:"orders.example.com" minRemaining:"720h" reload:"watch"`

	// Discount codes accepted at checkout.
	Discounts docuconf.ConfigFile[Discounts] `file:"discounts" path:"/etc/orders/discounts/discounts.yaml"`
}

// Discounts is the content of the discounts file.
type Discounts struct {
	// Percent off for each discount code.
	Codes map[string]int `json:"codes" yaml:"codes"`
}
```

```go
	if cfg.TLS.Present() {
		srv.TLSConfig = &tls.Config{GetCertificate: cfg.TLS.GetCertificate}
```

File types: `TLSKeyPair`, `CABundle`, `Keystore` (PKCS#12), `TextFile`, `BinaryFile` and `ConfigFile[T]` (JSON or YAML, with a JSON Schema generated from `T`). In `T`, a field without `omitempty` (or `omitzero`) that is not a pointer is **required** in the file, unlike plain Go decoding, which would leave it at its zero value. Add `omitempty` to make a field optional.

To run locally with files, `DOCUCONF_FILE_ROOT` remaps every path under a directory: `/etc/orders/tls` is read from `./dev/etc/orders/tls`. This makes a certificate that satisfies the declaration:

```sh
mkdir -p dev/etc/orders/tls dev/etc/orders/discounts
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 90 \
  -subj /CN=orders.example.com -addext subjectAltName=DNS:orders.example.com \
  -keyout dev/etc/orders/tls/tls.key -out dev/etc/orders/tls/tls.crt 2>/dev/null
echo 'codes: {WELCOME10: 10}' > dev/etc/orders/discounts/discounts.yaml
```

Then run with `DOCUCONF_FILE_ROOT=./dev`. `.env` files are read only when listed in `Options.DotEnv`. Set `Options.DotEnvRequired` to make a missing one an error.

### Tags

A doc comment's first paragraph is the description (a `desc` tag is the fallback), and the rest is details. docuconf's tags: `secret`, `min`/`max`, `minLength`/`maxLength` (in characters; `maxLength` also bounds a url or a `JSON[T]` value), `pattern` (RE2), `values` (enum), `schemes` (url), `minItems`/`maxItems`, `itemMin`/`itemMax` on integer lists, and `itemMinLength`/`itemMaxLength` on string lists (`` Shards []int `env:"SHARDS" itemMin:"0" itemMax:"1023"` ``). Integer bounds always include the range caarlos0/env parses the Go type with: an `int` exports `min: -2147483648, max: 2147483647` because caarlos0/env parses it as 32 bits, and a `[]uint16` exports `itemMin: 0, itemMax: 65535`. `JSON[T]` holds a structured variable. The full tag reference is in the [package docs](doc.go).

A comma-separated list keeps empty items, as caarlos0/env does: `ALLOWED_ORIGINS=","` is two empty strings and satisfies `minItems:"1"`. The contract accepts empty items too, so `vet` and boot agree. Check for empty items in your code if they matter.

### Injected secrets and config-file overlays

Secrets injected at startup, by Bank-Vaults' `vault-env`, a wrapper such as `op run`, or vals, need nothing special: docuconf reads the environment as it is when the process starts, after injection, and validates the real values. If a secret variable still holds a reference (it starts with `vault:`, `op://` or `ref+`), the injector did not run, and loading fails with `invalid_type`, naming the variable and the reference scheme but never the value.

caarlos0/env reads only environment variables and does not layer config files, so the Go SDK has no config-file overlays (spec section 4.7) and never exports `overlays`. A platform supplies every Go variable through the environment.

### Contract-first

To validate an environment against a contract with no Go struct, for example one written in CUE by hand and converted with `cue export --out json`, use `LoadContract`. It parses every wire encoding (lists as `csv`, `json` or `indexed`; durations as `go`, `iso8601`, `seconds` or `timespan`) and returns typed values, or a `*ValidationError` with every violation:

```go
	vals, err := docuconf.LoadContract(contractJSON, docuconf.Options{})
	if err != nil {
		return 0, err // a *docuconf.ValidationError with every violation
	}
	timeout := vals["REQUEST_TIMEOUT"].(time.Duration) // int is int64, list is []string or []int64
```

File inputs are loaded too, with the same checks as the Go file types, and returned by input name as those types (`TLSKeyPair`, `CABundle`, `Keystore`, `TextFile`, `BinaryFile`, or `ConfigFile[any]` checked against the contract's schema). A contract with `overlays` or `profiles` is rejected, and so is a `toml` config file or a `jks` keystore, which the Go SDK cannot read.

A generator for another language that builds the contract itself can format it with `docuconf.ContractCUE(contractJSON, pkg)`, which checks it as `LoadContract` does and writes the same `contract.cue` layout as `Export`: header, package, import, variables and files sorted by name. The COBOL SDK's `docuconf-cobol generate` uses it.

### Boot validation for any language: `docuconf exec` and `docuconf check`

A program written in a language without a docuconf SDK (a shell script, a COBOL batch job, a vendor binary) can still be checked at boot against its contract. `docuconf exec` validates the environment and file inputs with the Go SDK's contract-first loader (`LoadContract`, which passes the whole conformance suite), then replaces itself with the program:

```dockerfile
COPY --from=docuconf /docuconf /usr/local/bin/docuconf
COPY contract.cue /etc/docuconf/contract.cue
ENTRYPOINT ["docuconf", "exec", "-contract", "/etc/docuconf/contract.cue", "--", "/app/orders"]
```

- On success it `exec`s the program (`execve`): the program gets docuconf's PID, so it is PID 1 in the container and receives `SIGTERM` directly. docuconf prints nothing.
- The program gets exactly the environment that was validated: the process environment, then any `-env-file` values for variables it does not set (the process environment wins), then the contract's `default` for every variable still unset (empty counts as unset except for `string`). Defaults are exported in the variable's wire encoding (a list joined by its `separator`, or as `json` or `NAME__0`, `NAME__1`; a duration as `go`, `iso8601`, `seconds` or `timespan`), and a file input whose `pathEnv` is unset gets its path. A shell script or vendor binary therefore never repeats the contract's defaults. `-no-defaults` turns this off and passes only what was set.
- On failure it prints every violation on its own line, never a secret value, writes them to the termination log (`DOCUCONF_TERMINATION_LOG`, else `/dev/termination-log` when it exists), and exits 1 without starting the program:

  ```
  docuconf: 2 configuration problems:
    DATABASE_URL: is required but not set (missing_required)
    PORT: 0 is below min 1 (out_of_range)
  ```
- File paths honour each input's `pathEnv` and `DOCUCONF_FILE_ROOT`, as an SDK does.
- `-env-file .env` (repeatable) reads a .env file for local runs. Its values are validated and passed to the program; the process environment wins over it.
- Exit codes: 1 for configuration problems, 2 for a bad contract or usage, 127 when the program cannot be started.

`docuconf check -contract contract.cue` runs the same validation and exits 0 (printing `<name>: ok`) or 1, without starting anything. Use it in an init container or in CI.

The checks are the SDK's: contract-first mode loads variables and files, but not `overlays` or `profiles` (a contract with them is rejected), and it reads `json` and `yaml` config files and `pkcs12` keystores only.

### Conformance

`go test ./...` runs the shared conformance suite (spec section 12) from [`conformance/cases.json`](conformance/cases.json) through `LoadContract`, one subtest per case id. To run another copy of the suite, set `DOCUCONF_CONFORMANCE` to its `cases.json`:

```
go test -run TestConformance -v .
DOCUCONF_CONFORMANCE=/path/to/cases.json go test -run TestConformance .
```

The Go SDK supports every capability tag (`int64`, `json-schema`), so no case is skipped.

### More

- [Contract specification](spec/SPEC.md), with its CUE meta-schema in [`spec/cue`](spec/cue)
- [Implementation plan](docs/PLAN.md) and [edge cases](docs/EDGE_CASES.md)

## Licence

[MIT](LICENSE).
