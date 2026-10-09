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
- Secret storage and issuance. docuconf validates that a secret is *referenced*; External Secrets, Vault or the CSI driver store it, and generate, lease or revoke it. How a rotated value reaches the app, and how the contract declares the keys of a rotation that must overlap (a `keySet`), is covered (section 6.1); performing the rotation is not.
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
| `deprecated` | — | `{message, replacedBy?}`, for staged removal: the platform should stop setting the input. `message` says what to use instead, or why the input is going away: not blank, and at most 500 characters. `replacedBy` names the input that replaces it. A `required` input MUST NOT be deprecated, since the platform could not stop setting it. A deprecated input the platform still sets is a warning, never an error (section 7), and SDKs SHOULD warn at boot (section 11.2). |
| `configKey` | — | The app's own configuration key (SDKs MAY emit it even when it matches the env name): `Orders:CheckoutTimeout` in .NET, `orders.checkout-timeout` in Spring. Used in docs, so readers can find the setting in the app's config files. |

An optional variable with no default is legal. The SDK exposes it as absent (`nil`, `undefined`, `null`, `Option`). Whether an input is optional is the app's choice: docuconf does not prescribe how systems are set up.

### 4.3 Types

| `type` | Constraint fields | Platform value (CUE) | Wire string (section 5) |
|---|---|---|---|
| `string` | `minLength`, `maxLength`, `pattern` (RE2) | `string` | as is |
| `int` | `min`, `max` | `int` | base-10, no leading `+` or zeros: `8080` |
| `float` | `min`, `max` | `number` | shortest round-trip decimal: `0.5` |
| `bool` | — | `bool` | `true` / `false` |
| `duration` | `min`, `max` (durations), `encoding` | Go-syntax duration, e.g. `1m30s` | depends on `encoding` |
| `url` | `schemes`, `maxLength` | string with a `scheme://` | as is |
| `enum` | `values` (non-empty) | one of `values` | as is |
| `list` | `items` (`string`\|`int`), `encoding`, `separator` (csv only, default `,`), `minItems`, `maxItems`, `itemMin` and `itemMax` (`int` items only), `itemMinLength` and `itemMaxLength` (`string` items only) | list | depends on `encoding` |
| `keySet` | `secret: true` (always), `encoding`, `separator` (csv only, default `,`), `minKeys` (default 1, at least 1), `maxKeys` (default 2, at least `minKeys`), `keyMinLength`, `keyMaxLength` (at least 1) | a secret reference only | as a `list` of strings (section 5) |
| `json` | `schema` (JSON Schema), `maxLength` | any JSON value | compact JSON |

Rules:

- `pattern` is RE2, the only regex dialect every SDK can match exactly (Go native, `re2` bindings or a compatible subset elsewhere). SDKs MUST reject patterns that use features outside RE2, such as lookaround or backreferences, at declaration time.
- RE2's `\d`, `\w`, `\s` and `\b` are ASCII-only. Where the host engine treats them as Unicode (Python, .NET, Rust's `regex` by default), SDKs SHOULD warn and suggest explicit classes such as `[0-9]`, since the platform and the app would otherwise disagree on non-ASCII input.
- `pattern` matches **anywhere** in the value, as CUE's `=~` and JSON Schema's `pattern` do; anchor it with `^` and `$` to match the whole value. Some host libraries match the whole value instead (.NET `[RegularExpression]`, Java `@Pattern`); their SDKs MUST anchor such patterns on export, as `^(?:p)$`, so the platform and the app accept exactly the same values.
- **Lengths count characters**, meaning Unicode code points, never bytes or UTF-16 code units. This applies to `minLength` and `maxLength` on a `string`, to `maxLength` on a `url` or `json` value, to `itemMinLength` and `itemMaxLength` on each item of a `string` list, to `keyMinLength` and `keyMaxLength` on each key of a `keySet`, and to text files (section 4.6). `日本` is 2 characters, and `ZÜ01` fits an `itemMaxLength` of 4. CUE's `strings.MinRunes`/`MaxRunes` and JSON Schema's `minLength`/`maxLength` count the same way. A host whose strings are UTF-16 (Java, .NET, JavaScript) counts code points, not `length`. An app that stores values in fixed-width byte fields, such as a COBOL `PIC X(n)`, should declare a limit that leaves room for multi-byte characters, or reject them with a `pattern` such as `^[ -~]*$`.
- `maxLength` on a `url` bounds the URL string as it is.
- `maxLength` on a `json` value bounds its **wire string**. Before deploy, that is the compact JSON `#Render` writes: no insignificant whitespace, object fields in the order the platform wrote them, and no escaping beyond what JSON requires (`<`, `>` and `&` stay as they are). At boot, it is the raw value the app receives, whitespace included, before the SDK parses it. A value the platform rendered measures the same in both places. A `json` value read from a config-file overlay (section 4.7) is not a string, so the SDK measures its compact JSON. The Helm values schema cannot express this limit, so it is checked by `docuconf vet` and at boot.
- `itemMinLength` and `itemMaxLength` apply to each item after the list is split in its encoding, so a `csv` separator is never counted. They MUST NOT be set on an `int` list, just as `itemMin` and `itemMax` MUST NOT be set on a `string` list.
- `default` MUST satisfy the variable's own constraints. SDKs MUST check this at declaration time.
- A `json` variable carries a structured value, such as a rate-limit object. Its `schema` is a JSON Schema the SDK generates from the app's own type, so the platform checks the value against the same type the app deserializes into (section 4.6).
- A `keySet` is a set of secret keys that are all valid at once, so one can be rotated without an outage (section 6.1). It is for the side that verifies: webhook signatures, inbound API keys, JWT HMAC verification, cookie-signing fallbacks. `secret` MUST be `true`, so it has no `default` and no `examples`, and the meta-schema rejects anything else. It travels in a list's wire encodings, with the same `encoding` and `separator` (section 5). Keys are never trimmed. The number of keys outside `minKeys`..`maxKeys` is `too_few_items` or `too_many_items`; a key outside `keyMinLength`..`keyMaxLength`, and an empty key whatever the bounds (a stray separator), is `out_of_range`. Like every secret, no message holds a key.
- A `keySet`'s typed value is its keys, in the order the platform gave them: SDKs MUST expose them as ordered keys, and SHOULD offer a constant-time `contains(candidate)` and a helper that tries every key with a check the caller supplies, such as an HMAC comparison, without stopping at the first match. In conformance JSON (section 12) the value is an array of strings.
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
| `secretKeyRef` | `{secretKeyRef: {name: "db", key: "url"}}` | secret variables, including every `keySet` (one of the two secret sources) | at boot |
| `injected` with `ref` | `{injected: {provider: "bank-vaults", ref: "vault:secret/data/db#url"}}` | every type, including secrets; not a list or key set in the `indexed` encoding | the reference's shape before deploy; the value at boot |
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

