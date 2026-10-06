# docuconf-go

The Go SDK for [docuconf](https://github.com/docuconf): typed configuration contracts between an app and the Kubernetes platform that runs it. Your config struct stays a normal [caarlos0/env](https://github.com/caarlos0/env) struct; docuconf adds descriptions, secrets, constraints and file inputs, checks everything at boot, and exports a CUE contract the platform validates before it deploys.

```
go get github.com/docuconf/docuconf-go
go install github.com/docuconf/docuconf-go/cmd/docuconf@latest   # the CLI
```

> **Status:** early, spec `v1alpha1`. Expect breaking changes until v1.

- [Contract specification](spec/SPEC.md), with its CUE meta-schema in [`spec/cue`](spec/cue)
- [Implementation plan](docs/PLAN.md) and [edge cases](docs/EDGE_CASES.md)

## Declare

```go
type Config struct {
	// Primary Postgres connection string.
	DatabaseURL string `env:"DATABASE_URL,required" secret:"true" schemes:"postgres,postgresql"`

	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Minimum log level emitted.
	LogLevel string `env:"LOG_LEVEL" envDefault:"info" values:"debug,info,warn,error"`

	// Upstream request timeout.
	Timeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s" max:"5m"`

	// Certificate the service serves HTTPS with.
	TLS docuconf.TLSKeyPair `file:"serving-tls,required" path:"/etc/app/tls" dnsNames:"api.example.com" minRemaining:"720h" reload:"watch"`

	// Routing table.
	Routes docuconf.ConfigFile[Routes] `file:"routes,required" path:"/etc/app/routes/routes.yaml"`
}
```

Doc comments are the descriptions (a `desc` tag is the fallback). docuconf's tags: `secret`, `min`/`max`, `minLength`/`maxLength`, `pattern` (RE2), `values` (enum), `schemes` (url), `minItems`/`maxItems`. File types: `TLSKeyPair`, `CABundle`, `Keystore` (PKCS#12), `TextFile`, `BinaryFile`, `ConfigFile[T]` (JSON or YAML, with a JSON Schema generated from `T`), plus `JSON[T]` for structured variables. The full tag reference is in the [package docs](doc.go).

## Load

```go
cfg, err := docuconf.Parse[Config]() // env.ParseAs, then every docuconf check
if err != nil {
	log.Fatal(err) // every violation at once, e.g. "PORT: 70000 is above max 65535 (out_of_range)"
}
srv := &http.Server{TLSConfig: &tls.Config{GetCertificate: cfg.TLS.GetCertificate}}
```

Violations carry stable codes (`missing_required`, `certificate_expiring`, ...), never include secret values, and go to `/dev/termination-log` too. `DOCUCONF_FILE_ROOT` remaps file paths for local runs; `.env` files are read only when passed in `Options.DotEnv`.

### Injected secrets and config-file overlays

Secrets injected at startup, by Bank-Vaults' `vault-env`, a wrapper such as `op run`, or vals, need nothing special: docuconf reads the environment as it is when the process starts, after injection, and validates the real values. If a secret variable still holds a reference (it starts with `vault:`, `op://` or `ref+`), the injector did not run, and loading fails with `invalid_type`, naming the variable and the reference scheme but never the value.

caarlos0/env reads only environment variables and does not layer config files, so the Go SDK has no config-file overlays (spec section 4.7) and never exports `overlays`. A platform supplies every Go variable through the environment.

## Export and validate

```
docuconf export -pkg ./internal/config -type Config -name billing-api -o contract.cue
docuconf vet    -contract contract.cue -values values.yaml -files files.yaml -policy prod.cue
docuconf render -contract contract.cue -values values.yaml -files files.yaml
```

`vet` prints one line per problem, such as `serving-tls: certificate does not cover api.example.com`. From Go code, `docuconf.Export[Config](docuconf.Meta{Name: "billing-api"})` returns the same contract.

The old builder-based generator in `gen/` and `LoadDotEnv` are deprecated.

## Licence

[MIT](LICENSE).
