# docuconf Configuration Contract Specification

Status: **Draft, v1alpha1**
Reference schema: [`cue/contract/contract.cue`](cue/contract/contract.cue); docs model schema: [`cue/docs/docs.cue`](cue/docs/docs.cue) (run `cue/test.sh` to check both)

## 1. Purpose

An application's configuration inputs are an API between the application and the platform that runs it: environment variables, config files, TLS certificates, CA bundles, keystores and other files it reads. Today that API is usually undocumented, untyped, and checked only when the process crashes.

docuconf makes the API explicit:

1. The application declares every input it reads, in its own language, with a type, constraints and a description.
2. A docuconf SDK exports that declaration as a **contract**: a CUE document.
3. The platform (Kubernetes, composed by Crossplane and CUE) validates what it intends to supply against the contract **before** anything is deployed.
4. At boot, the SDK validates the real environment and files against the same declaration and exposes typed values to the application.

One contract format, one validation model, one SDK per language.

### 1.1 Goals

- A deploy with a missing, mistyped or out-of-range variable, or a missing or malformed file, fails at composition time, not at runtime.
- Every input is documented, because the contract requires a description.
- The contract is language-neutral. A Rails app and a .NET app produce documents with the same shape.
- Platform teams can add environment-specific policy (for example, "no debug logging in prod") on top of an app contract without editing it.
- Secret material never appears in a contract or in the values document.

### 1.2 Non-goals