Making sure the injector actually runs is the platform's job and outside the contract. Most injectors are switched on, and told what to do, by annotations or labels on the pod; the values and files documents may declare those next to the `injected` source, and `#Render` puts them on the pod template (section 4.5.2). Anything else an injector needs (the webhook installed in the cluster, a namespace label, a ServiceAccount annotation, a custom resource) stays the platform's. When the injector does not run, the app receives the raw reference, and the SDK's boot check is what catches it: a URL or pattern fails its constraints, and section 11.2 asks SDKs to recognise an unresolved reference outright.

#### 4.5.2 Enabling the injector

An `injected` source, for a variable or for a file input (section 4.6.1), MAY carry the pod annotations and labels its injector needs:

| Field | Type | Rule |
|---|---|---|
| `podAnnotations` | map of string to string | Keys are Kubernetes qualified names after expansion: an optional DNS-subdomain prefix of at most 253 characters and `/`, then a name of at most 63 characters matching `[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?`. Values are any string. |
| `podLabels` | map of string to string | Keys as for `podAnnotations`. Values are empty, or at most 63 characters matching the same pattern as a name. |

Values are strings: YAML's unquoted `true` is a boolean and is rejected, so write `"true"`.

The values document MAY also have its own `podAnnotations` and `podLabels`, beside the variables, for settings shared by every injected input, such as `vault.hashicorp.com/agent-inject: "true"` or the Vault role. Variable names are upper case (section 4.2), so these two keys never collide with one. The files document has no shared fields; its inputs' annotations merge with the values document's.

docuconf knows no injector. `provider` stays an uninterpreted label (section 4.5.1), and keys and values are passed through after one step, **placeholder expansion**, so an annotation that names one input can be written once, beside it:

| Placeholder | Expands to | Defined for |
|---|---|---|
| `{input}` | the variable's or file input's name | variables, file inputs |
| `{path}` | the file input's `path` | file inputs |
| `{dir}` | the directory the input occupies: `path` for a `tls` input, its parent directory otherwise (the mount directory of section 4.6) | file inputs |
| `{file}` | the last element of `path` | file inputs |

Expansion replaces every occurrence of these four tokens in keys and values, and nothing else: there is no escape, and other braces, such as a Vault Agent template's `{{ .Data }}`, are left alone. A token the source does not define (`{path}` on a variable, any token in the values document's shared maps) is a `#Validate` error (`undefinedPlaceholder`), so a typo never reaches the pod. Keys and values are checked after expansion.

