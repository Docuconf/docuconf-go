# docuconf: multi-language implementation plan

Companion to [`spec/SPEC.md`](../spec/SPEC.md). This plan covers how to get from today's Go-only codegen library to a contract standard with SDKs for several languages, a platform integration, and a website.

## 1. Where we are

The current repository, `github.com/autoscalerhq/docuconf`, is a Go code generator:

- A builder API declares options and generates a Go struct plus a Markdown file.
- `LoadDotEnv` fills the struct from a `.env` file through koanf.

What carries over: the idea that every option must have a description, the generated docs, and service-to-service sharing.

What has to change to meet the spec:

| Today | Spec |
|---|---|
| Reads only `.env` files. | Reads the process environment; `.env` is a dev-only opt-in (SPEC §11.4). |
| `log.Fatalf` on load errors. | Returns every violation with error codes (§11.5). |
| A global koanf instance, so repeated loads merge into each other. | No global state. |
| `Required` is documentation only. | Enforced at boot and at deploy (§7, §11). |
| Types: string, int, bool, float. | Adds duration, url, enum, list and constraints (§4.3). |
| No CUE output. | The CUE contract is the primary artifact. |
| The module path does not match the GitHub repo (`bearbinary/docuconf-go`). | Decide on the canonical org and module path before the first tagged release. |

## 2. Key decisions (recommended)

1. **Code-first declarations, CUE contract as the exchange format.** Rails and .NET teams will not write CUE, so they declare in their own language and the SDK exports CUE. A contract-first mode (SPEC §11) serves teams that prefer to write CUE.
2. **The contract is data, not CUE expressions.** Constraints are fields (`min: 1`), not CUE syntax (`>=1`). Every SDK can emit them without a CUE library, the compatibility diff stays simple, and the meta-schema turns the data into real CUE constraints. This is prototyped and tested in `spec/cue/`.
3. **One Go toolchain for everything platform-side.** CUE's only full implementation is Go, so the `docuconf` CLI (vet, render, diff, docs) and the Crossplane function share one Go library. Language SDKs never need CUE.
4. **Monorepo.** The spec, the conformance suite, the SDKs and the website change together; one repo keeps them in step. Each SDK publishes to its own registry with its own version, managed by release-please in monorepo mode.
5. **Conformance suite before the second SDK.** Without shared golden files, five SDKs will drift on edge cases such as empty strings and bool parsing.
6. **Feature flags are out of scope** (SPEC §10), with OpenFeature as the recommended route and a possible `FlagContract` kind later.

## 3. Repository layout

```
docuconf/
├── spec/                    SPEC.md, CUE meta-schema, examples (exists on this branch)
├── conformance/             language-neutral test cases + golden files
├── go/
│   ├── docuconf/            Go SDK (struct tags, loader, export)
│   ├── cue/                 shared Go library: validate, render, diff, error formatting
│   ├── cmd/docuconf/        CLI
│   └── function-docuconf/   Crossplane composition function
├── sdks/
│   ├── typescript/          @docuconf/core (+ adapters later)
│   ├── ruby/                docuconf + docuconf-rails
│   ├── dotnet/              Docuconf + Docuconf.SourceGenerator
│   └── python/              later
├── examples/                one sample app per language + a kind-based platform demo
└── website/
```

## 4. Phases

Sizes are relative: S is about a week of one engineer's time, M two to three weeks, L more than a month.

### Phase 0: Lock the contract (S)

- Settle the open questions in SPEC §13.
- Grow `spec/cue` into the published CUE module `docuconf.dev/contract`.
- Write the conformance cases: about 15 contract cases (the `testdata/invalid` set is the start), about 40 load cases covering every type, every error code, empty strings and bool spellings, and the export fixture with its `golden.cue`.
- CI runs `spec/cue/test.sh`.

**Done when:** the conformance cases cover every rule in the spec, and the CUE module validates them.

### Phase 1: Go reference SDK and CLI (M)