- **Feature flags.** See [section 10](#10-feature-flags-are-not-environment-configuration).
- Secret storage or rotation. docuconf validates that a secret is *referenced*; External Secrets, Vault or the CSI driver supply it.
- Provisioning the things an app depends on (databases, queues, DNS, buckets). That is the job of Crossplane composite resources or a workload spec such as [Score](https://score.dev). docuconf types how their outputs reach the app: a connection string Secret, a CA bundle, a credentials file.
- Command-line arguments. Twelve-factor apps take configuration from the environment and files; arguments may be added later.
- Replacing a language's config ecosystem. Each SDK extends that language's leading environment library rather than competing with it (section 11.1).

## 2. Terms

| Term | Meaning |
|---|---|
| **Declaration** | The in-language definition of a service's inputs (Go struct tags, a Ruby DSL, a TypeScript object, .NET attributes). |
| **Input** | Anything the app reads from its environment: a variable (`vars`) or a file (`files`). |
| **Contract** | The CUE document exported from a declaration. Kind `ConfigContract`. |
| **Values** | The typed values a platform intends to inject for one deployment of one service. |
| **Sources** | Where the platform gets each file input: inline content, a ConfigMap, a Secret, a cert-manager Certificate, a CSI volume or an image. |
| **Policy** | Extra CUE constraints a platform unifies with values for a given environment. |
| **SDK** | A docuconf library for one language: declaration API, export, boot-time loader. |
| **Canonical encoding** | The single string form each type takes inside a Kubernetes `env` entry. |
| **Docs model** | The JSON document `docuconf docs` builds from a contract, and renders documentation from. Kind `ConfigDocs` (section 14). |

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

## 3. Lifecycle

```
 app repo (CI)                               platform (GitOps + Crossplane)
 ─────────────                               ──────────────────────────────
 declaration ──export──▶ contract.cue ──publish (OCI, by image digest)──┐
                                                                         ▼
                                   values + sources + policy ──▶ #Validate ──▶ #Render ──▶ env, volumes
                                                                                           │
 SDK loader at boot ◀──────────────────────── process environment ◀────────────────────────┘
```

Validation happens three times, each catching what the earlier step cannot:

| When | Who | Catches |
|---|---|---|
| CI of the platform repo | `docuconf vet` | Bad values before merge. |
| Composition | Crossplane function | Bad values that reached the cluster anyway, and contract/image version skew. |
| Boot | Language SDK | Secret contents, certificate expiry and key match, values set outside the platform, local development. |

## 4. The contract document

A contract is CUE that MUST unify with `#Contract`. SDKs MUST emit it as plain data: no CUE expressions, references, comprehensions or imports beyond the meta-schema. Because the contract is data, every SDK can emit it without a CUE library, it converts losslessly to JSON (`cue export`), and the compatibility checker (section 9) can diff it field by field.

```cue
// Code generated by docuconf. DO NOT EDIT.
package billing

import "docuconf.dev/contract"

contract.#Contract & {
	apiVersion: "docuconf.dev/v1alpha1"
	kind:       "ConfigContract"
	metadata: {
		name:       "billing-api"
		appVersion: "1.4.0"
		generator: {language: "ruby", sdk: "docuconf-rails", version: "0.1.0"}
	}
	vars: {
		DATABASE_URL: {
			type:        "url"
			description: "Primary Postgres connection string"
			required:    true
			secret:      true
			schemes: ["postgres", "postgresql"]
		}
		PORT: {
			type:        "int"
			description: "HTTP listen port"
			default:     8080
			min:         1
			max:         65535
		}
	}
}
```

A full example is in [`cue/examples/billing_contract.cue`](cue/examples/billing_contract.cue).

### 4.1 Metadata

| Field | Required | Rule |
|---|---|---|
| `metadata.name` | yes | DNS label (`[a-z0-9-]`, at most 63 characters). Identifies the service. |
| `metadata.appVersion` | no | The version or git SHA the contract was exported from. |
| `metadata.generator.language` | yes | One of `go`, `typescript`, `ruby`, `dotnet`, `python`, `java`, `kotlin`, `rust`, `swift`, `elixir`, `gleam`, `cpp`, `php`, `cobol`. |
| `metadata.generator.sdk`, `.version` | yes | The SDK package name and version, for debugging and compatibility. |

### 4.2 Variables

`vars` is a map keyed by environment variable name. Names MUST match `^[A-Z][A-Z0-9_]*$`. Language SDKs map idiomatic field names (`databaseUrl`, `DatabaseUrl`, `database_url`) to this form, and the contract always holds the final name.

Fields common to every type:

| Field | Default | Rule |
|---|---|---|
| `type` | — | One of the types in 4.3. |
| `description` | — | Required. Plain text, at least 5 characters: what the input is, in one sentence or phrase. Rendered into generated docs (section 14). |
| `details` | — | Optional. CommonMark: why the input exists and when to change it. Not blank, and at most 4000 characters (Unicode code points). Used only in generated docs, never at runtime. SDKs fill it from the language's natural doc location (section 11.2). |
| `required` | `false` | A required variable MUST NOT have a `default`. The platform must supply it. |
| `secret` | `false` | The value must come from a secret reference (section 6). A secret MUST NOT have a `default` or `examples`. |
| `group` | — | Free-form grouping for docs (`database`, `http`). |
| `examples` | — | Example values, as strings, for docs. |
| `deprecated` | — | `{message, replacedBy?}`. SDKs warn at boot when a deprecated variable is set. |
| `configKey` | — | The app's own configuration key (SDKs MAY emit it even when it matches the env name): `Orders:CheckoutTimeout` in .NET, `orders.checkout-timeout` in Spring. Used in docs, so readers can find the setting in the app's config files. |

An optional variable with no default is legal. The SDK exposes it as absent (`nil`, `undefined`, `null`, `Option`).

### 4.3 Types

| `type` | Constraint fields | Platform value (CUE) | Wire string (section 5) |
|---|---|---|---|
| `string` | `minLength`, `maxLength`, `pattern` (RE2) | `string` | as is |
| `int` | `min`, `max` | `int` | base-10, no leading `+` or zeros: `8080` |
| `float` | `min`, `max` | `number` | shortest round-trip decimal: `0.5` |
| `bool` | — | `bool` | `true` / `false` |
| `duration` | `min`, `max` (durations), `encoding` | Go-syntax duration, e.g. `1m30s` | depends on `encoding` |
| `url` | `schemes` | string with a `scheme://` | as is |
| `enum` | `values` (non-empty) | one of `values` | as is |
| `list` | `items` (`string`\|`int`), `encoding`, `separator` (csv only, default `,`), `minItems`, `maxItems`, `itemMin` and `itemMax` (`int` items only) | list | depends on `encoding` |
| `json` | `schema` (JSON Schema) | any JSON value | compact JSON |

Rules:

- `pattern` is RE2, the only regex dialect every SDK can match exactly (Go native, `re2` bindings or a compatible subset elsewhere). SDKs MUST reject patterns that use features outside RE2, such as lookaround or backreferences, at declaration time.
- RE2's `\d`, `\w`, `\s` and `\b` are ASCII-only. Where the host engine treats them as Unicode (Python, .NET, Rust's `regex` by default), SDKs SHOULD warn and suggest explicit classes such as `[0-9]`, since the platform and the app would otherwise disagree on non-ASCII input.
- `pattern` matches **anywhere** in the value, as CUE's `=~` and JSON Schema's `pattern` do; anchor it with `^` and `$` to match the whole value. Some host libraries match the whole value instead (.NET `[RegularExpression]`, Java `@Pattern`); their SDKs MUST anchor such patterns on export, as `^(?:p)$`, so the platform and the app accept exactly the same values.
- `default` MUST satisfy the variable's own constraints. SDKs MUST check this at declaration time.
- A `json` variable carries a structured value, such as a rate-limit object. Its `schema` is a JSON Schema the SDK generates from the app's own type, so the platform checks the value against the same type the app deserializes into (section 4.6).
- The type set is closed in v1alpha1. A new type needs a spec change, because every SDK must parse it identically.

### 4.4 Config files and profiles

Many apps do not get their configuration only from the environment. .NET layers `appsettings.json`, then `appsettings.{Environment}.json`, then environment variables. Spring layers `application.yml` and `application-{profile}.yml`. anyway_config reads per-environment YAML in Rails. Files baked into the image are part of what the app will actually run with, so the contract describes them:

- A value in an **always-loaded base file** (`appsettings.json`, `application.yml`) is an ordinary `default`. A `[Required]` property with a value in the base file is therefore exported as optional with that default.
- A value in a **profile file** goes in `profiles.defaults`, keyed by profile name. It applies only when that profile is selected.
- `profiles.selector` names the environment variable that picks the profile (`ASPNETCORE_ENVIRONMENT`, `DOTNET_ENVIRONMENT`, `SPRING_PROFILES_ACTIVE`). It MUST be a declared variable; when the app does not declare it itself, the SDK adds it as an optional `string` variable whose default is `profiles.default`, as the .NET and Ruby SDKs do for `ASPNETCORE_ENVIRONMENT` and `RAILS_ENV`. `profiles.default` is the profile in effect when the selector is unset (`Production` in .NET, `development` in Rails).

```cue
profiles: {
	selector: "DOTNET_ENVIRONMENT"
	default:  "Production"
	defaults: {
		Production: INVENTORY__WAREHOUSEAPI: "https://warehouse.internal"
		Staging: INVENTORY__WAREHOUSEAPI:    "https://warehouse.staging.internal"
	}
}
```

Profile names in `profiles.defaults` are case-sensitive. Where the host treats them case-insensitively (figment), the SDK exports the names as the app's config files spell them.

Rules, enforced by the meta-schema:

- Every profile default MUST name a declared variable and satisfy its constraints. A bad value in `appsettings.Production.json` fails the same way as a bad value from the platform.
- A secret MUST NOT have a value in any config file. That would ship the secret inside the image.
- A required variable is satisfied if the platform sets it **or** the selected profile does. Selecting a profile with no file, or one whose file lacks the value, makes the platform responsible for it.
- The platform's environment variables override file values. This is the default precedence in .NET and Spring, and SDKs for hosts with a different order MUST document it.

Configuration from sources the platform does not control, such as Azure Key Vault, AWS Secrets Manager or Rails credentials, is outside the contract. SDKs MUST leave those keys out, or provide a way to exclude them. Exclusion is usually opt-in, since an SDK cannot always tell where a value comes from (Rails credentials are encrypted, for example). Settings that cannot be expressed in v1alpha1 types (arrays of objects, dictionaries) stay file-only, and the SDK warns that the platform cannot set them.

### 4.5 Value sources

A non-secret variable's value is usually a literal. Some values only exist in the cluster, so the platform may supply them by reference instead:

| Source | Example | Allowed for | Checked |
|---|---|---|---|
| literal | `PORT: 9090` | every non-secret type | before deploy |
| `configMapKeyRef` | `{configMapKeyRef: {name: "limits", key: "rate"}}` | every non-secret type except `list` | at boot |
| `fieldRef` (Downward API) | `{fieldRef: fieldPath: "metadata.namespace"}` | `string` | always valid |
| `resourceFieldRef` | `{resourceFieldRef: resource: "limits.memory"}` | `int` | always valid |
| `secretKeyRef` | `{secretKeyRef: {name: "db", key: "url"}}` | secret variables (one of the two secret sources) | at boot |
| `injected` with `ref` | `{injected: {provider: "bank-vaults", ref: "vault:secret/data/db#url"}}` | every type, including secrets; not an `indexed` list | the reference's shape before deploy; the value at boot |
| `injected` without `ref` | `{injected: {provider: "otel-operator"}}` | every type, including secrets | at boot |

The Downward API always yields a string, and resource fields an integer, so the meta-schema rejects a `fieldRef` for an `int` variable. Typical uses are a pod's namespace for metrics labels, and `GOMEMLIMIT` or `DOTNET_GCHeapHardLimit` from the container's memory limit.

#### 4.5.1 Injected values

Many platforms do not put every value in the pod spec. A mutating webhook, a wrapper process or an operator supplies it when the container starts:

- **Bank-Vaults** (`vault-env`) reads env values such as `vault:secret/data/db#url`, resolves them, and execs the app with the real values.
- **Wrappers** such as `op run`, `doppler run` or `infisical run` resolve their own reference formats the same way.
- **Operators and webhooks**, such as the OpenTelemetry operator, add variables (`OTEL_EXPORTER_OTLP_ENDPOINT`) to the pod themselves.

The contract does not change: it describes what the app accepts, not who supplies it. The platform's values document says who supplies it, with `injected`:

- `provider` names the injector, as a lowercase label (`bank-vaults`, `otel-operator`). It is for people and tooling; docuconf does not interpret it.
- `ref`, when present, is the reference the injector resolves. `#Render` writes it verbatim as the env value (with `$` escaped, section 5). When absent, the injector sets the variable itself and `#Render` emits nothing for it.
- An injected value is checked only at boot, by the SDK, after injection. Before deploy, `#Validate` checks only that the reference is well-formed and that an `indexed` list (one variable per item) is not given a single reference.
- A secret variable may be injected; that is often the point. Like a `secretKeyRef`, the reference is not secret material.

Making sure the injector actually runs (for Bank-Vaults, the pod annotations or namespace label that enable its webhook) is the platform's job and outside the contract. When it does not run, the app receives the raw reference, and the SDK's boot check is what catches it: a URL or pattern fails its constraints, and section 11.2 asks SDKs to recognise an unresolved reference outright.

### 4.6 File inputs

Many inputs are files, not variables: a structured config file, a TLS key pair, a CA bundle, a keystore, a licence key, a data file. `files` is a map keyed by input name (a DNS label, since it names a volume):

```cue
files: {
	routes: {
		type:        "config"
		format:      "yaml"
		description: "Routing table: path prefixes and their upstreams"
		required:    true
		path:        "/etc/gateway/routes/routes.yaml"
		pathEnv:     "ROUTES_FILE"
		reload:      "watch"
		schema: {...} // JSON Schema generated from the app's Routes type
	}
	"serving-tls": {
		type:        "tls"
		description: "Certificate the gateway serves HTTPS with"
		path:        "/etc/gateway/tls"
		dnsNames: ["gateway.internal", "api.example.com"]
		keyAlgorithms: ["ECDSA", "RSA"]
		minRemaining: "720h"
	}
}
```

Fields common to every file input:

| Field | Default | Rule |
|---|---|---|
| `type` | — | One of the file types below. |
| `description`, `details`, `required`, `group`, `deprecated` | | As for variables. |
| `secret` | `false` | Content must come from a secret store. Forced to `true` for `tls` and `keystore`. |
| `path` | — | Where the app reads the input: a directory for `tls`, a file otherwise. Absolute and normalised. |
| `pathEnv` | — | An environment variable the platform sets to `path`, for apps that read the location from the environment (`SSL_CERT_FILE`, `ROUTES_FILE`). It MUST NOT also be declared in `vars`. |
| `reload` | `restart` | `restart`: the app reads the file once, so a changed source needs a rollout. `watch`: the app reloads the file itself. |
| `maxSize` | — | Upper bound in bytes. |

| `type` | Content | Constraint fields |
|---|---|---|
| `config` | A structured file in `format` `json`, `yaml` or `toml`. | `schema`: a JSON Schema the SDK generates from the type the app binds the file to. |
| `tls` | A key pair in the `kubernetes.io/tls` layout: `tls.crt`, `tls.key`, and `ca.crt` when `requireCA` is set. | `dnsNames`, `keyAlgorithms` (`RSA`, `ECDSA`, `Ed25519`), `minRemaining`, `requireCA`. |
| `caBundle` | One or more PEM CA certificates. | `minCertificates` (default 1). |
| `keystore` | A PKCS#12 or JKS keystore. | `format`; `passwordVar`, which MUST name a declared secret variable. |
| `text` | A text file, such as a licence key. | `pattern` (RE2), `minLength`, `maxLength`. |
| `binary` | Opaque bytes, such as a GeoIP database. | `maxSize` only. |

**Schemas come from code.** A config file or `json` variable is only type-safe if the platform checks it against the same type the app deserializes into. SDKs therefore generate `schema` from that type, using each ecosystem's own JSON Schema support: `JsonSchemaExporter` in .NET, `model_json_schema()` in pydantic, Standard JSON Schema in TypeScript, reflection-based generators in Go and Java. The platform tooling compiles the JSON Schema to CUE (`cuelang.org/go/encoding/jsonschema`) and passes it to `#Validate` as `#schemas`.

**Mount rules**, enforced by the meta-schema:

- A file is mounted at its parent directory, and a TLS key pair at its own directory. Mounting hides whatever the image had there, so no two inputs may share a mount directory, and none may be mounted at a reserved directory such as `/`, `/etc`, `/etc/ssl/certs`, `/usr`, `/var` or `/app`. A CA bundle at `/etc/ssl/certs/private.pem` would otherwise hide the system trust store.
- Files are projected with `items`, never `subPath`. A `subPath` mount does not receive updates, which would silently break certificate rotation.
- Secret files are mounted read-only with mode `0400`; other files `0444`.

#### 4.6.1 File sources

The platform chooses where each file comes from:

| Source | For | Checked before deploy |
|---|---|---|
| `inline` | non-secret files | Everything: format, `schema`, `pattern`, size, certificate count. Rendered as an immutable ConfigMap named with a hash of its content, so any change causes a rollout. |
| `configMap` | non-secret files | That a key is given for single files. trust-manager writes CA bundles to ConfigMaps. |
| `secret` | any | That a key is given for single files, and none for a TLS directory. With resolved metadata: the Secret's type is `kubernetes.io/tls` and it has the required keys. |
| `certificate` (cert-manager) | `tls` | From the Certificate's spec, which is not secret: it covers every name in `dnsNames` (wildcards count for one label), uses an allowed key algorithm, and its `renewBefore` is at least `minRemaining`, since cert-manager renews when that much validity is left. |
| `csi` (Secrets Store CSI driver) | any | Nothing about the content; it is checked at boot. |
| `image` (image volume) | non-secret files | Nothing about the content. For data too large for a ConfigMap's 1 MiB limit. Needs a cluster with image volumes enabled. |
| `injected` | any | Nothing about the content. An injector, such as the Vault Agent injector rendering a template to `/vault/secrets`, writes the file at `path` when the pod starts. `#Render` emits no volume or mount for it; the platform must make the injector write to the declared `path`. |

Inline content is a string, written to the file as given. For a `json` or `yaml` config file it may instead be structured data, which is checked against the file's `schema` and serialized in the file's `format`. The exact bytes of serialized content, and so the ConfigMap's hash, belong to the renderer: the CUE renderer keeps the order fields are written in, while Helm sorts object keys and indents lists differently. Two renderers MUST produce content that parses to the same data, and each MUST name the ConfigMap from a hash of the bytes it wrote; they need not agree on the bytes. A platform that needs byte-identical output across tools gives the content as a string.

Fields described as **resolved** (a Secret's `type` and `keys`, a Certificate's spec) are filled in by the platform tooling from the cluster. They are metadata, never secret contents. When they are absent, those checks move to boot.

#### 4.6.2 Rotation

A source that changes after deploy (a renewed certificate, an updated ConfigMap) only reaches an app that rereads it. `reload` makes this part of the contract:

- `watch`: the app reloads the file. The platform does nothing more.
- `restart`: `#Render` lists the source under `restartTriggers`, and the platform MUST roll the pods when it changes (for example with a reloader controller, or by hashing the source into a pod annotation).

Inline content is content-hashed, so it always rolls the pods when it changes.

### 4.7 Config-file overlays

Hosts that layer configuration files under environment variables (.NET, Spring Boot, Rails, figment, Hoplite) commonly take one more file from the platform, mounted between the files baked into the image and the environment:

```csharp
builder.Configuration.AddJsonFile("appsettings.json", optional: false);
builder.Configuration.AddJsonFile("/app/config/appsettings.Production.json", optional: true, reloadOnChange: true);
builder.Configuration.AddEnvironmentVariables();
```

The contract declares such a file in `overlays`, keyed by name (a DNS label):

```cue
overlays: platform: {
	format:       "json"                                    // json | yaml | toml
	path:         "/app/config/appsettings.Production.json"
	keySeparator: ":"                                       // ":" in .NET, "." in Spring
	reload:       "watch"                                   // or "restart" (default)
}
```

The platform supplies an overlay's values in the `overlays` input of `#Validate` and `#Render`, keyed by overlay name and then variable name, as typed values like any other:

```cue
overlays: platform: {
	CATALOG__PAGESIZE: 50
	CATALOG__CACHETTL: "90s"
	CATALOG__FEATUREDCATEGORIES: ["books", "games"]
}
```

Rules, enforced by the meta-schema:

- **Precedence** is fixed on every host: base file, then profile file (section 4.4), then the overlay, then environment variables. SDKs MUST load overlays in this position, and MUST reject an overlay at declaration time where the host cannot layer files.
- **Overlays add, never replace.** The overlay's directory is mounted, hiding what the image had there, so `path` MUST NOT be in a directory holding files the app ships with; the SDK checks this at export. Mounting a file over the baked-in `appsettings.Production.json` would silently discard its values, which is why overlays have their own path.
- An overlay value MUST be a literal for a declared, non-secret variable with a `configKey`. It is checked exactly like an env value. Overlays are ConfigMaps, so secrets come from the environment (a `secretKeyRef` or `injected`).
- A variable comes from one place: the environment or one overlay. Supplying both is an error, since the environment would silently win.
- A required variable is satisfied by an overlay as by an env value or a profile.
- The overlay's mount directory follows the file-input mount rules (section 4.6): unique and not reserved.
- The profile selector (section 4.4) cannot be supplied through an overlay: it chooses which files load, and an overlay is one of them.
- A variable an overlay may carry needs a `configKey`, and with overlays declared the `configKey` is no longer only for docs: it MUST be the host's real key path, written with `keySeparator`, because `#Render` places the value there. SDKs fill it in from the declaration and reject a conflicting one. A variable the host cannot read back from a nested file (for example a `json` value on a host whose file reader flattens objects) is exported without a `configKey`, which keeps it in the environment.

What the SDK does at boot:

- It loads the overlay as an optional file: a missing one is not an error; one that does not parse is `file_malformed`, reported with the other violations.
- It validates the values it binds from the overlay exactly as it validates env values, and a `json` value in its bound form: hosts that merge layers key by key may combine an overlay object with baked-in keys, and the app checks the result.
- It MUST NOT take a secret's value from an overlay, and SHOULD fail with `invalid_type` if one is there, without printing it.
- When a variable is set both in the environment and in an overlay, the environment wins, as the precedence says. The platform rejects this before deploy; an SDK that sees it at boot SHOULD log a warning naming the variable.
- It refuses an overlay whose directory holds files the app ships with. The image's real layout is often only known at runtime, so this check MAY run at boot rather than at export (for example: the overlay directory is the executable's directory, the working directory, or the directory of a baked-in config file).
- If it cannot reload an overlay, it rejects `reload: watch` at declaration time (section 11.2, item 8).