**Rendering.** `#Render` emits two maps, `podAnnotations` and `podLabels`, merged from the values document's shared ones and then every injected variable and file input in contract order. They belong on the **pod template's** metadata (`spec.template.metadata` of a Deployment, StatefulSet, Job or CronJob's job template), not on the workload's own metadata: mutating webhooks see pods, never Deployments. A change to either map changes the pod template and so rolls the pods, which is what a changed injector setting needs. A source that is not `injected` cannot carry these fields, so nothing is ever emitted for a literal, a reference or a mounted file. A platform without injectors gets two empty maps.

**Merging.** The same key with the same value from several sources is fine: two Vault Agent inputs may each repeat `agent-inject: "true"`. The same key with different values is a `#Validate` error (`conflictingPodAnnotation`, `conflictingPodLabel`) naming the key and both sources, because one of them would otherwise silently lose. The render itself fails on such a conflict too.

**Not secret.** Annotations and labels are visible to anyone who can read the pod, and are copied into events, audit logs and monitoring. They MUST hold references and settings (a Vault path, a role, an address, a template), never secret material, by the same argument as `ref` in section 4.5.1. docuconf cannot tell a password from a path, so this rule is the platform's; `#Validate` treats the values as plain data and error messages print them.

**Why the contract does not change.** The contract describes what the app reads; the same image runs under the Vault Agent injector in one cluster, Bank-Vaults in another and a CSI volume in a third. Which injector runs, and what it needs on the pod, is a fact about the cluster, so it lives only in the platform's values and files documents. SDKs, exporters and the contract meta-schema are unchanged by this section.

Three examples, rendered in [`cue/testdata/render/injectorPodOut.yaml`](cue/testdata/render/injectorPodOut.yaml) from [`cue/examples/injector_pod.cue`](cue/examples/injector_pod.cue). The **Vault Agent injector** writes a secret config file `db-creds` declared at `path: /vault/secrets/db.json`. The agent writes each secret to `<secret-volume-path>/<agent-inject-file>`, defaulting to `/vault/secrets/<name>`, where `<name>` is the suffix of `agent-inject-secret-<name>`, lower-cased unless `preserve-secret-case` is set. Using `{input}`, a DNS label, as that name, and setting the directory and file name from `{dir}` and `{file}`, puts the file at exactly `path`, wherever the contract declares it; a template makes the file JSON rather than the agent's default Go-map format:

```yaml
# values.yaml
podAnnotations:                      # shared: one agent per pod, one role
  vault.hashicorp.com/agent-inject: "true"
  vault.hashicorp.com/role: ledger
# files.yaml
db-creds:
  injected:
    provider: vault-agent
    podAnnotations:
      vault.hashicorp.com/agent-inject-secret-{input}: database/creds/ledger
      vault.hashicorp.com/agent-inject-template-{input}: '{{- with secret "database/creds/ledger" -}}{{ .Data | toJSON }}{{- end }}'
      vault.hashicorp.com/secret-volume-path-{input}: "{dir}"
      vault.hashicorp.com/agent-inject-file-{input}: "{file}"
```

renders

```yaml
podAnnotations:
  vault.hashicorp.com/agent-inject: "true"
  vault.hashicorp.com/role: ledger
  vault.hashicorp.com/agent-inject-secret-db-creds: database/creds/ledger
  vault.hashicorp.com/agent-inject-template-db-creds: '{{- with secret "database/creds/ledger" -}}{{ .Data | toJSON }}{{- end }}'
  vault.hashicorp.com/secret-volume-path-db-creds: /vault/secrets
  vault.hashicorp.com/agent-inject-file-db-creds: db.json
podLabels: {}
```

**Bank-Vaults** resolves the reference in an env value; its webhook takes the Vault address and role from annotations:

```yaml
DB_PASSWORD:
  injected:
    provider: bank-vaults
    ref: "vault:database/creds/ledger#password"
    podAnnotations:
      vault.security.banzaicloud.io/vault-addr: https://vault.vault.svc:8200
      vault.security.banzaicloud.io/vault-role: ledger
```

renders `DB_PASSWORD=vault:database/creds/ledger#password` in `env` and those two annotations. Not every injector handles secrets. The **OpenTelemetry operator** sets `OTEL_EXPORTER_OTLP_ENDPOINT` on pods carrying its annotation, and some injectors key on a label instead, such as Azure Workload Identity:

```yaml
OTEL_EXPORTER_OTLP_ENDPOINT:
  injected: {provider: otel-operator, podAnnotations: {instrumentation.opentelemetry.io/inject-java: "true"}}
AZURE_CLIENT_ID:
  injected: {provider: azure-workload-identity, podLabels: {azure.workload.identity/use: "true"}}
```

renders no env entries, `podAnnotations: {instrumentation.opentelemetry.io/inject-java: "true"}` and `podLabels: {azure.workload.identity/use: "true"}`.

Edge cases:

- **Injector disabled or not installed.** The annotations are on the pod, but no webhook acts on them, and Kubernetes ignores annotations nobody reads. The app starts with the raw reference in its env value, or without the variable or file. The SDK's boot check catches it: an unresolved reference (section 11.2), `missing_required`, or `file_missing` for an injected file. docuconf cannot see from the values document whether a webhook is installed.
- **Injectors that read the namespace.** Some injectors are enabled by a label on the Namespace (Istio's `istio-injection`, Bank-Vaults' namespace selector, Linkerd's annotation on the Namespace). Namespace metadata is not part of a workload's render and is out of scope; set it where the platform manages namespaces. Pod-level settings for the same injectors still work here.
- **Pod template, not the workload.** Webhooks see pods, so `#Render`'s maps go on the pod template. Putting them on the Deployment's own metadata does nothing. Some tools put the same key on both; only the pod template's counts.
- **Labels and selectors.** A workload's selector labels are the platform's own. A `podLabels` key that is also a selector label is a conflict the platform must refuse when it assembles the pod template; changing a selector label of an existing Deployment is not allowed by Kubernetes.
- **Length limits.** Key length (prefix 253, name 63) and label values (63) are checked after expansion, so an `{input}` or `{file}` that makes a key too long fails `#Validate`. Kubernetes also caps a pod's annotations at 256 KiB in total; a large Vault Agent template counts toward it. That limit is not checked before deploy.
- **ServiceAccount and other objects.** Some injectors need metadata elsewhere: Azure Workload Identity and EKS's IAM roles for service accounts read an annotation on the ServiceAccount, the OpenTelemetry operator needs an `Instrumentation` resource, the Vault Agent injector a Kubernetes auth role in Vault. These are out of scope; `podAnnotations` and `podLabels` only cover the pod.
- **Reserved prefixes.** Keys under `kubernetes.io/` and `k8s.io/` are reserved by Kubernetes. They are valid qualified names and are not rejected, but a platform policy (section 7) may forbid them.

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
| `injected` | any | Nothing about the content. An injector, such as the Vault Agent injector rendering a template to `/vault/secrets`, writes the file at `path` when the pod starts. `#Render` emits no volume or mount for it. The source may carry the pod annotations and labels that make the injector write to the declared `path` (section 4.5.2), with `{path}`, `{dir}` and `{file}` expanded from the input. |

Inline content is a string, written to the file as given. For a `json` or `yaml` config file it may instead be structured data, which is checked against the file's `schema` and serialized in the file's `format`. The exact bytes of serialized content, and so the ConfigMap's hash, belong to the renderer: the CUE renderer keeps the order fields are written in, while Helm sorts object keys and indents lists differently. Two renderers MUST produce content that parses to the same data, and each MUST name the ConfigMap from a hash of the bytes it wrote; they need not agree on the bytes. A platform that needs byte-identical output across tools gives the content as a string.

Fields described as **resolved** (a Secret's `type` and `keys`, a Certificate's spec) are filled in by the platform tooling from the cluster. They are metadata, never secret contents. When they are absent, those checks move to boot.

#### 4.6.2 Rotation

A source that changes after deploy (a renewed certificate, an updated ConfigMap) only reaches an app that rereads it. `reload` makes this part of the contract:

- `watch`: the app reloads the file. The platform does nothing more.
- `restart`: `#Render` lists the source under `restartTriggers`, and the platform MUST roll the pods when it changes (for example with a reloader controller, or by hashing the source into a pod annotation).

Inline content is content-hashed, so it always rolls the pods when it changes.

Section 6.1 covers rotation for every kind of input, including variables, injected values and dynamic secrets.

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

A `keySet` uses the same encodings and the same `separator` as a list of strings, with keys for items: during a rotation a `csv` key set is `old,new`.

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

### 6.1 Rotation

A source's value can change while the app runs: a rotated API key or database password, a renewed certificate, an edited ConfigMap. Whether and when the app sees the new value depends on how the input reaches it.

**Environment variables are read once.** A process's environment is fixed when it starts. A variable from a `secretKeyRef` or `configMapKeyRef` keeps its old value in a running pod after the Secret or ConfigMap changes, and so does an `injected` one: the injector (Bank-Vaults' `vault-env`, `op run`) resolved it when the container started. A new value reaches the app on the next restart or redeploy, and starting one is the platform's job: a reloader controller, a checksum of the source on the pod template, the injector's own rollout, or a plain `kubectl rollout restart`. `#Render` lists no variable source in `restartTriggers`, and the contract has nothing to declare: every variable is restart-only.

**Files** say how they take a change with `reload` (section 4.6.2): `watch` when the app rereads the file itself, `restart` when it reads it once and the source is a restart trigger.

**Dynamic secrets** have a lease: Vault database credentials, cloud tokens with an expiry. A lease can end long before the pod next restarts, so they should not be environment variables. Deliver them one of two ways:

- as a **file** that an agent keeps current (the Vault Agent injector, or the Secrets Store CSI driver with rotation enabled), declared `reload: watch`, so the app rereads it when the agent writes new credentials; or
- **fetched by the app itself**, from Vault or the cloud provider's API. The contract then declares what the app needs to fetch them, such as the Vault address and role (`VAULT_ADDR`, `VAULT_ROLE`), not the credential.

**Dual-lifecycle keys.** Some keys must be rotated without a moment when the old one is already gone and the new one not yet in use: webhook signing keys, service-to-service API keys, token signing keys. During the overlap two keys are valid. The two sides of such a key differ:

- A **verifier** (the side that checks a signature or an incoming key) accepts any key in a set. It declares a `keySet` (section 4.3), preferably:

  ```cue
  WEBHOOK_KEYS: {
  	type:         "keySet"
  	secret:       true
  	description:  "Keys that verify the signature on incoming payment webhooks"
  	minKeys:      1   // the default
  	maxKeys:      2   // the default
  	keyMinLength: 32
  	keyMaxLength: 256
  }
  ```

  The platform supplies it like any secret, as one Secret key holding `old,new` during the overlap. An empty key (a trailing comma) or a truncated one fails at boot (`out_of_range`) instead of locking callers out. A rotation takes three steps, which the generated docs print for every key set (section 14), so an app need not repeat them in `details`:

  1. add the new key, and roll out;
  2. switch the sender (the caller, or the signer) to the new key;
  3. remove the old key, and roll out.

  A host that cannot offer a key set may declare two variables instead, a required key and an optional previous one, with the same length limits, and accept either:

  ```cue
  API_KEY: {
  	type:        "string"
  	description: "Key that callers present"
  	secret:      true
  	required:    true
  	minLength:   32
  	maxLength:   256
  }
  API_KEY_PREVIOUS: {
  	type:        "string"
  	description: "The key API_KEY replaced, accepted until every caller has switched"
  	secret:      true
  	minLength:   32
  	maxLength:   256
  }
  ```

  Rotation is the same three steps: set `API_KEY_PREVIOUS` to the old key and `API_KEY` to the new one, roll out; switch the callers; unset `API_KEY_PREVIOUS`, roll out.

  Earlier drafts recommended a secret `list` of strings in `csv` encoding, with `minItems: 1`, `maxItems: 2` and item length limits. That convention stays valid, with the same wire format, but is no longer the recommendation: a `keySet` says what the list is for, rejects an empty key without a length limit, and gets its rotation steps in the docs. Changing a variable from such a list to a `keySet` changes its `type` (section 9).
- A **caller** (the side that presents or signs with a key) uses one key at a time. It declares a single secret, and rotates in step 2 above by updating its Secret, which reaches it on its next restart or redeploy like any other variable.

The SDK enforces a key set's constraints at boot (section 11.2, item 5); the conformance suite covers the type in `conformance/load/key_set_type.yaml`, and the list convention in `key_set.yaml`. SDKs SHOULD give the verifier a constant-time `contains` and a helper that tries every key (section 4.3), but accepting a key is the app's own code: docuconf delivers the keys and checks their shape, never the keys themselves.

**What the platform cannot check.** A rotation is only safe if every rollout keeps one key in common with the one before it: step 1 adds a key and keeps the old one, step 3 removes the old one only after step 2. The platform never sees secret values, since it supplies a reference, so neither `#Validate` nor `docuconf vet` can check this; following the steps in order is the operator's job. A controller with read access to the Secrets could compare consecutive versions, but that is out of scope for docuconf.

## 7. Platform validation

The meta-schema provides two definitions, both exercised by `cue/test.sh`:

**`#Validate`**: given `contract`, `values`, `files` (sources) and the compiled `#schemas`,

- every `required` variable must be set by the platform or by the selected profile (section 4.4), and any that are not are listed in `missingRequired`,
- every value must satisfy its variable's type and constraints (`checks.<NAME>`), and every file source must pass the checks in section 4.6.1 (`fileChecks.<name>`),
- every required file input must have a source,
- any value or file source not declared in the contract is rejected. A typo like `DATABSE_URL` is the most common environment bug, so this check is always on; an input on its way out is marked `deprecated` instead (section 4.2),
- every deprecated input the platform still sets, as a value, an overlay value or a file source, is listed in `deprecatedSet` with its `deprecated` notice. These are **warnings**: they never make the values invalid. `docuconf vet` prints one line per warning with its message, and exits 0 when there are only warnings,
- the pod annotations and labels of injected sources, and the values document's shared ones, are valid after placeholder expansion and do not set one key to two values (`podChecks`, section 4.5.2).

**`#Render`**: produces the container's `env` entries (in each variable's wire encoding, plus every `pathEnv`), the `volumes` and `volumeMounts` for file inputs and overlays, the ConfigMaps for inline content and overlays (section 4.7), the `restartTriggers` (section 4.6.2), and the `podAnnotations` and `podLabels` that injected sources ask for, for the pod template's metadata (section 4.5.2).

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
- **Fallback:** an image label `dev.docuconf.contract` holding the contract, base64-encoded. This only works for small contracts, because of label size limits, and is only needed where a registry cannot store the artifact at all: registries without the referrers API still hold it under the referrers tag schema (below).
- **Local and GitOps:** commit `contract.cue` beside the claim. This is fine for getting started but invites version skew.