- Rewrite the Go SDK around struct tags, replacing the generator program:

  ```go
  type Config struct {
      DatabaseURL string        `env:"DATABASE_URL" required:"true" secret:"true" schemes:"postgres,postgresql" desc:"Primary Postgres connection string"`
      Port        int           `env:"PORT" default:"8080" min:"1" max:"65535" desc:"HTTP listen port"`
      Timeout     time.Duration `env:"REQUEST_TIMEOUT" default:"30s" max:"5m" desc:"Upstream request timeout"`
  }

  cfg, err := docuconf.Load[Config]()            // process env; docuconf.WithDotEnv(".env") for dev
  //go:generate go run github.com/<org>/docuconf/go/cmd/docuconf export --type Config --name billing-api
  ```

  Keep the current builder API working for one release, marked deprecated.
- Build the `docuconf` CLI:
  - `export`: writes the contract.
  - `vet`: validates values and policy, with readable errors (SPEC §7).
  - `render`: produces a Kubernetes env list.
  - `diff`: classifies changes (SPEC §9).
  - `docs`: renders Markdown or HTML.
  - `push` and `pull`: move the contract as an OCI artifact by image digest, using oras-go.
- Add the conformance runner for Go.

**Done when:** the Go SDK passes conformance, and `docuconf vet` runs in a GitHub Action against a sample GitOps repo.

### Phase 2: Platform integration (M)

- Build `function-docuconf`, a Crossplane composition function that:
  1. reads `spec.image` (by digest) and `spec.env` from the composite resource,
  2. pulls the contract from the registry and caches it by digest,
  3. unifies it with the environment policy, supplied as an `EnvironmentConfig` or function input,
  4. on failure, sets a `ContractValid=False` condition on the XR with one line per variable,
  5. on success, emits the rendered `env` for the Deployment that later pipeline steps compose.

  It is a function rather than plain `function-cue` because composition-time OCI pulls and readable errors need Go code. Teams that already use `function-cue` can import the meta-schema and inline the contract instead.
- Add a GitHub Action and a GitLab CI template for `docuconf vet` and `docuconf diff`.
- Ship an example in `examples/platform`: a kind cluster, Crossplane, an `XApp` composite resource definition, and the composition. One claim deploys; a second, with a bad `PORT`, is rejected with a clear condition.

**Done when:** the kind demo runs from one `make` target in CI.

### Phase 3: Language SDKs (L, parallelisable)

These can run in parallel once Phase 0's conformance suite and Phase 1's CLI exist. Each SDK ships with framework integration, docs and an example app, and passes conformance.

**TypeScript, `@docuconf/core`.** Type inference does the work:

```ts
export const config = defineConfig({
  name: "billing-api",
  vars: {
    DATABASE_URL: env.url({ description: "Primary Postgres connection string", required: true, secret: true, schemes: ["postgres", "postgresql"] }),
    PORT: env.int({ description: "HTTP listen port", default: 8080, min: 1, max: 65535 }),
    LOG_LEVEL: env.enum({ description: "Minimum log level emitted", values: ["debug", "info", "warn", "error"], default: "info" }),
  },
});
const cfg = config.load(); // cfg.PORT: number, cfg.LOG_LEVEL: "debug" | "info" | "warn" | "error"
```

- Export with `npx docuconf export src/config.ts`.
- Zero runtime dependencies.
- Adapters later: Zod, NestJS `ConfigModule`, and Next.js, which must keep server-only variables out of the client bundle.

**Ruby, `docuconf` and `docuconf-rails`.**

```ruby
# config/docuconf.rb
Docuconf.define "billing-api" do
  url  :DATABASE_URL, "Primary Postgres connection string", required: true, secret: true, schemes: %w[postgres postgresql]
  int  :PORT, "HTTP listen port", default: 8080, min: 1, max: 65535
  enum :LOG_LEVEL, "Minimum log level emitted", values: %w[debug info warn error], default: "info"
end

Docuconf.config.port # => 8080
```

- A Railtie validates in `before_initialize`.
- Export with `bin/rails docuconf:export`.
- **Rails gotcha:** `assets:precompile` and other build-time tasks boot the app without production env vars. Validation needs a documented skip: on by default for `rails server` and `console`, off for asset tasks, with an explicit override.

**.NET, `Docuconf` and `Docuconf.SourceGenerator`.**