`#Render` writes the file:

- Each value goes at its variable's `configKey`, split on `keySeparator` (`Catalog:Search:Url` becomes `{"Catalog": {"Search": {"Url": ...}}}`), at most 8 levels deep.
- Values are written in **native types** (numbers, booleans, lists), since the host binds the file itself. Durations are written in the variable's `encoding`: a number for `seconds` (`90`, `1.5`), a string otherwise (`"00:01:30"` for .NET's `timespan`).
- Keys are sorted, so the file and its hash do not depend on the order the platform wrote its values in.
- `reload: watch` renders a mutable ConfigMap with a stable name: the kubelet updates the mounted file in place and the host reloads it (`reloadOnChange` in .NET, read through `IOptionsMonitor<T>`). `reload: restart` renders an immutable, content-hashed ConfigMap, so any change rolls the pods.

## 5. Wire encoding and parsing

Kubernetes env values are strings. The platform always holds **typed** values in CUE, written one way: lists as CUE lists, durations in Go syntax. `#Render` converts each value into the string form the application's library parses.

For most types, every mainstream library already agrees, so there is one form:

| Type | Wire form |
|---|---|
| `string`, `url`, `enum` | as is |
| `int` | base-10, no leading `+` or zeros: `8080` |
| `float` | shortest round-trip decimal: `0.5` |
| `bool` | `true` / `false` |