### 8.1 The artifact

| Field | Value |
|---|---|
| Manifest | An OCI image manifest (`application/vnd.oci.image.manifest.v1+json`), as OCI 1.1 packs artifacts. |
| `artifactType` | `application/vnd.docuconf.contract.v1alpha1+cue`. The version follows the contract's `apiVersion`, so the format freeze changes it to `v1beta1`. |
| `config` | The empty descriptor (`application/vnd.oci.empty.v1+json`). |
| `layers` | One layer with the same media type as `artifactType`, holding `contract.cue` exactly as the SDK exported it, with the annotation `org.opencontainers.image.title: contract.cue`. |
| `subject` | The image's manifest or index, by digest. |
| Annotations | `org.opencontainers.image.created` (RFC 3339) and `dev.docuconf.contract.name` (`metadata.name`). |

On a registry with the referrers API, the registry indexes the artifact by its `subject`. On one without it, the client keeps the index itself under the **referrers tag schema** of the OCI distribution spec: an image index tagged `sha256-<hex of the image digest>` that lists every artifact referring to the image. Clients that read referrers (oras, cosign, `docuconf pull`) try the API and fall back to the tag.

### 8.2 `docuconf push` and `docuconf pull`

```
docuconf push --image registry/repo@sha256:<digest> [--plain-http] contract.cue
docuconf pull --image registry/repo@sha256:<digest> | registry/repo:<tag> [-o contract.cue] [--plain-http]
```