```csharp
[EnvContract("billing-api")]
public sealed partial class BillingConfig
{
    [Env("DATABASE_URL", "Primary Postgres connection string", Required = true, Secret = true), UrlSchemes("postgres", "postgresql")]
    public required Uri DatabaseUrl { get; init; }

    [Env("PORT", "HTTP listen port"), Range(1, 65535)]
    public int Port { get; init; } = 8080;
}

builder.Services.AddDocuconf<BillingConfig>(); // binds IOptions<BillingConfig>, ValidateOnStart
```

- A source generator emits `contract.cue` at compile time, so export needs no running app and works in `dotnet build`. It reads defaults from property initializers.
- It is AOT- and trimming-safe because it uses no reflection.

**Python (later), `docuconf`.** Pydantic-settings is the natural base; ship an adapter that exports a contract from a `BaseSettings` model, plus a minimal dependency-free core.

**Java/Kotlin (later).** Use Spring `@ConfigurationProperties` with an annotation processor for export.

**Done when:** each SDK passes conformance, and its example app deploys through the Phase 2 platform demo.

### Phase 4: Website and v1beta1 (M)

See section 5. In this phase the spec also moves to `v1beta1`, after feedback from at least two languages in real use.

## 5. Website

**Stack:** Astro Starlight.

- It is static, fast and Markdown-first.
- Its built-in synced tabs suit "show this in Go, TypeScript, Ruby, .NET", and the choice persists across pages.
- It has search built in, through Pagefind. Versioned docs come from the community `starlight-versions` plugin.

**Hosting:** Cloudflare Pages or GitHub Pages, built from `website/` on every merge, with previews on pull requests.

**Domain:** the spec uses `docuconf.dev` as the CUE module path. Check it is available and register it before Phase 0 ends, because the module path is hard to change once published.

| Section | Content |
|---|---|
| Home | One-paragraph pitch, the lifecycle diagram, a 60-second example showing a contract and a rejected deploy. |
| Concepts | Contracts, values and policy; the three validation points; why feature flags are separate. |
| Spec | `SPEC.md` rendered and versioned per `apiVersion`, plus a CUE meta-schema reference. |
| Languages | One guide per SDK: install, declare, load, export, framework integration. Code tabs on shared pages. |
| Platform | Crossplane function setup, the GitHub Action, policy recipes (prod hardening, secret-only database URLs), OCI distribution. |
| CLI reference | Generated from the CLI's help text. |
| Playground (later) | Paste a contract and values, and see validation and rendered env in the browser through CUE compiled to WebAssembly. |
| Community | Contributing, the governance model, how to propose a new type, how to add an SDK. |

The guide to adding an SDK matters most for an open-source project: the conformance suite turns "is this SDK correct?" into a mechanical check, so the community can maintain SDKs for other languages.

## 6. Risks

| Risk | Mitigation |
|---|---|
| CUE's learning curve and noisy errors put off platform teams. | The CLI and the function translate errors (SPEC §7). App teams never write CUE. |
| SDKs drift on parsing edge cases. | The conformance suite gates every SDK release. |
| Contract and image skew in production. | Distribute contracts by image digest (SPEC §8). The function refuses an image with no contract unless a namespace opts out. |
| A new required variable breaks deploys during rollout. | `docuconf diff` in app CI flags it, and the platform pull request lands values before the app ships. |
| Teams cram feature flags into env. | A naming lint warning, clear docs, and a later `FlagContract`. |
| Removing a variable breaks validation, because values still set it. | A deprecation workflow. A non-strict mode is an open question (SPEC §13.2). |

## 7. Decisions needed from you

1. **Org, module path and domain.** `autoscalerhq` or `bearbinary`, and `docuconf.dev` or another domain.
2. **Licence.** Apache-2.0 is recommended: it is standard for Kubernetes-adjacent projects and has a patent grant.
3. **SDK order after Go.** The plan assumes TypeScript, then Ruby, then .NET, based on ecosystem size. Change it if your own services lean another way.
4. **Distribution.** Whether your registries support the OCI 1.1 referrers API, or at least the tag-schema fallback that oras uses, or whether to start with GitOps-committed contracts. Support varies by registry and version, so test yours.
5. **The SPEC §13 open questions**, especially strict unknown-variable handling and optional variables without defaults.