Lists and durations are different: the leading libraries disagree, and making an SDK fight its host library defeats the point of building on it. So the contract records the **encoding** the app actually parses, and the platform renders to it:

| `list` encoding | Wire form | Native to |
|---|---|---|
| `csv` (default) | `a,b`, joined by `separator` | caarlos0/env, Spring Boot, anyway_config |
| `json` | `["a","b"]` | pydantic-settings |
| `indexed` | separate variables `NAME__0=a`, `NAME__1=b` | Microsoft.Extensions.Configuration |

| `duration` encoding | Wire form for 90s | Native to |
|---|---|---|
| `go` (default) | `1m30s` | Go `time.ParseDuration` |
| `iso8601` | `PT90S` | pydantic `timedelta`, `ActiveSupport::Duration.parse`, `java.time.Duration` (Spring Boot, Hoplite). Spring's own short form takes one unit only (`90s`, not `1m30s`). |
| `seconds` | `90` | anything that takes a number |
| `timespan` | `00:01:30` (`d.hh:mm:ss.fff` when needed) | .NET `TimeSpan.Parse` |

An `indexed` list is present when any `NAME__<n>` is set, where `<n>` is a decimal index with no leading zero; other suffixes (`NAME__HOST`) are not items. Its items MUST be numbered from `0` with no gap: `NAME__0`, `NAME__2` without `NAME__1` is `invalid_type`, because a host that stops at the gap and one that skips it would read different lists.

Encodings other than `go` carry at most millisecond precision, and `#Validate` rejects finer values. Platform authors never see encodings: they write `"90s"` and `["a", "b"]` for every app.

Renderers MUST double every `$` in a literal value (`$` becomes `$$`). Kubernetes expands `$(NAME)` references inside env values and reduces `$$` to `$`, so this is the only way a literal containing `$` arrives unchanged.

SDK parsing rules:

- `bool` MUST accept `true` and `false`, case-insensitive. Host libraries that also accept `1`, `0`, `yes` and so on may keep doing so, since the platform only ever emits `true` / `false`.
- `int` MUST reject non-integers (`invalid_type`) and values outside the 64-bit signed range (`out_of_range`). When the app's field is narrower (a 32-bit `Int`, an `int8`, an unsigned type, a JavaScript `number` beyond 2^53), the SDK MUST export `min`/`max` within that range, so the platform never accepts a value the app cannot hold. The same applies to the items of an `int` list, through `itemMin`/`itemMax`; an item outside them is `out_of_range`.
- Values are never trimmed. A trailing newline is part of the value. Host libraries that trim whitespace around `csv` separators may keep doing so: the renderer never emits it.
- `float` MUST NOT be `NaN` or infinite, and SDKs MUST parse floats independently of the process locale.
- An **empty string** is a present value for `string` (and fails `minLength` if set). For every other type, empty means *unset*, so a defaulted variable takes its default and a required one fails. Where a host library treats empty differently, the SDK adds a pre-check rather than changing the spec.

## 6. Secrets

A `secret: true` variable MUST be supplied as a reference, never a literal:

```cue
DATABASE_URL: secretKeyRef: {name: "billing-db", key: "url"}
```

`#Render` emits it as a `valueFrom.secretKeyRef` entry. CUE cannot see the secret's contents, so its type and constraints (scheme, pattern, length) are enforced by the SDK at boot. SDKs MUST NOT include a secret's value in error messages, logs or docs.

A secret variable MAY instead be `injected` (section 4.5.1), with or without a reference.

A secret file input (`secret: true`, and always `tls` and `keystore`) MUST come from a `secret`, `certificate`, `csi` or `injected` source, never `inline` or a ConfigMap. Neither a contract nor a values document ever contains private keys or passwords.

## 7. Platform validation

The meta-schema provides two definitions, both exercised by `cue/test.sh`:

**`#Validate`**: given `contract`, `values`, `files` (sources) and the compiled `#schemas`,