- `push` validates the contract, then pushes the artifact above with the image as its subject, using the referrers API or the referrers tag schema. The image MUST be given by digest, so the contract is tied to one build; a tag is refused. Pushing a contract that is already there (the same `contract.cue` bytes for the same image) pushes nothing and reports the existing artifact.
- `push` prints the artifact's reference, `registry/repo@sha256:<artifact digest>`, on standard output, and nothing else, so CI can sign it: `cosign sign $(docuconf push --image ... contract.cue)`. The CLI does not sign; verify with `cosign verify` on the same reference, or with cosign's policy for referrers of the image.
- `pull` resolves a tag to a digest first, then lists the image's referrers of the contract artifact type. When several contracts refer to one image, the newest by `org.opencontainers.image.created` wins, and `pull` says so on standard error. When none does, it reads the `dev.docuconf.contract` label of the image's config (an image manifest only, since each platform of an index has its own config). The contract is checked against the meta-schema before it is written to `-o` or standard output.
- `pull` exits 1 when the image has no contract, and 2 on any other error (an unknown image, a registry or credentials error, an invalid contract).
- Both read credentials as `docker login` and oras do: from `$DOCKER_CONFIG/config.json`, or `~/.docker/config.json`, and the credential helpers it names. `--plain-http` talks to a registry over HTTP, for a local test registry.
- A platform that validates by image digest runs `docuconf pull --image <image@digest> -o contract.cue`, then `docuconf vet -contract contract.cue ...`, so it can never validate image B against contract A.

## 9. Compatibility

`docuconf diff old.cue new.cue` classifies every change. CI SHOULD block merges with an unacknowledged breaking change.

| Change | Class | Why |
|---|---|---|
| Add an optional variable | compatible | |
| Mark an input `deprecated` | compatible | Reported as notable: the platform should stop setting it. Values that still set it pass, with a warning (section 7). |
| Add a required variable | **breaking** | Existing values no longer validate. |
| Remove a variable | **breaking for the platform** | Values that still set it fail the unknown-variable check. Deprecate it first. Removing an input that the old contract already marked `deprecated` is still breaking for the platform, but `diff` says it was deprecated, so a platform that heeded the warning is unaffected. |
| Optional → required | **breaking** | |
| Required → optional | compatible | |
| Change `type` or `secret` | **breaking** | The value's shape changes. |
| Secret `list` of strings → `keySet` (section 6.1) | **breaking** for the contract | `type` changes, so a strict diff and the SDK's declaration change; the wire format is identical, so the Secret that holds the keys, and the values document, need no change. |
| Tighten a constraint (narrower range, fewer enum values, new pattern) | **breaking** | |
| Loosen a constraint | compatible | |
| Change `description`, `details`, `group`, `examples` or `default` | compatible | `default` changes alter behaviour, so diff reports them as notable. `description` and `details` are docs only. |
| Add a required file input, or make one required | **breaking** | Existing sources no longer validate. |
| Change a file input's `path`, `type` or `format` | **breaking** for the app image only | The platform re-renders the mount; nothing in the values changes. Reported as notable. |
| Tighten a `schema` (new required property, narrower type) | **breaking** | Existing config files may no longer validate. Compared structurally after compiling both schemas. |
| Add `dnsNames`, raise `minRemaining`, narrow `keyAlgorithms` | **breaking** | The existing certificate may no longer qualify. |
| `reload: restart` → `watch` | compatible | |

The contract format itself is versioned by `apiVersion`: `v1alpha1` (fields may change), then `v1beta1` (additive only), then `v1`.

### 9.1 `docuconf diff`

```
docuconf diff <old.cue | old.json | -> <new.cue | new.json | -> [--format text|json] [--allow-breaking] [--ack file]
```

Both contracts are unified with the meta-schema first, so defaults (`required: false`, `reload: restart`, a list's `encoding`, a key set's `minKeys`) are explicit on both sides and a default written out is not a change. Either side, not both, may be `-` for standard input; JSON is recognised by a leading `{`.

Every change gets a **change id** and one of four classes:

| Class | Text label | Meaning |
|---|---|---|
| `compatible` | `ok` | Nothing the platform supplies stops working. |
| `notable` | `NOTABLE` | Behaviour changes, or the app image changes in a way the platform re-renders (the rows above marked "reported as notable" or "breaking for the app image only"). |
| `breaking-platform` | `BREAKING` (`platform only` after the change id) | Values or sources that still set something the contract dropped fail: removed inputs and overlays. |
| `breaking` | `BREAKING` | Existing values or sources may no longer validate. |

Constraints are compared field by field, as bounds: a lower bound (`min`, `minLength`, `minItems`, `itemMin`, `itemMinLength`, `minKeys`, `keyMinLength`, `minCertificates`, `minRemaining`) tightens when it is added or raised, and an upper bound (`max`, `maxLength`, `maxItems`, `itemMax`, `itemMaxLength`, `maxKeys`, `keyMaxLength`, `maxSize`) when it is added or lowered. Durations compare as durations. A list of allowed values (`values`, `schemes`, `keyAlgorithms`) tightens when a value is removed, or when the list appears where any value was allowed; `dnsNames`, a list of required names, tightens when a name is added. Changes the table does not list are classified conservatively:

