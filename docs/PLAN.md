# docuconf: multi-language implementation plan

Companion to [`spec/SPEC.md`](../spec/SPEC.md). This plan covers how to get from today's Go-only code generator to a contract standard with an SDK for each language, a platform integration, a website, and a GitHub organization of its own.

## 1. Principles

1. **Extend each language's best env library; never replace it.** A Rails team already uses anyway_config, and a .NET team already uses the Options pattern. docuconf adds descriptions, secrets, constraints and contract export to the declaration they already write (SPEC §11.1). Adoption should be "add a package and some metadata", not "rewrite your config".
2. **The CUE contract is the exchange format.** App teams never write CUE. Platform teams get one format for every language.
3. **The contract is data.** Constraints are fields (`min: 1`), not CUE expressions, so every SDK can emit them without a CUE library. This is prototyped and tested in `spec/cue/`.
4. **The contract records wire formats; it does not dictate them.** Each host library keeps its own list and duration parsing, and the platform renders to match (SPEC §5).
5. **Conformance gates every SDK.** It is the only way independently maintained SDKs stay consistent.
6. **Feature flags are out of scope** (SPEC §10). OpenFeature is the recommended route.

## 2. Host library per language

| Language | Host library | Why this one | docuconf package |
|---|---|---|---|
| Go | [caarlos0/env](https://github.com/caarlos0/env) v11 | The most widely used struct-tag env parser. It has no dependencies, is feature-complete and is still maintained. It replaces koanf, which is a multi-source loader rather than an env library. | `github.com/docuconf/docuconf-go` |
| TypeScript | [T3 Env](https://env.t3.gg) with Standard Schema (Zod 4, Valibot, ArkType) | The de facto typed env library. Standard Schema means teams keep the validator they already use, and Standard JSON Schema gives us a portable way to read it. | `@docuconf/t3` |
| Ruby | [anyway_config](https://github.com/palkan/anyway_config) (Evil Martians) | Typed config classes with `required` and `coerce_types`, and Rails integration. It is the closest Ruby has to the others. | `docuconf-anyway` |
| .NET | Microsoft.Extensions.Options with DataAnnotations and the `[OptionsValidator]` source generator | Built into the platform. Most attributes already exist (`[Required]`, `[Range]`, `[AllowedValues]`). | `Docuconf.Options` |
| Python | [pydantic-settings](https://github.com/pydantic/pydantic-settings) | The clear standard, and `model_json_schema()` already exposes everything. | `docuconf-pydantic` |
| Java | Spring Boot `@ConfigurationProperties` with Jakarta Validation | Spring's configuration processor already emits metadata at compile time. docuconf turns that metadata into a contract. | `dev.docuconf:docuconf-spring` |

Two related projects are worth knowing for positioning:

- **[Varlock / @env-spec](https://varlock.dev/env-spec/overview/)** puts a schema in `.env.schema` comments, aimed at local development and secrets hygiene.
- **Spring's configuration metadata** is the closest existing idea to a contract, but it only covers Spring.

docuconf's difference is the platform side: a contract tied to the image digest and checked by Crossplane before deploy. An `@env-spec` importer is a cheap way to win Varlock users later.

## 3. What each SDK looks like

The examples below declare the same service as `spec/cue/examples/billing_contract.cue`. Each is the host library's normal code, plus small docuconf additions.

**Go, on caarlos0/env.**

```go
type Config struct {
    // Primary Postgres connection string.
    DatabaseURL string `env:"DATABASE_URL,required" secret:"true" schemes:"postgres,postgresql"`

    // HTTP listen port.
    Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

    // Upstream request timeout.
    Timeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s" max:"5m"`
}

cfg, err := docuconf.Parse[Config]() // env.ParseAs, then docuconf's constraints; all violations in one error
```

- Teams already on caarlos0/env can keep calling `env.ParseAs` and add `docuconf.Validate(cfg)` afterwards.
- `docuconf export -pkg ./internal/config -type Config` compiles and runs a tiny program inside the app's module (via `go run`) that reflects over the struct and reads its doc comments from source. It needs the Go toolchain and the module's dependencies, but no environment values. The config package's `init` functions, and those of everything it imports, run during export, so keep the config package free of side-effecting imports. Descriptions come from each field's doc comment, the idiomatic place in Go, with a `desc` tag as a fallback.

**TypeScript, on T3 Env.**

```ts
import { createEnv } from "@docuconf/t3"; // re-exports T3's createEnv and records the schema
import { secret, duration } from "@docuconf/t3";
import { z } from "zod";

export const env = createEnv({
  server: {
    DATABASE_URL: secret(z.url({ protocol: /^postgres(ql)?$/ }).describe("Primary Postgres connection string")),
    PORT: z.coerce.number().int().min(1).max(65535).default(8080).describe("HTTP listen port"),
    LOG_LEVEL: z.enum(["debug", "info", "warn", "error"]).default("info").describe("Minimum log level emitted"),
    REQUEST_TIMEOUT: duration().default("30s").describe("Upstream request timeout"),
  },
  runtimeEnv: process.env,
});
```

- `npx docuconf export src/env.ts` loads the module in export mode, which skips validation, and converts each schema through Standard JSON Schema.
- The `client` section is build-time and is not exported (SPEC §11.1).
- **Lint:** `z.coerce.boolean()` turns the string `"false"` into `true`. The exporter rejects it and points to `z.stringbool()`.

**Ruby, on anyway_config.**

```ruby
class BillingConfig < Anyway::Config
  include Docuconf::Anyway
  config_name :billing # anyway reads BILLING_* variables

  attr_config :database_url, port: 8080, log_level: "info", request_timeout: "PT30S"
  required :database_url
  coerce_types port: :integer, request_timeout: :duration # docuconf supplies the :duration caster

  describe database_url: "Primary Postgres connection string",
           port: "HTTP listen port",
           log_level: "Minimum log level emitted",
           request_timeout: "Upstream request timeout"
  secret :database_url, schemes: %w[postgres postgresql]
  constrain port: {min: 1, max: 65535}, log_level: {values: %w[debug info warn error]}
end
```

- `bin/rails docuconf:export` writes the contract. Durations use `ActiveSupport::Duration.parse`, so their encoding is `iso8601`.
- anyway_config also reads YAML and Rails credentials. docuconf's `exclude` macro marks attributes that only come from credentials, and those are left out of the contract because the platform does not inject them.
- **Rails gotcha:** `assets:precompile` boots the app without production env vars. Validation is on for `server` and `console`, off for asset tasks, with an explicit override.

**.NET, on Options.**

```csharp
[ConfigContract("billing-api", Section = "Billing")]
public sealed class BillingOptions
{
    [Required, Secret, UrlSchemes("postgres", "postgresql"), Description("Primary Postgres connection string")]
    public string DatabaseUrl { get; set; } = "";

    [Range(1, 65535), Description("HTTP listen port")]
    public int Port { get; set; } = 8080;

    [Description("Upstream request timeout")]
    public TimeSpan RequestTimeout { get; set; } = TimeSpan.FromSeconds(30);
}

[OptionsValidator]
public partial class ValidateBillingOptions : IValidateOptions<BillingOptions> { }

builder.Services.AddOptions<BillingOptions>().BindConfiguration("Billing").ValidateOnStart();
builder.Services.AddSingleton<IValidateOptions<BillingOptions>, ValidateBillingOptions>();
```

- The `Docuconf.Options` source generator emits `contract.cue` during `dotnet build`. It reads the same DataAnnotations the `[OptionsValidator]` generator uses. It needs no reflection and is AOT-safe.
- Variable names follow .NET's configuration path rules (`BILLING__DATABASEURL`). Lists use the `indexed` encoding and durations use `timespan`, because that is what the binder parses.
- A `[Required]` string's `= ""` initializer is not a default, so the generator does not export it as one.

*appsettings.json.* Most .NET teams keep much of their configuration in appsettings files, so the SDK treats them as part of the contract (SPEC §4.4). The default host's precedence decides the design: `appsettings.json` < `appsettings.{Environment}.json` < user secrets (Development only) < environment variables < command line.

- The generator reads the appsettings files in the publish output. These are the files that ship in the image, so they are what the app will really run with.
- **`appsettings.json`** values become `default`s. A `[Required]` property set there is exported as optional with that default, because the platform no longer has to supply it.
- **`appsettings.{Environment}.json`** values become `profiles.defaults.{Environment}`. The selector is `ASPNETCORE_ENVIRONMENT` for web projects, where it overrides `DOTNET_ENVIRONMENT`, and `DOTNET_ENVIRONMENT` for worker services. The default profile is `Production`, which is .NET's own default.
- **Platform values always win,** because environment variables layer on top of the files. So a team can keep sensible values in `appsettings.Production.json` and let the platform override one of them per cluster without touching the image.
- **Build errors:**
  - a `[Secret]` property with a value in any appsettings file, since that would ship the secret in the image;
  - a file value that breaks the property's own attributes, such as a `Port` of 70000 against `[Range(1, 65535)]`.

  The schema enforces both too (`spec/cue/testdata/invalid/profile_*`).
- **Not in the contract:**
  - sections no `[ConfigContract]` class binds (`Logging`, `Kestrel`, `AllowedHosts`, `Serilog`);
  - values from Key Vault or other external providers (`[External]`);
  - shapes env vars cannot carry in v1alpha1 (arrays of objects, dictionaries). The generator warns about these, because the platform cannot set them.
- **Framework settings platforms commonly override** ship as opt-in fragments: `Logging__LogLevel__Default` as an enum, `ASPNETCORE_HTTP_PORTS`, `ASPNETCORE_URLS`.
- **Mounting `appsettings.Production.json` from a ConfigMap** is discouraged. It replaces the baked-in file and silently drops its values. Env vars layer instead. A file render target for teams that insist is an open question (SPEC §13.4).

`spec/cue/examples/inventory_contract.cue` is a worked example: Production is satisfied by its appsettings file, and Staging overrides its file's value from the platform.

**Python, on pydantic-settings.**

```python
class Settings(BaseSettings):
    database_url: SecretStr = Field(description="Primary Postgres connection string",
                                    json_schema_extra={"x-docuconf": {"type": "url", "schemes": ["postgres", "postgresql"]}})
    port: int = Field(8080, ge=1, le=65535, description="HTTP listen port")
    log_level: Literal["debug", "info", "warn", "error"] = Field("info", description="Minimum log level emitted")
    request_timeout: timedelta = Field(timedelta(seconds=30), description="Upstream request timeout")
```

- `docuconf export app.settings:Settings` converts `model_json_schema()`.
- `SecretStr` maps to `secret`.
- Lists use the `json` encoding, pydantic-settings' default. Fields annotated `NoDecode` with a splitting validator use `csv`.
- `timedelta` uses `iso8601`.

**Java, on Spring Boot (later).** An annotation processor runs next to `spring-boot-configuration-processor`. It reads `@ConfigurationProperties` classes, Javadoc descriptions and Jakarta constraints, and emits the contract with relaxed-binding env names (`BILLING_DATABASEURL`).

## 4. The GitHub organization

The project gets its own org. This keeps it neutral, which matters for outside contributors and for a later CNCF Sandbox application. It also lets each SDK have its own maintainers.

**Layout:** follow OpenFeature's proven model. It has the same shape as docuconf: one spec, a conformance suite, and an SDK per language maintained by people from that ecosystem.

| Repository | Contents |
|---|---|
| `docuconf/spec` | `SPEC.md`, the CUE module `docuconf.dev/contract`, the conformance suite. Tagged releases that SDKs pin. |
| `docuconf/docuconf` | The Go CLI (`export` for Go, `vet`, `render`, `diff`, `docs`, `push`/`pull`) and the shared Go library for CUE validation and error formatting. |
| `docuconf/function-docuconf` | The Crossplane composition function. Named to match Crossplane's `function-*` convention. |
| `docuconf/docuconf-go` | Go SDK on caarlos0/env. **This repository, transferred**, so its history and stars carry over. |
| `docuconf/docuconf-js` | `@docuconf/t3` and the TypeScript export CLI. |
| `docuconf/docuconf-ruby` | `docuconf-anyway` and its Railtie. |
| `docuconf/docuconf-dotnet` | `Docuconf.Options` and its source generator. |
| `docuconf/docuconf-python` | `docuconf-pydantic`. Later. |
| `docuconf/docuconf-java` | `docuconf-spring`. Later. |
| `docuconf/examples` | One sample app per language, plus the kind and Crossplane demo. |
| `docuconf/docuconf.dev` | The website. |
| `docuconf/.github` | Org profile README, and the default CONTRIBUTING, CODE_OF_CONDUCT, SECURITY and issue templates every repo inherits. |

This replaces the monorepo in the previous draft. Now that SDKs are thin layers on separate ecosystems, each needs its own tooling (gem, npm, NuGet), release cadence and maintainers. The spec repo holds what must stay in step. Each SDK repo pins a spec release and runs its conformance suite in CI. A nightly workflow in `docuconf/spec` runs every SDK against the spec's main branch and publishes a compatibility matrix to the website.

**Setup checklist:**

1. **Names.** Claim the org name, with `docuconf-dev` or `docuconfhq` as fallbacks. Check and reserve, in the same sitting:
   - the domain `docuconf.dev` (used in the CUE module path and Maven `groupId`)
   - the npm scope `@docuconf`
   - RubyGems `docuconf`
   - PyPI `docuconf`
   - a NuGet `Docuconf.*` ID prefix reservation
   - a Maven Central namespace for `dev.docuconf`, which needs the domain to verify
2. **Move this repository.** Transfer `bearbinary/docuconf-go` to the org; GitHub redirects old URLs and git remotes. The Go module path is already `github.com/docuconf/docuconf-go`. That breaks imports, which is acceptable before v1. Later, split `spec/` into `docuconf/spec` with `git filter-repo --subdirectory-filter spec`, which keeps its history.
3. **Org settings.**
   - Require 2FA.
   - Create a team per SDK and give it CODEOWNERS on that repo.
   - Use org-wide rulesets: protected `main`, required reviews and status checks.
   - Enable private vulnerability reporting.
4. **Contribution terms.** Use the DCO (sign-off) rather than a CLA. It has less friction and is what CNCF projects use.
5. **Licence.** MIT everywhere: a `LICENSE` file in every repository and the licence field in every package manifest.
6. **Releases.**
   - release-please in each repo.
   - Trusted publishing (OIDC from GitHub Actions) wherever the registry supports it, so no long-lived tokens are stored.
   - npm provenance.
   - Signed tags.
   - OpenSSF Scorecard on every repo.
7. **Governance.** Start with a small maintainer group: you plus the SDK leads. Write `GOVERNANCE.md` early. A neutral org with written governance is a prerequisite for CNCF Sandbox.

## 5. Phases

Sizes are relative: S is about a week of one engineer's time, M two to three weeks, L more than a month.

### Phase 0: Org and spec (S)

- Complete the org checklist (section 4).
- Create `docuconf/spec` from this branch's `spec/`.
- Settle the open questions in SPEC §13.
- Grow `spec/cue/testdata` into the conformance suite:
  - about 20 contract cases
  - about 40 load cases covering every type, encoding and error code
  - the export fixture with `golden.cue`
- Publish the CUE module.

**Done when:** the spec repo has a tagged `v1alpha1` release that SDKs can pin.

### Phase 1: Go SDK and CLI (M)

- Rebuild `docuconf-go` on caarlos0/env (section 3). Keep the old builder API for one release, marked deprecated.
- Build the CLI:
  - `vet` and `render`, with readable errors (SPEC §7)
  - `diff` (SPEC §9)
  - `docs`
  - `push` and `pull` of contracts as OCI artifacts by image digest
  - a conformance runner that other SDKs call. It renders each load case in the SDK's encodings and checks the SDK's results.

**Done when:** the Go SDK passes conformance, and a `docuconf vet` GitHub Action runs against a sample GitOps repo.

### Phase 2: Platform integration (M)

- Build `function-docuconf`. It:
  1. pulls the contract for the claim's image digest,
  2. unifies it with the environment's policy,
  3. sets a `ContractValid` condition with one line per bad variable,
  4. emits the rendered `env`.
- CI templates for `vet` and `diff`.
- A kind demo in `docuconf/examples`: one claim deploys; one with a bad `PORT` is rejected.

**Done when:** the demo runs from one `make` target in CI.

### Phase 3: TypeScript, Ruby and .NET SDKs (L, in parallel)

These can start as soon as Phase 0's conformance suite and Phase 1's runner exist. Ideally each has a lead from that ecosystem.

**Done when:** each SDK passes conformance, its example app deploys through the Phase 2 demo, and its README shows the host library's code first and docuconf's additions second.

### Phase 4: Website, Python, Java, v1beta1 (M, then ongoing)

- Launch the website (section 6).
- Build the Python SDK, then the Java SDK.
- Move the spec to `v1beta1` after at least two languages are in real use.

## 6. Website

**Stack:**

- Astro Starlight in `docuconf/docuconf.dev`.
- Synced code tabs, so one page shows Go, TypeScript, Ruby and .NET, and the reader's choice persists.
- Pagefind search, built in.
- Versioning through the community `starlight-versions` plugin.
- Hosted on Cloudflare Pages or GitHub Pages, with pull request previews.

| Section | Content |
|---|---|
| Home | The pitch, the lifecycle diagram, and "your existing env library + a few annotations = a deploy-time contract". |
| Concepts | Contracts, values and policy; the three validation points; wire encodings; why feature flags are separate. |
| Languages | One guide per SDK, each starting from the host library's own docs and adding docuconf. |
| Platform | Crossplane function setup, CI actions, policy recipes, OCI distribution. |
| Spec | `SPEC.md` per version, the CUE reference, and the live compatibility matrix from the nightly conformance run. |
| Comparisons | Honest pages: docuconf versus T3 Env, Varlock, Spring metadata. Mostly "use them together". |
| Playground (later) | Paste a contract and values; see validation and rendered env, using CUE compiled to WebAssembly. |
| Community | Governance, contributing, "add an SDK for your language", the DCO. |

## 7. Risks

| Risk | Mitigation |
|---|---|
| A host library changes behaviour or is abandoned. | The SDK is a thin layer, so swapping hosts is contained. Conformance catches regressions on the next upgrade. |
| A host library cannot express something in the spec. | The SDK adds it as metadata (Ruby `constrain`, .NET `[Secret]`). If no clean way exists, the spec is the thing to reconsider. |
| SDKs drift. | Pinned conformance in each repo, plus the nightly matrix across all of them. |
| CUE errors put off platform teams. | The CLI and the function translate errors (SPEC §7). |
| Contract and image version skew. | Distribution by image digest (SPEC §8). |
| A new required variable breaks deploys. | `docuconf diff` in app CI. The platform pull request lands values first. |
| Removing a variable breaks validation. | A deprecation workflow, or the non-strict mode in SPEC §13.2. |

## 8. Decisions needed from you

1. **Org name.** Then reserve the names in section 4 the same day.
2. **Phase 3 order.** If only one SDK can start at once: TypeScript (largest audience), Ruby or .NET.
3. **Ruby host.** anyway_config is recommended. The alternative is building on plain `ENV` plus dotenv, if your Rails apps do not use anyway_config and you would rather avoid the dependency.
4. **Distribution.** Whether your registries support OCI referrers (or the tag fallback oras uses), or whether to start with GitOps-committed contracts.
5. **SPEC §13 open questions,** especially strict unknown-variable handling and build-time variables.