- every `required` variable must be set by the platform or by the selected profile (section 4.4), and any that are not are listed in `missingRequired`,
- every value must satisfy its variable's type and constraints (`checks.<NAME>`), and every file source must pass the checks in section 4.6.1 (`fileChecks.<name>`),
- every required file input must have a source,
- any value or file source not declared in the contract is rejected. A typo like `DATABSE_URL` is the most common environment bug, so this check is on by default.

**`#Render`**: produces the container's `env` entries (in each variable's wire encoding, plus every `pathEnv`), the `volumes` and `volumeMounts` for file inputs and overlays, the ConfigMaps for inline content and overlays (section 4.7), and the `restartTriggers` (section 4.6.2).

**Policy** is plain CUE unified with the values:

```cue
prodPolicy: {
	LOG_LEVEL?:       "info" | "warn" | "error"
	STRIPE_API_BASE?: "https://api.stripe.com"
}

goodProd: contract.#Validate & {contract: billing, values: goodValues & prodPolicy}
```

The contract states what the app can accept. The policy states what an environment allows. A deploy must satisfy both, and neither side has to edit the other's file.

Error output MUST NOT include the value of a `secret` variable, including a literal wrongly supplied where a secret reference was required. CUE's own messages print values, so the translator redacts them.

Raw CUE errors for a failed disjunction are noisy, for example "8 errors in empty disjunction". The `docuconf` CLI and the Crossplane function MUST turn them into one line per variable using the `type` field, such as `PORT: 70000 is above max 65535`.

## 8. Distribution

A contract describes a specific build of an application. It MUST travel with the image it was exported from, so the platform can never validate image B against contract A.

- **Preferred:** push the contract as an OCI artifact that references the image's digest, using the OCI 1.1 referrers API. Artifact type: `application/vnd.docuconf.contract.v1alpha1+cue`. It can be signed with cosign like the image.
- **Fallback:** an image label `dev.docuconf.contract` holding the contract, base64-encoded, for registries without referrers support. This only works for small contracts, because of label size limits.
- **Local and GitOps:** commit `contract.cue` beside the claim. This is fine for getting started but invites version skew.

## 9. Compatibility

`docuconf diff old.cue new.cue` classifies every change. CI SHOULD block merges with an unacknowledged breaking change.

| Change | Class | Why |
|---|---|---|
| Add an optional variable | compatible | |
| Add a required variable | **breaking** | Existing values no longer validate. |
| Remove a variable | **breaking for the platform** | Values that still set it fail the unknown-variable check. Deprecate it first. |
| Optional → required | **breaking** | |
| Required → optional | compatible | |
| Change `type` or `secret` | **breaking** | The value's shape changes. |
| Tighten a constraint (narrower range, fewer enum values, new pattern) | **breaking** | |
| Loosen a constraint | compatible | |
| Change `description`, `details`, `group`, `examples` or `default` | compatible | `default` changes alter behaviour, so diff reports them as notable. `description` and `details` are docs only. |
| Add a required file input, or make one required | **breaking** | Existing sources no longer validate. |
| Change a file input's `path`, `type` or `format` | **breaking** for the app image only | The platform re-renders the mount; nothing in the values changes. Reported as notable. |
| Tighten a `schema` (new required property, narrower type) | **breaking** | Existing config files may no longer validate. Compared structurally after compiling both schemas. |
| Add `dnsNames`, raise `minRemaining`, narrow `keyAlgorithms` | **breaking** | The existing certificate may no longer qualify. |
| `reload: restart` → `watch` | compatible | |

The contract format itself is versioned by `apiVersion`: `v1alpha1` (fields may change), then `v1beta1` (additive only), then `v1`.

## 10. Feature flags are not environment configuration

They look similar, since both are often booleans, but they differ in every way that matters:

| | Environment config (docuconf) | Feature flags |
|---|---|---|
| Changes | With a rollout. Fixed for the life of a pod. | At runtime, with no deploy. |
| Scope | Per deployment. | Per request, user, tenant or percentage. |
| Owner | App and platform engineers. | Product, often through a UI. |
| Validated | Before deploy (CUE) and at boot. | At evaluation, with a fallback value. |
| Audit | Git history of the claim. | Flag service audit log. |

Rules:

- A variable belongs in the contract if, and only if, changing it requires a new rollout.
- Flags SHOULD use OpenFeature with a provider (flagd, LaunchDarkly, Unleash, and so on). The **provider's bootstrap settings**, such as `FLAGD_HOST` or an SDK key, *are* environment config and belong in the contract.
- SDKs SHOULD warn when a variable name matches `^(FF|FEATURE|FEATURE_FLAG|ENABLE)_`. The warning is only a hint: kill switches that are deliberately deploy-time may stay.
- Possible future work: a separate `FlagContract` kind, declaring flag keys, types and fallback values, validated against flagd or OpenFeature configuration with the same toolchain. It would be a different file and a different lifecycle, and is out of scope for v1.

## 11. SDK requirements

### 11.1 Build on the host library

Every language already has an environment library that teams trust. A docuconf SDK MUST extend it, not replace it: the host library keeps loading, parsing and binding, and its users keep its API, its docs and its idioms. The SDK adds only what the host lacks:

1. **Metadata the host cannot express:** descriptions where the host has none, `secret`, and constraints with no host equivalent.
2. **Validation the host does not do,** run after the host has parsed.
3. **Contract export,** read from the same declaration, so the contract cannot drift from the code.