| Change | Class |
|---|---|
| `pattern` added or changed (diff cannot compare regular expressions) | breaking |
| `pattern` removed | compatible |
| List `items` changed | breaking |
| `encoding` or `separator` changed | notable (the platform re-renders the wire value) |
| `encoding` changed to `indexed` | breaking for the platform: an `injected` reference can no longer supply it (section 4.5) |
| `default` added or removed | notable |
| `deprecated` removed or its message changed | compatible |
| `configKey` changed | notable; removed while the contract has overlays: breaking for the platform |
| File input made `secret` | breaking (inline and `configMap` sources no longer validate) |
| File `pathEnv` changed | notable |
| `requireCA` set | breaking |
| `reload: watch` → `restart` | notable: the platform must now roll the pods |
| Overlay added | compatible |
| Overlay removed | breaking for the platform |
| Overlay `path`, `format` or `keySeparator` changed | notable, breaking for the app image only |
| Profile value added or changed, `profiles.selector` or `profiles.default` changed | notable |
| Profile value removed for a required variable | breaking: the platform must now supply it when that profile is selected |
| `metadata.name` or `apiVersion` changed | notable (`appVersion` and `generator` are ignored) |
| `schema` added | breaking |
| Any field diff does not know | breaking, "changed in a way diff cannot classify" |

`injected` sources live in the platform's values, not the contract, so only their one contract-side rule shows up (`indexed`). An injected value is checked at boot, against the new contract's constraints like any other.

**JSON Schemas** (`schema` on `json` variables and config files) are compared structurally, keyword by keyword, following local `$ref`s into `$defs` and `definitions`: `type` (as a set; `integer` is within `number`), `required`, `properties`, `additionalProperties` and `items` (absent means `true`), `enum`, `const`, `pattern`, `format`, `uniqueItems`, and the numeric and length bounds. A property only one side declares is compared with what the other side's `additionalProperties` allowed for that key; a new property on an open object is notable, since values may already set that key in another shape. Annotations (`description`, `title`, `examples`, `default` and the like) are compatible. Any other keyword that differs, such as `oneOf`, `allOf` or a remote `$ref`, is reported as breaking with the reason "schema changed in a way diff cannot classify".

**Output.** By default, one line per change, then a summary line:

```
BREAKING  WORKER_COUNT: max lowered from 64 to 32 [max-tightened]
BREAKING  LEGACY_MODE: variable removed; it was deprecated (...) [var-removed, platform only]
NOTABLE   PORT: default changed from 8080 to 9090 [default-changed]
ok        LOG_LEVEL: description changed (docs only) [description-changed]
orders-api: 2 breaking, 1 notable, 1 compatible
```

`--format json` prints a list of `{"input", "change", "class", "reason"}` objects, with `"acknowledged": true` on acknowledged changes, and `[]` when nothing changed. Variables and file inputs are named as in the contract, overlays as `overlays.<name>`, and profile settings by `profiles` or the variable they set.

**Exit status:** 0 when no change is breaking, 1 when one is (in either breaking class), 2 on a usage or parse error. `--allow-breaking` prints the same and exits 0.

**Acknowledging a change.** CI blocks unacknowledged breaking changes. `--ack file` names accepted ones, one per line as `<input> <change-id>`, with `#` comments:

```
# the platform stopped setting it in platform PR #142
LEGACY_MODE var-removed
WORKER_COUNT max-tightened
```

An acknowledged change is still printed, marked `acknowledged`, and does not fail the run. An acknowledgment that matches no breaking change is reported on standard error, so stale lines can be cleaned up. Keep the file in the app's repository and empty it after the release that carried the change.

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
5. Fail fast at boot with **all** violations reported together, each with a stable error code (`missing_required`, `invalid_type`, `out_of_range`, `pattern_mismatch`, `not_in_enum`, `invalid_scheme`, `too_few_items`, `too_many_items`, `file_missing`, `file_unreadable`, `file_too_large`, `file_malformed`, `schema_mismatch`, `certificate_invalid`, `certificate_expiring`, `certificate_name_mismatch`, `key_mismatch`, `keystore_unreadable`). Secret values are never printed. Length limits (`minLength`, `maxLength`, `itemMinLength`, `itemMaxLength`, `keyMinLength`, `keyMaxLength`, on variables and text files), `itemMin`/`itemMax` on list items, and an empty key in a `keySet`, use `out_of_range`; the number of a key set's keys uses `too_few_items` and `too_many_items`. A too-long secret reports its length, never its value. An expired or not-yet-valid certificate, a disallowed key algorithm or a broken chain is `certificate_invalid`; a CA bundle with too few certificates is `file_malformed`.
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
- Log a warning at boot for each deprecated input (section 4.2) that is set, naming the input and its `deprecated` message, never the value. It is not a violation: the input still loads and is still checked.
- Offer a `keySet`'s helpers: a constant-time `contains(candidate)` and a helper that tries every key with a check the caller supplies (section 4.3).
- Report `invalid_type` when a secret variable still holds an unresolved injector reference (a value starting with `vault:`, `op://` or `ref+`), because the injector did not run. The message names the variable and the reference scheme, never the value.
- Integrate with the framework around the host library: a Railtie, `ValidateOnStart` in .NET, a Next.js or NestJS adapter for T3 Env.

## 12. Conformance suite

The `conformance/` directory of docuconf-go is the shared suite. Cases are language-neutral, so every SDK runs the same ones and a disagreement between two SDKs is a bug in one of them.

- `load/*.yaml` holds the cases, written by hand. Each file declares variables and a list of cases. A case gives either `values` (typed platform values, which must pass `#Validate`) or `env` (raw strings, for input the platform would never send, such as malformed or out-of-range values), and, for `env`, either the expected typed result (`expect`) or the expected errors (`errors`, a list of variable and code).
- `cases.json` is generated from `load/` by `docuconf conformance`, and checked in. For each case it holds the full contract, unified with the meta-schema so defaults are explicit; the exact process environment the SDK sees; and either the typed value of every variable (`null` when absent) or the errors. A `values` case is rendered with `#Render` once per list and duration encoding the case leaves open, so one case tests every encoding, with `$` already reduced as Kubernetes does.
- A case may list `requires` tags: `int64` (the host holds every 64-bit integer) and `json-schema` (the SDK validates `json` values against their JSON Schema in contract-first mode). An SDK lacking a capability skips those cases and documents the gap. No other case may be skipped. A `load/*.yaml` file may also list `requires` for all its cases.
- Two more tags are **transitional**: `key-set` (the `keySet` type, section 4.3) and `deprecated` (deprecated inputs, section 4.2), so that SDKs written before these features keep a green suite while they catch up. They are not capabilities a host may lack: every SDK MUST support both, and run their cases, by `v1beta1`, when the tags are dropped from the suite. `int64` and `json-schema` stay.
- A runner skips a case only when it holds a tag the SDK lacks. A runner that does not know a tag at all MUST skip the case, not run it, so that a new transitional tag never breaks an SDK that predates it.

A conformance runner, one per SDK, runs every case in `cases.json` through the SDK's contract-first mode (section 11.2, item 11), with the case's `env` as the whole environment:

- For `expect`, loading succeeds and each variable's typed value, written as JSON, equals the expected one: durations in canonical form (section 11.2, item 3), integers exactly, floats numerically, lists as arrays.
- For `errors`, loading fails with exactly the listed variable and code pairs, in any order, and no error output contains the raw value of a secret variable.

The suite covers variables in v1. File inputs, profiles and overlays are tested by each SDK for now; cases for them are planned. Contract export is checked separately: each SDK writes a fixture declaration in its own language and vets the exported contract against the meta-schema (section 11.2, item 3).

## 13. Open questions