| Language | Host library | Declaration the team already writes | docuconf adds |
|---|---|---|---|
| Go | [caarlos0/env](https://github.com/caarlos0/env) v11 | struct with `env`, `envDefault`, `required` tags | `desc`, `secret`, constraint tags; export runs a generated program in the app's module that reflects over the struct and reads doc comments from source |
| TypeScript | [T3 Env](https://env.t3.gg) with any [Standard Schema](https://standardschema.dev) validator (Zod, Valibot, ArkType) | `createEnv({ server: {...} })` | export through Standard JSON Schema; `secret` via schema metadata |
| Ruby | [anyway_config](https://github.com/palkan/anyway_config) | `Anyway::Config` subclass with `attr_config`, `required`, `coerce_types` | `describe`, `secret`, constraints, a `:duration` coercion; `rails docuconf:export` |
| .NET | Microsoft.Extensions.Options with DataAnnotations and the `[OptionsValidator]` source generator | options class with `[Required]`, `[Range]`, `[RegularExpression]`, `[AllowedValues]` | a source generator that emits the contract at build; `[Secret]`; env names from the configuration path |
| Python | [pydantic-settings](https://github.com/pydantic/pydantic-settings) | `BaseSettings` with `Field(description=..., ge=..., le=...)`, `SecretStr`, `Literal` | export through `model_json_schema()`; `SecretStr` maps to `secret` |
| Java | Spring Boot `@ConfigurationProperties` with Jakarta Validation | properties class plus `spring-boot-configuration-processor` | export from the generated configuration metadata plus validation annotations |

The SDK sets each variable's `encoding` (section 5) to whatever its host parses, so the host never needs a custom parser for platform-rendered values.

Where the host library's behaviour conflicts with a MUST in this spec (for example, empty-string handling), the SDK adapts the host with a pre-check or a custom parser. Where the conflict is only a wire format, the contract records it instead.

**Hosts that read config files** export them as section 4.4 describes. The SDK reads the files that ship in the image (the publish output, not the whole repo) at export time.

**Build-time variables are not part of the runtime contract.** Some frameworks inline variables into the bundle at build time: Next.js `NEXT_PUBLIC_*`, Vite `import.meta.env`, T3 Env's `client` section. Setting them on a pod does nothing, so SDKs MUST NOT export them as runtime variables. T3 Env's `shared` section (such as `NODE_ENV`) is also left out: it is read in both bundles and is a framework concern, covered by the well-known fragments proposed in section 13.

**Local file roots.** For development and tests, SDKs MUST support `DOCUCONF_FILE_ROOT`, a directory prepended to every absolute file input path, including a path read from a `pathEnv` variable.

### 11.2 Conformance requirements

A conforming SDK MUST:

1. Offer an idiomatic declaration API covering every type and field in section 4.
2. Validate the declaration itself at definition time: name format, description length, `details` not blank and at most 4000 characters, default against constraints, required without default, RE2-only patterns. Every input MUST have a `description`, and export MUST fail when one is missing or shorter than 5 characters. It comes from the language's natural doc location (section 14.7) or an explicit annotation, so that the documentation lives beside the code that reads the input.
3. Export a contract that matches the conformance golden file for the fixture declaration, compared as data (`cue export` to JSON), so formatting does not matter. Fields equal to their meta-schema default (`required: false`, `reload: "restart"`, `minCertificates: 1`) MAY be omitted; the comparison is made after unifying with the meta-schema. Durations are written in canonical form: units in the order `h`, `m`, `s`, `ms`, `us`, `ns`, each at most once, zero units omitted, and `0s` for zero (`1h30m`, not `90m`, `1.5h` or Go's `1h30m0s`). `metadata.generator` and the `encoding` fields are set by the SDK, so they are excluded from the comparison. Output MUST be deterministic: variables and file inputs sorted by name.
4. Load from the **process environment**, as it is when the process starts, by default. That is after any injection (section 4.5.1), so injected values are validated exactly like any other, and the SDK never resolves secret references itself. Configuration is never read at build time. Reading a `.env` file is an opt-in for development, and real environment variables override it.
5. Fail fast at boot with **all** violations reported together, each with a stable error code (`missing_required`, `invalid_type`, `out_of_range`, `pattern_mismatch`, `not_in_enum`, `invalid_scheme`, `too_few_items`, `too_many_items`, `file_missing`, `file_unreadable`, `file_too_large`, `file_malformed`, `schema_mismatch`, `certificate_invalid`, `certificate_expiring`, `certificate_name_mismatch`, `key_mismatch`, `keystore_unreadable`). Secret values are never printed. Length limits on strings and text files, and `itemMin`/`itemMax` on list items, use `out_of_range`. An expired or not-yet-valid certificate, a disallowed key algorithm or a broken chain is `certificate_invalid`; a CA bundle with too few certificates is `file_malformed`.
6. Expose typed values: a struct, a class, or an inferred TypeScript type. Not a string map.
7. Check every file input at boot, covering what the platform could not see:
   - the path exists and is readable, within `maxSize`;
   - `config` files parse in their `format` and bind to the app's type, which is the type their `schema` came from;
   - `tls`: the certificate and key parse and match, the certificate is currently valid with at least `minRemaining` left, covers every name in `dnsNames`, uses an allowed key algorithm, and chains to `ca.crt` when `requireCA` is set;
   - `caBundle` holds at least `minCertificates` parseable certificates; `keystore` opens with its password variable (an empty password when that optional variable is unset; where the host has no keystore parser, the SDK MUST at least verify the keystore's integrity MAC or, failing that, its format, and document the gap); `text` matches its constraints.
8. Honour `reload: watch` for every file input that declares it, typically by watching (or polling) the mount directory, since Kubernetes updates projected files by swapping a symlink. An SDK that cannot reload an input type MUST reject `watch` for it at declaration time rather than export a promise it does not keep.
9. Load declared config-file overlays (section 4.7) between the profile file and the environment, reloading them when declared `watch`; reject `overlays` at declaration time where the host cannot layer files, and reject an overlay `path` whose directory holds files the app ships with.
10. Ignore environment variables not in the declaration. A real process has many (`HOSTNAME`, `KUBERNETES_*`), so the unknown-variable check is only applied to platform values.
11. Offer a **contract-first** mode: validate an environment against a `contract.json` (the contract exported as JSON) with no in-language declaration, parsing every encoding in section 5 and returning typed values. The conformance runner uses this mode, and so can teams that want to author CUE by hand and export it with `cue export`. It MAY be an internal API, used only by the runner, while the SDK has no public use for it.
12. Pass the shared conformance suite (section 12).

An SDK SHOULD also:

- Export `details` for every input whose documentation has more than its description, from the same doc location (section 14.7). Documentation is generated by `docuconf docs` from the contract (section 14); an SDK does not need a generator of its own.
- Report `invalid_type` when a secret variable still holds an unresolved injector reference (a value starting with `vault:`, `op://` or `ref+`), because the injector did not run. The message names the variable and the reference scheme, never the value.
- Integrate with the framework around the host library: a Railtie, `ValidateOnStart` in .NET, a Next.js or NestJS adapter for T3 Env.

## 12. Conformance suite

The `conformance/` directory of docuconf-go is the shared suite. Cases are language-neutral, so every SDK runs the same ones and a disagreement between two SDKs is a bug in one of them.

- `load/*.yaml` holds the cases, written by hand. Each file declares variables and a list of cases. A case gives either `values` (typed platform values, which must pass `#Validate`) or `env` (raw strings, for input the platform would never send, such as malformed or out-of-range values), and, for `env`, either the expected typed result (`expect`) or the expected errors (`errors`, a list of variable and code).
- `cases.json` is generated from `load/` by `docuconf conformance`, and checked in. For each case it holds the full contract, unified with the meta-schema so defaults are explicit; the exact process environment the SDK sees; and either the typed value of every variable (`null` when absent) or the errors. A `values` case is rendered with `#Render` once per list and duration encoding the case leaves open, so one case tests every encoding, with `$` already reduced as Kubernetes does.
- A case may list `requires` tags: `int64` (the host holds every 64-bit integer) and `json-schema` (the SDK validates `json` values against their JSON Schema in contract-first mode). An SDK lacking a capability skips those cases and documents the gap. No other case may be skipped.

A conformance runner, one per SDK, runs every case in `cases.json` through the SDK's contract-first mode (section 11.2, item 11), with the case's `env` as the whole environment:

- For `expect`, loading succeeds and each variable's typed value, written as JSON, equals the expected one: durations in canonical form (section 11.2, item 3), integers exactly, floats numerically, lists as arrays.
- For `errors`, loading fails with exactly the listed variable and code pairs, in any order, and no error output contains the raw value of a secret variable.

The suite covers variables in v1. File inputs, profiles and overlays are tested by each SDK for now; cases for them are planned. Contract export is checked separately: each SDK writes a fixture declaration in its own language and vets the exported contract against the meta-schema (section 11.2, item 3).

## 13. Open questions

1. Should optional variables with no default be allowed at all, or should every variable be required or defaulted?
2. Should `#Validate` support a non-strict mode where unknown variables are warnings, to ease removals?
3. Should the spec cover build-time variables (section 11.1) with a separate `buildVars` section, so a CI build can be validated the same way?
4. Spring can activate several profiles at once (`SPRING_PROFILES_ACTIVE=prod,eu`). Should `profiles` support an ordered list, with later profiles winning?
5. Proposals arising from [`docs/EDGE_CASES.md`](../docs/EDGE_CASES.md):
   - **roles**, for one image running several processes;
   - **`requiredIf`**, for conditional requirements;
   - **well-known fragments**, for variables read by frameworks and libraries;
   - **platform-authored contracts**, for third-party images.
6. Should service-to-service sharing (the current Go library's `AddShared`) be a contract feature, through importable fragments, or stay an SDK-level convenience?
7. Should a file input be able to take a whole directory of arbitrary files (for example, every `*.crt` in a trust directory), rather than one file or a TLS key pair?
8. Should file inputs support profiles, so a baked-in `routes.yaml` can be the default for some environments, as `appsettings.{Environment}.json` is for variables?
9. Should `description` and `details` be translatable (a map by language tag), so generated docs can be published in more than one language?
10. Should the docs model turn a `json` variable's or config file's JSON Schema into a field table (name, type, required, description), rather than carry the schema for each renderer to show?

Resolved in this draft: generated docs come from one generator, `docuconf docs` in the CLI, through a versioned docs model that any renderer can read, and SDKs export an optional `details` beside the required `description` instead of generating docs themselves (section 14); per-item bounds for `int` lists (`itemMin`, `itemMax`, section 4.3); config-file overlays, rendered from `configKey` into a file of their own rather than replacing a baked-in one (section 4.7); values and files supplied at runtime by injectors (section 4.5.1); non-secret values may come from `configMapKeyRef`, the Downward API and resource fields (section 4.5); a `json` variable type exists, with schemas generated from code (sections 4.3 and 4.6).

## 14. Generated docs

Every input has a `description`, and may have `details` (section 4.2), so the contract holds everything a reader needs to know about an app's configuration. Documentation is generated from it by one generator, `docuconf docs` in the CLI, for every language. SDKs export the text; they do not render it.

### 14.1 Pipeline

```
contract.cue | contract.json ──build──▶ docs model (docs.json) ──render──▶ CONFIG.md          (developers)
                                                               └─render──▶ CONFIG.agents.md   (AI agents)
```

1. **Build.** The CLI unifies the contract with the meta-schema, so that defaults such as `required: false` and a list's `encoding` are explicit, and builds the docs model from it.
2. **Model.** The docs model is a public, versioned JSON document: `apiVersion: "docs.docuconf.dev/v1alpha1"`, `kind: "ConfigDocs"`. Its schema is `#DocsModel` in [`cue/docs/docs.cue`](cue/docs/docs.cue). It holds every fact a renderer shows, already phrased, so that the docuconf.dev website, an MCP server or a Backstage plugin can render from it without reading the contract, and every renderer words a fact the same way.
3. **Render.** A renderer MUST read only the model. The CLI's renderers accept a docs.json in place of a contract, and render it to the same bytes.

The model is versioned like the contract (section 9): `v1alpha1` may change, `v1beta1` will only add fields.

### 14.2 The docs model

```json
{
  "apiVersion": "docs.docuconf.dev/v1alpha1",
  "kind": "ConfigDocs",
  "service": {"name": "orders-api", "appVersion": "1.4.0", "generator": {"language": "go", "sdk": "docuconf-go", "version": "0.1.0"}},
  "profiles": {"selector": "DOTNET_ENVIRONMENT", "default": "Production", "names": ["Production", "Staging"]},
  "overlays": [{"name": "platform", "format": "json", "path": "/app/config/appsettings.Production.json", "keySeparator": ":", "reload": "watch"}],
  "groups": [
    {"name": "", "title": "General", "inputs": [
      {
        "name": "PORT", "kind": "var", "type": "int", "typeLabel": "integer",
        "required": false, "secret": false,
        "description": "HTTP listen port",
        "details": "Behind the mesh, keep the default.",
        "default": 8080,
        "defaultEnv": [{"name": "PORT", "value": "8080"}],
        "examples": ["9090"],
        "configKey": "Orders:Port",
        "wire": {"text": "a base-10 integer with no leading `+` or zeros, such as `8080`", "platform": "an integer"},
        "constraints": [{"rule": "range", "params": {"min": 1, "max": 65535}, "text": "between 1 and 65535"}],
        "sources": [
          {"kind": "literal", "text": "a literal value in the platform's values file"},
          {"kind": "configMapKeyRef", "text": "a key of a ConfigMap (`configMapKeyRef`), checked at boot"},
          {"kind": "resourceFieldRef", "text": "a container resource limit or request (`resourceFieldRef`), such as `limits.memory`"},
          {"kind": "injected", "text": "supplied when the container starts by an injector (`injected`), ...; checked at boot"},
          {"kind": "overlay", "overlay": "platform", "note": "the `platform` overlay, at key `Orders:Port`", "text": "a config-file overlay (`overlay`): ..."}
        ],
        "errors": ["invalid_type", "out_of_range"]
      },
      {
        "name": "serving-tls", "kind": "file", "type": "tls", "typeLabel": "TLS key pair",
        "required": false, "secret": true,
        "description": "Certificate to serve HTTPS with",
        "file": {"path": "/etc/orders/tls", "reload": "watch",
                 "contents": "a directory holding `tls.crt` and `tls.key` (PEM), as a `kubernetes.io/tls` Secret lays them out",
                 "reloadText": "the app reloads the file when it changes"},
        "constraints": [{"rule": "minRemaining", "params": {"minRemaining": "720h"}, "text": "at least 720h (30 days) of validity left"}],
        "sources": [{"kind": "secret", "note": "the whole `kubernetes.io/tls` Secret, with no key", "text": "a Kubernetes Secret (`secret`), one key per file"}, "..."],
        "errors": ["file_unreadable", "file_malformed", "certificate_invalid", "certificate_expiring", "key_mismatch"]
      }
    ]}
  ],
  "errors": [{"code": "invalid_type", "meaning": "The value does not parse as the input's type in its wire format, ...", "fix": "Write the value in the input's wire format. ..."}]
}
```

| Field | Content |
|---|---|
| `service` | The contract's `metadata`: name, `appVersion` when set, and the generator. |
| `profiles`, `overlays` | Present when the contract has them (sections 4.4 and 4.7): the selector, the default profile and the profile names; each overlay's format, path, key separator and reload. |
| `groups` | Every input, by `group`. Inputs with no group, or `group: ""`, form the group with `name: ""` and `title: "General"`, which comes first; the named groups follow in code point order, titled with their name. Within a group, variables come first, then files, each sorted by name. A group always has at least one input. |
| `groups[].inputs[]` | One input. `kind` is `var` or `file`; `type` is the contract type and `typeLabel` a phrase for it ("list of integers", "YAML config file"). `required`, `secret`, `group`, `description`, `details`, `deprecated` (`message`, `replacedBy`) and, for variables, `configKey` and `examples` are copied from the contract. |
| `default`, `defaultEnv` | Variables only. The contract's default as a typed platform value, and as the process environment holds it, in the wire format: one entry, or one per item for an `indexed` list. |
| `profileSelector`, `profileDefaults` | Variables only. Whether the variable selects the profile, and its default in each profile file, by profile name. |
| `wire` | Variables only. `encoding` and `separator` for lists and durations; `text`, how the value is written in the process environment; `platform`, how it is written in a values file. |
| `file` | Files only. `path`, `pathEnv`, `format` (config and keystore), `reload`, `maxSize`, and `contents` and `reloadText`, what the file holds and what a change to its source does, in plain words. |
| `constraints` | Each constraint as data and as a phrase (section 14.3). |
| `sources` | Where the platform may get the input (sections 4.5, 4.6.1, 4.7, 6): `kind`, `text`, the same for every input of that kind, and `note`, what is particular to this input (an overlay's key, an indexed list's `injected` without `ref`). |
| `errors` | The boot error codes (section 11.2, item 5) the SDK may report for the input, in the order of that item. Empty for an optional, unconstrained string. |
| `errors` (top level) | For each code any input may report: what it means and how to fix it. |

**Secrets** never have a value in the model: a secret input has no `default`, `defaultEnv`, `examples` or `profileDefaults`, and `#DocsModel` rejects one that does, whatever the contract held. Its `wire.platform` says that only a reference goes in a values file, and its `sources` are only the secret ones.

All phrases (`typeLabel`, `wire`, `constraints[].text`, `sources[].text` and `note`, `file.contents`, `errors`) are English sentence fragments in CommonMark inline syntax: literal values are code spans. The output is deterministic: no timestamps, a fixed field order, object keys in `params` and in values sorted, UTF-8 without `\u` or HTML escapes.

### 14.3 Constraints

Each constraint is `{rule, params, text}`. `params` holds the contract fields it comes from, under their contract names, and `text` the phrase every renderer shows:

| `rule` | From | Applies to | `text`, for example |
|---|---|---|---|
| `range` | `min`, `max` | `int`, `float`, `duration` | `between 1 and 65535`, `at least 1`, `at most 5m`, `exactly 3` |
| `length` | `minLength`, `maxLength` | `string`, `text` files | `at most 120 characters (Unicode code points)` |
| `pattern` | `pattern` | `string`, `text` files | ``matches the RE2 pattern `^[a-z]+$` `` when anchored with `^` and `$`, else ``contains a match for the RE2 pattern `[a-z]` `` (section 4.3) |
| `schemes` | `schemes` | `url` | `` `https` or `http` URL `` |
| `values` | `values` | `enum` | ``one of `debug`, `info` or `warn` `` |
| `itemCount` | `minItems`, `maxItems` | `list` | `between 1 and 5 items`, `at least 1 item` |
| `itemRange` | `itemMin`, `itemMax` | `list` | `each item between 0 and 1023` |
| `schema` | `schema` | `json`, `config` files | `matches the JSON Schema in the contract` (the schema is in `params`) |
| `maxSize` | `maxSize` | files | `at most 64 KiB (65536 bytes)` |
| `dnsNames` | `dnsNames` | `tls` | ``the certificate covers `a.example.com` and `b.example.com` `` |
| `keyAlgorithms` | `keyAlgorithms` | `tls` | ``key algorithm `ECDSA` or `RSA` `` |
| `minRemaining` | `minRemaining` | `tls` | `at least 720h (30 days) of validity left` |
| `requireCA` | `requireCA: true` | `tls` | ``includes `ca.crt`, and the certificate chains to it`` |
| `minCertificates` | `minCertificates` | `caBundle` | `at least 1 CA certificate` |
| `passwordVar` | `passwordVar` | `keystore` | ``opens with the password in `KS_PASSWORD` `` |

Constraints appear in this order. A pair of bounds is one constraint, phrased `between`, `at least`, `at most` or `exactly`. In the CLI the table is data: a new bound is one row. `itemLength` (from per-item string bounds `itemMinLength` and `itemMaxLength`, once the contract has them) is reserved, phrased `each item between 1 and 64 characters (Unicode code points)`.

### 14.4 Text and Markdown

- `description` is plain text. Renderers escape it for Markdown and keep it on one line.
- `details` is CommonMark, used as written, with one exception: its headings are demoted to nest under the input's own heading, so they cannot break the document's outline. A level-n heading becomes level base + n, at most 6, where base is the level of the input's heading; setext headings become ATX headings; fenced and indented code is left alone.
- An example of up to 60 characters, on one line and without surrounding spaces, is shown as a code span; a longer or multi-line one goes in a fenced code block.
- Non-ASCII text is written as is. Lengths are counted in Unicode code points, as everywhere in this spec.

### 14.5 Renderers

`--format markdown` writes the reference for developers, such as `CONFIG.md`:

1. `<!-- Generated by docuconf. Do not edit. -->`, the title, where the file comes from, and a count of the inputs.
2. A table of contents, by group, with each input's description and whether it is required, secret or deprecated.
3. "Environment variables", then "Files": a heading per group (when any input has a group), and a section per input with its description, a deprecation notice that links to the replacement, a table (type, required, secret, default, profile defaults, constraints, wire format, values-file form, config key, path, contents, reload, sources, boot errors), its JSON Schema in a collapsed block, its examples and its details.
4. "Profiles" and "Config-file overlays", when the contract has them; "Sources", what each source kind means; "Boot errors", each code's meaning and fix.

`--format agents` writes one file for AI agents, both coding agents working in the app's repository and agents that set deployment values. It can be included in an `AGENTS.md` or served as an `llms.txt`-style file:

1. **Hard rules** first: never put a secret value in code, a `.env` file, a values file, a ConfigMap or an annotation, and supply secrets only as `secretKeyRef`, secret files or `injected` (the secret inputs are listed); use each input's wire format for raw environment values and typed values in values files; validate with `docuconf vet` (platform values) or `docuconf check` (a running environment) before proposing a change; do not invent inputs that are not in the contract; set the required inputs that have no default; do not add uses of deprecated inputs.
2. **Using this config in code**, in terms that hold for every SDK: the declaration is the source of truth, values are read through the SDK's typed configuration, and `config key` says where a setting lives in the app's own configuration.
3. **Setting values**: one block per input, where every fact is a `key: value` line (kind, type, group, required, secret, deprecated, path, path variable, format, contents, reload, default, default in the environment, profile defaults, constraints, wire format, values-file form, config key, examples, schema, allowed sources, boot errors), then the description and the details. Then the source kinds, profiles and overlays.
4. **Boot errors**: what each applicable code means and how to fix it.

Both renderers are deterministic: the same model always gives the same bytes.

### 14.6 The command

```
docuconf docs <contract.cue | contract.json | docs.json> [--format model|markdown|agents] [-o file | --check file]
```

- The input is a contract, in CUE or as JSON, or a docs model, recognised by its `apiVersion`. A model is checked against `#DocsModel`, as is every model the CLI builds, before anything is rendered.
- `--format` defaults to `markdown`. `-o` writes a file; without it the output goes to standard output.
- `--check file` writes nothing: it exits 0 when the file is what would be generated, and otherwise exits 1 with a count of changed lines and a diff, as `docuconf export -check` does for the contract. CI uses it to keep committed docs current.

### 14.7 What SDKs export

An SDK MUST export `description` for every input and SHOULD export `details` (section 11.2), both from where programmers of that language already document a setting, so that the docs live beside the code:

| Language | `description` | `details` |
|---|---|---|
| Go | the field's doc comment: its first paragraph, on one line, without a final period (a `desc` tag as the fallback) | the rest of the doc comment, converted from Go doc comment syntax to Markdown (`# Heading`, lists, indented code, `[pkg.Name]` links) |
| TypeScript | `.describe()` or the schema's `description` metadata | the TSDoc/JSDoc comment of the property, or `.meta({details})` |
| Python | `Field(description=...)` | the attribute docstring, or `Field(json_schema_extra={"details": ...})` |
| .NET | `[Description]`, or the XML doc `<summary>` | the XML doc `<remarks>` |
| Java, Kotlin | the Javadoc/KDoc first sentence, as Spring's configuration metadata takes it | the rest of the Javadoc/KDoc |
| Ruby | `describe` | a `details` option of `describe`, or the YARD comment |
| Rust, Swift, Elixir, Gleam, C++, PHP, COBOL | the first paragraph of the field's doc comment (`///`, `@doc`, `/** */`, PHPDoc), or an explicit annotation | the rest of that doc comment |

Doc syntax specific to the language (Javadoc tags, XML doc elements, Go doc links) is converted to CommonMark or dropped. A `details` that would exceed 4000 characters fails export, as a short description does.