1. Proposals arising from [`docs/EDGE_CASES.md`](../docs/EDGE_CASES.md), each tracked in an issue:
   - **roles**, for one image running several processes ([#22](https://github.com/Docuconf/docuconf-go/issues/22));
   - **`requiredIf`**, for conditional requirements ([#23](https://github.com/Docuconf/docuconf-go/issues/23));
   - **well-known fragments**, for variables read by frameworks and libraries, and for sharing declarations between services ([#24](https://github.com/Docuconf/docuconf-go/issues/24));
   - **platform-authored contracts**, for third-party images ([#25](https://github.com/Docuconf/docuconf-go/issues/25)).

Resolved in this draft: optional variables with no default are allowed (section 4.2), and docuconf does not prescribe how systems are set up; the unknown-variable check stays strict, and inputs are removed in stages by marking them `deprecated`, which the platform sees as a warning (sections 4.2, 7 and 9); build-time variables are not covered: contracts are runtime only, and build-time variables belong to Docker and compilers (section 11.1); sharing variables between services is not a contract feature of its own (the Go library's `AddShared` no longer exists), since well-known fragments would cover it (question 1); an app does not declare injector hints, because the injector is a platform concern: platform engineers add pod annotations, webhooks or other options in their own documents (section 4.5.2); the docs model turns a `json` variable's or config file's JSON Schema into a field table, keeping the raw schema for what the table cannot express (section 14.3); a key set is a first-class type, `keySet`, which SDKs give verifier helpers and the docs give rotation steps (sections 4.3 and 6.1); generated docs come from one generator, `docuconf docs` in the CLI, through a versioned docs model that any renderer can read, and SDKs export an optional `details` beside the required `description` instead of generating docs themselves (section 14); per-item bounds for `int` lists (`itemMin`, `itemMax`, section 4.3); length limits for fixed-width hosts: `maxLength` on `url` and `json` values, and `itemMinLength`/`itemMaxLength` on `string` lists, counted in characters (section 4.3); config-file overlays, rendered from `configKey` into a file of their own rather than replacing a baked-in one (section 4.7); values and files supplied at runtime by injectors (section 4.5.1), with the pod annotations and labels that enable them (section 4.5.2); non-secret values may come from `configMapKeyRef`, the Downward API and resource fields (section 4.5); a `json` variable type exists, with schemas generated from code (sections 4.3 and 4.6).

Planned after beta, without changing anything in v1alpha1: several profiles at once through an optional `profiles.separator`, later profiles winning, while a single profile name keeps its meaning (section 4.4); profiles for file inputs, so a baked-in file can be the default for some environments; and translatable `description` and `details`. Deferred: file inputs that take a whole directory of arbitrary files ([#26](https://github.com/Docuconf/docuconf-go/issues/26)).

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
| `groups[].inputs[]` | One input. `kind` is `var` or `file`; `type` is the contract type and `typeLabel` a phrase for it ("list of integers", "key set", "YAML config file"). `required`, `secret`, `group`, `description`, `details`, `deprecated` (`message`, `replacedBy`) and, for variables, `configKey` and `examples` are copied from the contract. |
| `default`, `defaultEnv` | Variables only. The contract's default as a typed platform value, and as the process environment holds it, in the wire format: one entry, or one per item for an `indexed` list. |
| `profileSelector`, `profileDefaults` | Variables only. Whether the variable selects the profile, and its default in each profile file, by profile name. |
| `wire` | Variables only. `encoding` and `separator` for lists, key sets and durations; `text`, how the value is written in the process environment; `platform`, how it is written in a values file. |
| `rotation` | Key sets only (section 6.1): `text`, which introduces the rotation, and `steps`, the three steps in order. It is the same for every key set, so an app's `details` need not repeat it. |
| `file` | Files only. `path`, `pathEnv`, `format` (config and keystore), `reload`, `maxSize`, and `contents` and `reloadText`, what the file holds and what a change to its source does, in plain words. |
| `constraints` | Each constraint as data and as a phrase (section 14.3). |
| `fields` | A `json` variable or `config` file with a `schema`: the schema as a field table (section 14.3). The raw schema stays in the `schema` constraint. |
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
| `length` | `minLength`, `maxLength` | `string`, `text` files; `maxLength` also on `url` and `json` | `at most 120 characters (Unicode code points)` |
| `pattern` | `pattern` | `string`, `text` files | ``matches the RE2 pattern `^[a-z]+$` `` when anchored with `^` and `$`, else ``contains a match for the RE2 pattern `[a-z]` `` (section 4.3) |
| `schemes` | `schemes` | `url` | `` `https` or `http` URL `` |
| `values` | `values` | `enum` | ``one of `debug`, `info` or `warn` `` |
| `itemCount` | `minItems`, `maxItems` | `list` | `between 1 and 5 items`, `at least 1 item` |
| `itemRange` | `itemMin`, `itemMax` | `list` | `each item between 0 and 1023` |
| `itemLength` | `itemMinLength`, `itemMaxLength` | `list` | `each item between 1 and 64 characters (Unicode code points)` |
| `keyCount` | `minKeys`, `maxKeys` | `keySet` | `between 1 and 2 keys` |
| `keyLength` | `keyMinLength`, `keyMaxLength` | `keySet` | `each key between 32 and 256 characters (Unicode code points)` |
| `schema` | `schema` | `json`, `config` files | `matches the JSON Schema in the contract` (the schema is in `params`) |
| `maxSize` | `maxSize` | files | `at most 64 KiB (65536 bytes)` |
| `dnsNames` | `dnsNames` | `tls` | ``the certificate covers `a.example.com` and `b.example.com` `` |
| `keyAlgorithms` | `keyAlgorithms` | `tls` | ``key algorithm `ECDSA` or `RSA` `` |
| `minRemaining` | `minRemaining` | `tls` | `at least 720h (30 days) of validity left` |
| `requireCA` | `requireCA: true` | `tls` | ``includes `ca.crt`, and the certificate chains to it`` |
| `minCertificates` | `minCertificates` | `caBundle` | `at least 1 CA certificate` |
| `passwordVar` | `passwordVar` | `keystore` | ``opens with the password in `KS_PASSWORD` `` |

Constraints appear in this order. A pair of bounds is one constraint, phrased `between`, `at least`, `at most` or `exactly`. In the CLI the table is data: a new bound is one row.

**Field tables.** A JSON Schema is hard to read, and each renderer would show it differently, so the model also turns the `schema` of a `json` variable or a `config` file into `fields`, one row per property:

| Row field | Content |
|---|---|
| `path` | Dotted for nested objects (`database.host`), with `[]` for the items of a list (`routes[].match`; `tags[]` for the items of a list of scalars, when they say more than their type). Empty for a schema that is not an object, which is one row. Rows are in path order, properties sorted by name, each object before its properties. |
| `type` | `string`, `integer`, `number`, `boolean`, `object`, `list of strings`, `list of objects` ..., `string or null` for a list of types, or `see schema`. |
| `required` | Whether the property is in its parent's `required`. |
| `default`, `description`, `enum` | From the schema; `const` is an `enum` of one. A secret input's rows have no `default`. |
| `constraints` | `{rule, params, text}` as above, with `params` under their JSON Schema keywords: `range` (`minimum`, `maximum`), `exclusiveRange` (`exclusiveMinimum`, `exclusiveMaximum`: `above 0`, `below 1`), `length` (`minLength`, `maxLength`), `pattern`, `format` (``format `email` ``), `itemCount` (`minItems`, `maxItems`) and `uniqueItems` (`no two items equal`). |
| `schema` | Only on a `see schema` row: the raw schema of that subtree. |

A subtree that uses `anyOf`, `oneOf`, `allOf`, `not`, `$ref`, `patternProperties`, an `additionalProperties` schema (a map), tuple `items`, or any keyword the table does not show, is one row of type `see schema`, carrying its subtree; annotations (`title`, `examples`, `$comment`, `$defs`, ...) change nothing. Renderers show the table, then the raw schema of each `see schema` row. The model is still `v1alpha1`: `fields` and `rotation` are additions.

### 14.4 Text and Markdown

- `description` is plain text. Renderers escape it for Markdown and keep it on one line.
- `details` is CommonMark, used as written, with one exception: its headings are demoted to nest under the input's own heading, so they cannot break the document's outline. A level-n heading becomes level base + n, at most 6, where base is the level of the input's heading; setext headings become ATX headings; fenced and indented code is left alone.
- An example of up to 60 characters, on one line and without surrounding spaces, is shown as a code span; a longer or multi-line one goes in a fenced code block.
- Non-ASCII text is written as is. Lengths are counted in Unicode code points, as everywhere in this spec.

### 14.5 Renderers

`--format markdown` writes the reference for developers, such as `CONFIG.md`:

1. `<!-- Generated by docuconf. Do not edit. -->`, the title, where the file comes from, and a count of the inputs.
2. A table of contents, by group, with each input's description and whether it is required, secret or deprecated.
3. "Environment variables", then "Files": a heading per group (when any input has a group), and a section per input with its description, a deprecation notice with its message that links to the replacement, a table (type, required, secret, default, profile defaults, constraints, wire format, values-file form, config key, path, contents, reload, sources, boot errors), a key set's rotation steps, its field table followed by the raw JSON Schema of any `see schema` row in a collapsed block, its examples and its details.
4. "Profiles" and "Config-file overlays", when the contract has them; "Sources", what each source kind means; "Boot errors", each code's meaning and fix.

`--format agents` writes one file for AI agents, both coding agents working in the app's repository and agents that set deployment values. It can be included in an `AGENTS.md` or served as an `llms.txt`-style file:

1. **Hard rules** first: never put a secret value in code, a `.env` file, a values file, a ConfigMap or an annotation, and supply secrets only as `secretKeyRef`, secret files or `injected` (the secret inputs are listed); use each input's wire format for raw environment values and typed values in values files; validate with `docuconf vet` (platform values) or `docuconf check` (a running environment) before proposing a change; do not invent inputs that are not in the contract; set the required inputs that have no default; do not add or use deprecated inputs, in code or in values (the deprecated inputs are listed).
2. **Using this config in code**, in terms that hold for every SDK: the declaration is the source of truth, values are read through the SDK's typed configuration, and `config key` says where a setting lives in the app's own configuration.
3. **Setting values**: one block per input, where every fact is a `key: value` line (kind, type, group, required, secret, deprecated, path, path variable, format, contents, reload, default, default in the environment, profile defaults, constraints, wire format, values-file form, rotation, config key, examples, one `field` line per field table row, with the raw schema on `see schema` rows, allowed sources, boot errors), then the description and the details. Then the source kinds, profiles and overlays.
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
