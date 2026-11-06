# docuconf for infrastructure as code: Terraform, OpenTofu and Pulumi

Status: **Draft, v1alpha1.** Proposed; nothing here is implemented yet. It extends [SPEC.md](SPEC.md) and changes nothing in it: contracts, SDKs and the values and files documents stay as they are.

## 1. Purpose

A platform that deploys with Terraform, OpenTofu or Pulumi sets an app's environment inside resources: a `kubernetes_deployment_v1` container's `env`, an ECS task definition's `environment` and `secrets`, a Cloud Run service's `env`. The value of each variable comes from somewhere else in the same program: a literal, a database's address, a Secrets Manager ARN, a `random_password`. Today nothing connects the two. A required variable can be missing, a port can be the string `"eighty"`, a password can be written into a pod spec in plaintext, and the first anyone hears of it is a crash at boot.

docuconf closes that gap the way it does for Helm and Crossplane. From the app's contract it **generates a module** that:

1. takes **one argument per contract input**, each typed and constrained like the input, and each saying **how** it is set (a literal, a Secret reference, a resource field, an injector);
2. renders the inputs into the exact blocks the workload resource takes (its `env`, `volumes` and mounts), in the wire encodings of [SPEC.md section 5](SPEC.md#5-wire-encoding-and-parsing);
3. records a **binding** for each input: which source sets it, and which resource the value comes from.

A linter then reads the planned program and checks, input by input, that what every resource will set is what the contract accepts: before apply where the value is known, at apply before the workload changes where it is not, and at boot for what only the app can see.

### 1.1 Goals

- Every contract input has exactly one place in the program where it is set, and the linter can name it: `DATABASE_URL ← aws_secretsmanager_secret.orders_db (secret reference)`.
- A missing required input, a typo, a wrong type or an out-of-range literal fails at `terraform validate` or `terraform plan`, not at boot.
- A value only known after apply (an endpoint, a generated name) is checked before the workload that uses it is created or changed.
- A secret input can only be wired to the target's secret mechanism. A secret value written into a plain env entry is an error, even when Terraform or Pulumi marks it sensitive.
- The workload's env holds nothing the module did not render, so a hand-added `PORTT` or a second `LOG_LEVEL` is caught.
- The generated code is ordinary HCL or ordinary Pulumi code, readable and reviewable, with no provider or plugin to install.

### 1.2 Non-goals

- Provisioning what the app depends on (databases, queues, buckets), as in [SPEC.md section 1.2](SPEC.md#12-non-goals). The program does that; docuconf types how the results reach the app.
- Generating the workload resource itself. Platforms shape Deployments and task definitions in many ways; the module renders the parts the contract governs, and the platform's own resource includes them.
- Reading secret values. Bindings name secrets; they never hold them, so neither the state nor the plan the linter reads contains secret material that docuconf put there.
- A Terraform provider. A provider would add a binary to install and version; a generated module and an offline linter do not.

## 2. Terms

| Term | Meaning |
|---|---|
| **Module** | The code `docuconf terraform` or `docuconf pulumi` generates from one contract: a Terraform module, or a Pulumi component in one language. |
| **Input argument** | The module's argument for one contract input, named exactly as the input (`DATABASE_URL`, `serving-tls`). |
| **Source** | How an input argument is set: one of the value sources of [SPEC.md section 4.5](SPEC.md#45-value-sources) or file sources of [section 4.6.1](SPEC.md#461-file-sources), mapped to the target (section 5). |
| **Origin** | What the program feeds into a source: a literal, or a resource or data source attribute, such as `aws_db_instance.orders.address`. |
| **Binding** | One input's source and origin, recorded by the module and reconstructed by the linter. |
| **Target** | The kind of workload the module renders for: `kubernetes`, `ecs`, `cloudrun`, `containerapps` or `lambda`. |
| **Workload** | A resource that runs the app's container: a `kubernetes_deployment_v1`, an `aws_ecs_task_definition`, and so on (section 5). |
| **Bound container** | A container in a workload whose env comes from the module's rendered output. |
| **Known, unknown** | Whether a value is known at plan time. A literal is known; an attribute computed on apply, such as an RDS endpoint for a new instance, is unknown. |

## 3. Overview

```
 app repo                           platform repo (Terraform / OpenTofu / Pulumi)
 ────────                           ─────────────────────────────────────────────
 contract.cue ──docuconf terraform──▶ modules/orders-config/   (generated, committed)
                                         │
     origins ── aws_db_instance, aws_secretsmanager_secret, literals ──┐
                                         ▼                             │
                         module "orders_config" { DATABASE_URL = {secret = …}, PORT = {value = 8080} }
                                         │ outputs: kubernetes_env, kubernetes_volumes, …, bindings
                                         ▼
                         kubernetes_deployment_v1.orders  (container env = module.orders_config.kubernetes_env)
                                         │
 terraform plan ──▶ plan.json ──▶ docuconf lint terraform ──▶ apply ──▶ SDK boot check
```

Checks run in five places, each catching what the one before cannot:

| When | What runs | Catches |
|---|---|---|
| `terraform validate` | The module's argument types | A missing required input, an unknown input (a typo), a literal given for a secret, a value of the wrong type. |
| `terraform plan` | The module's `validation` blocks, on known values | Ranges, patterns, enum values, URL schemes, lengths, list bounds. |
| `docuconf lint terraform`, on the plan | The binding rules of section 7, across every resource | Env the module did not render, a secret routed into a plain value, a container not bound to the module, a source the target cannot express, a contract that does not match the image, plus everything above, in one report. |
| `terraform apply` | The module's output `precondition`s | The constraints of values that were unknown at plan, evaluated before any workload that uses them is created or changed. |
| Boot | The SDK ([SPEC.md section 11](SPEC.md#11-sdk-requirements)) | Secret contents, file contents, certificate expiry, values set outside the program. |

Pulumi has the same four steps (section 6): argument types at compile time or construction, validation of known values in preview, a policy pack that checks every resource, and validation in `apply` callbacks before dependent resources are created.

## 4. The Terraform module

### 4.1 The command

```sh
docuconf terraform -contract contract.cue -target kubernetes -out modules/orders-config
docuconf terraform -contract contract.cue -target kubernetes -out modules/orders-config -check
```

- `-contract` is the contract file, or an OCI reference to one (`oci://registry/orders@sha256:…`, [SPEC.md section 8](SPEC.md#8-distribution)), in which case the image digest it belongs to is recorded (section 4.7).
- `-target` names the module's target (section 5): `kubernetes` (the default), `ecs`, `cloudrun` or `containerapps`. A module renders for one target, because a target decides how an input can be set, and a secret in particular is a different kind of reference on each (a Kubernetes Secret's name and key, a Secrets Manager ARN, a Secret Manager secret and version, a Key Vault secret ID). A platform that deploys the app to two targets generates a module for each.
- `-out` is the module directory. The command writes only the files of section 4.2, each starting with `# Code generated by docuconf. DO NOT EDIT.`, and removes generated files it no longer writes.
- `-check` writes nothing and exits 1 with a diff when the directory is not what the command would write, so CI can keep a committed module in step with the contract, as `docuconf docs --check` does for docs.

The module is plain HCL and runs on Terraform 1.5 or later and OpenTofu 1.6 or later. It declares no provider and creates no resources: the platform declares the ConfigMaps the module renders (section 4.5), so a module never needs the `kubernetes` provider installed.

### 4.2 Files

| File | Holds |
|---|---|
| `versions.tf` | `required_version`: Terraform 1.5 or later. |
| `variables.tf` | One input argument per contract input and overlay value (section 4.3), plus `docuconf_options` (below). |
| `render.tf` | `locals` that render each input in its wire encoding (section 4.4). |
| `outputs.tf` | Per target, the blocks the workload takes; `bindings`; `contract`. |
| `docuconf.json` | The contract as JSON, its digest, the generator version and the image digest if known. The linter reads it (section 7). |
| `README.md` | The input table, generated with the docs model ([SPEC.md section 14](SPEC.md#14-generated-docs)), and a usage example for each target. |

`docuconf_options` is the one argument that is not a contract input. All of its fields are optional:

| Field | Default | Meaning |
|---|---|---|
| `pod_annotations`, `pod_labels` | `{}` | Pod metadata shared by every injector, the values document's `podAnnotations` and `podLabels` ([SPEC.md section 4.5.2](SPEC.md#452-enabling-the-injector)). They cannot use placeholders. |
| `environment` | `null` | Selects one of the compiled policies (section 4.6). |
| `unmanaged_env` | `[]` | Env names the platform sets for itself on a bound container, which the linter ignores (rule `undeclared_input`). |
| `allow_env_from` | `false` | Downgrades `env_from_unchecked` to a warning. |

### 4.3 Input arguments

The module has one variable per contract input, named exactly as the input: `vars` keys (`DATABASE_URL`) and `files` keys (`serving-tls`) are both valid Terraform identifiers. A name the platform misspells is then `Unsupported argument` at `terraform validate`, which is the unknown-variable check of [SPEC.md section 7](SPEC.md#7-platform-validation) for free.

Each variable is an **object with one attribute per source** the input allows, all optional, and a validation that exactly one is set. Which attributes exist depends on the input, so the type itself encodes the rules of [SPEC.md sections 4.5 and 6](SPEC.md#45-value-sources):

```hcl
# Code generated by docuconf. DO NOT EDIT.
variable "PORT" {
  description = "HTTP listen port"
  type = object({
    value          = optional(number)                       # literal
    config_map     = optional(object({ name = string, key = string }))
    injected       = optional(object({ provider = string, ref = optional(string), pod_annotations = optional(map(string), {}), pod_labels = optional(map(string), {}) }))
  })
  default  = null           # optional input; a required input has no default and nullable = false
  nullable = true

  validation {
    condition     = var.PORT == null ? true : length([for k, s in var.PORT : k if s != null]) == 1
    error_message = "PORT: set exactly one of value, config_map or injected"
  }
  validation {
    condition     = var.PORT == null ? true : (var.PORT.value == null ? true : floor(var.PORT.value) == var.PORT.value)
    error_message = "PORT: expected an integer, got ${try(format("%v", var.PORT.value), "")}"
  }
  validation {
    condition     = var.PORT == null ? true : (var.PORT.value == null ? true : var.PORT.value >= 1)
    error_message = "PORT: ${try(format("%v", var.PORT.value), "")} is below min 1"
  }
  # …and max, and the injected source's checks
}

variable "DATABASE_URL" {
  description = "Primary Postgres connection string"
  type = object({
    secret   = optional(object({ name = string, key = string }))  # the target's secret reference (section 5)
    injected = optional(object({ provider = string, ref = optional(string), pod_annotations = optional(map(string), {}), pod_labels = optional(map(string), {}) }))
  })
  nullable = false   # required: no default
  # …exactly-one validation as above
}
```

Rules:

- **Types.** The `value` attribute has the platform type of [SPEC.md section 4.3](SPEC.md#43-types): `string` for `string`, `url`, `enum` and `duration` (Go syntax), `number` for `int` and `float`, `bool`, `list(string)` or `list(number)` for a `list`, and `any` for a `json` value. An `int` adds a whole-number check.
- **Secrets have no `value`.** A `secret: true` input, and every `keySet`, has only `secret` and `injected`, so `DATABASE_URL = { value = "postgres://…" }` fails at validate. Terraform drops an attribute an object type does not declare rather than rejecting it, so the argument arrives with nothing set, and the exactly-one validation says why: `DATABASE_URL: set exactly one of secret or injected (a secret is never a literal value; Terraform ignores a value attribute here)`. A secret file input has no `inline` and no `config_map`.
- **Required.** A required input has `nullable = false` and no default. A required input satisfied by a profile ([SPEC.md section 4.4](SPEC.md#44-config-files-and-profiles)) is optional here, since the module cannot know which profile the platform selects until the selector's argument is known; the linter checks it (section 7, rule `missing_required`).
- **Constraints** become `validation` blocks on the `value` attribute, one per constraint, with the error message `docuconf vet` prints for the same mistake ([SPEC.md section 7](SPEC.md#7-platform-validation)): ranges, `values`, `pattern`, `schemes`, lengths, list bounds and item bounds. They run at plan on known values. A duration's bounds are compared in nanoseconds, which Terraform's arbitrary-precision numbers hold exactly. A constraint HCL cannot express exactly (a `json` value's `schema`) is left to the linter and to boot, and the generated README says so.
- **Null-safe messages.** Terraform evaluates a validation's `error_message` even when its condition holds, so every interpolation in a message is wrapped in `try(format("%v", …), "")`, and every condition is guarded with `var.X == null ? true : …` rather than `||`, since not every supported version short-circuits `||`.
- **Regular expressions.** Terraform's `regex` and `can(regex(...))` use Go's RE2, the dialect of [SPEC.md section 4.3](SPEC.md#43-types), so a `pattern` is copied unchanged and matches anywhere, as the spec requires.
- **Lengths count code points.** Terraform's `length()` counts grapheme clusters, not code points, so `"é"` written as `e` plus a combining accent is 1 to Terraform and 2 to the spec. The generator MUST count with `length(regexall("(?s).", v))`, which counts code points, never with `length(v)`.
- **Origins are free.** The argument says how an input is set, not where the value comes from: `DATABASE_URL = { secret = { name = kubernetes_secret_v1.db.metadata[0].name, key = "url" } }` and `PORT = { value = var.port }` are both fine. The linter reconstructs the origin from the plan (section 7.1).
- **Deprecated inputs** keep their argument. Setting one is a plan-time warning: the generator emits a `check` block whose assertion fails, with the input's `deprecated.message`, when the argument is set. A `check` block reports a warning and never stops the plan, which is the warning of [SPEC.md section 7](SPEC.md#7-platform-validation).
- **File inputs.** A file input's argument has one attribute per source its type allows: `inline` (text, or for a `config` file an object or list), `config_map`, `secret`, `certificate` (`tls` only), `csi`, `image` and `injected`. A secret file has no `inline`, `config_map` or `image`; a `binary` file has no `inline`. The validations are `#CheckFile`'s, worded as `docuconf vet` words them: a source's key, inline size in bytes, well-formed JSON and YAML, text bounds, a CA bundle's certificate count, and a cert-manager certificate's DNS names, key algorithm and `renewBefore`. Inline TOML must be text, since Terraform has no TOML encoder.
- **Overlays.** An overlay's values ([SPEC.md section 4.7](SPEC.md#47-config-file-overlays)) are arguments too, one per overlay, prefixed by it: `overlay_platform = { CATALOG__PAGESIZE = 50 }`. The argument is typed `any`, not an object type, because Terraform drops an attribute an object type does not declare: a variable the overlay cannot carry (secret, without a `configKey`, the profile selector, or not in the contract) would vanish instead of failing. Validations name such a variable with vet's reason, and check each value's type and constraints as for an env argument, with ` (in overlay NAME)` after the message. A required variable an overlay can carry is optional as an argument; a precondition checks that it is set in one place. Supplying a variable both as an env argument and in an overlay, or in two overlays, fails a precondition, since one would silently win.

### 4.4 Rendering

`render.tf` turns the arguments into env entries, volumes and mounts, with exactly the results of the CUE `#Render` ([SPEC.md section 7](SPEC.md#7-platform-validation)) and the Helm library chart:

- the wire encodings of [SPEC.md section 5](SPEC.md#5-wire-encoding-and-parsing): `csv`, `json` and `indexed` lists (an `indexed` list becomes one entry per item), the duration encodings, `int` and `float` forms, compact JSON;
- `$` escaped as `$$` for the `kubernetes` target, where the kubelet expands `$(VAR)`, and left alone for targets that do not;
- one entry per `pathEnv`, file inputs projected with `items` (never `subPath`), content-hashed ConfigMap names for inline content and `restart` overlays (a `watch` overlay keeps a stable name and a mutable ConfigMap), the pod annotations and labels of injected sources with their placeholders expanded ([SPEC.md section 4.5.2](SPEC.md#452-enabling-the-injector)), and the restart triggers of [section 4.6.2](SPEC.md#462-rotation).

Structured content is compared by meaning, not by bytes. Terraform cannot reproduce CUE's YAML and TOML encoders, so the module writes inline YAML and YAML overlays with `yamlencode`, JSON compactly (an overlay too, which `#Render` indents), and a TOML overlay with one dotted key per value. The data is the same; the text, and so the content hash in a ConfigMap's name, can differ from what `#Render` and the Helm library chart write. The hash is always of the module's own content, so a change still renames the ConfigMap and rolls the pods. A TOML overlay cannot hold a `json` value with objects.

Two more differences from `#Render` are allowed, because Terraform's values do not keep them: a `float` is written in Terraform's form (`1` for a value the values document writes `1.0`, `0.0000001` for `1E-7`), and a `json` object's keys are written in sorted order. Both parse to the same value, and the lengths a `maxLength` sees can differ only by the float's spelling.

Every output that carries rendered values has `precondition`s that repeat each input's constraint checks. Terraform evaluates a precondition at plan when its values are known, and otherwise during apply, before anything that depends on the output. A workload that takes its env from the output therefore cannot be created or changed with a value that breaks the contract, even when the value came from an attribute computed earlier in the same apply.

### 4.5 Outputs

For the `kubernetes` target:

| Output | Shape | Goes into |
|---|---|---|
| `kubernetes_env` | list of `{name, value, value_from}`, exactly one of `value` and `value_from` non-null (one object type, so `for_each` accepts it) | a `dynamic "env"` block of the container |
| `kubernetes_volumes`, `kubernetes_volume_mounts` | lists in the provider's block shapes: each volume `{name, config_map, secret, csi, image}` with one kind set, `default_mode` as the provider's octal string | `dynamic "volume"` in the pod spec, `dynamic "volume_mount"` in the container |
| `kubernetes_config_maps` | map of name to `{data, immutable}`, for inline content and overlays | `kubernetes_config_map_v1` resources the platform declares with `for_each` |
| `pod_annotations`, `pod_labels` | maps | the pod template's metadata, never the workload's ([SPEC.md section 4.5.2](SPEC.md#452-enabling-the-injector)) |
| `restart_triggers` | list of `{kind, name}`: the ConfigMaps, Secrets and SecretProviderClasses a `restart` file input reads, as `#Render` lists them | a pod template annotation hashing their content, or the platform's reloader |
| `helm_values` (not yet generated) | the `docuconf:` values of the Helm library chart | a `helm_release` that uses the chart instead of `kubernetes_deployment_v1` |

The other targets have their own outputs in section 5. Every module also has:

- `bindings`: for each input, `{source = "secret" | "value" | …, set = true | false}`. The linter uses it to find the module in the plan and to tell an unset optional input from one the module failed to render.
- `contract`: the contract's name, digest and, when known, the image digest.

A platform uses the outputs like this:

```hcl
module "orders_config" {
  source       = "./modules/orders-config"
  DATABASE_URL = { secret = { name = kubernetes_secret_v1.orders_db.metadata[0].name, key = "url" } }
  PORT         = { value = 8080 }
  LOG_LEVEL    = { value = var.environment == "prod" ? "warn" : "debug" }
}

resource "kubernetes_deployment_v1" "orders" {
  # …
  spec {
    template {
      metadata { annotations = module.orders_config.pod_annotations }
      spec {
        container {
          name  = "orders"
          image = "ghcr.io/acme/orders@${var.orders_digest}"
          dynamic "env" {
            for_each = module.orders_config.kubernetes_env
            content {
              name  = env.value.name
              value = env.value.value
              dynamic "value_from" {
                for_each = env.value.value_from == null ? [] : [env.value.value_from]
                content { /* secret_key_ref, config_map_key_ref, field_ref, resource_field_ref, generated */ }
              }
            }
          }
          dynamic "volume_mount" {
            for_each = module.orders_config.kubernetes_volume_mounts
            content { name = volume_mount.value.name, mount_path = volume_mount.value.mount_path, read_only = true }
          }
        }
        dynamic "volume" { for_each = module.orders_config.kubernetes_volumes /* … */ }
      }
    }
  }
}
```

The generated README carries this example, complete, for each target the module was generated for.

### 4.6 Compiled policy

An environment policy (section 7.3) can also run inside the module, so that a constraint on a value or reference that is unknown at plan is checked at apply, before the workload changes, like the contract's own constraints (section 4.4).

```sh
docuconf terraform -contract contract.cue -target ecs -out modules/orders-config \
  -policy prod=policies/prod.cue -policy staging=policies/staging.cue
```

- Each `-policy` is `name=file`. One module serves every environment, so it holds every named policy, and the platform selects one with `docuconf_options.environment` (`"prod"`). An environment the module does not know is a validation error; leaving it `null` applies no compiled policy.
- The generator compiles the parts of a policy HCL can express exactly: concrete values; disjunctions of concrete values; `=~` and `!~` with an RE2 pattern; `<`, `<=`, `>`, `>=` on numbers; `strings.HasPrefix` and `strings.HasSuffix`; and a forbidden field (`field?: _|_`). It compiles them for inputs named in the policy and for every entry of `references` and `secretReferences`. Each becomes a `precondition` on the rendered outputs, with the message `policy_mismatch` prints (section 7.2).
- For a reference, the module derives the fields of section 7.3 from the reference with RE2, as the linter does: `account`, `region` and `name` from a Secrets Manager or Parameter Store ARN, `project` and `version` from a Secret Manager name, `vault` and `name` from a Key Vault secret ID. A field the module cannot derive because the reference is itself unknown is derived during apply, once it is known.
- A constraint the generator cannot compile (a comprehension, a reference to another field, a definition, a non-RE2 pattern) stays with the linter alone. The generator lists those constraints, and `docuconf.json` records them, so the linter can say which constraints were checked at apply and which were not (section 7.2, `policy_deferred`).
- `docuconf.json` records each policy's digest. `-check` and `stale_module` (section 7.2) treat a changed policy file like a changed contract.

The linter still applies the whole policy at plan. Compiling it into the module adds the apply-time check; it does not replace the plan-time one.

### 4.7 Contract and image

A contract describes one image ([SPEC.md section 8](SPEC.md#8-distribution)). When the module is generated from an OCI reference, `docuconf.json` and the `contract` output record the image digest, and the linter checks that every bound container runs that digest (rule `contract_image_mismatch`). A module generated from a local file records no digest, and the linter says so once (`image_unverified`, a notice).

## 5. Targets

A target maps the contract's sources onto what one kind of workload can express. A source a target cannot express is missing from that target's argument types, so using it is a validate error, and the linter reports `unsupported_source` with the target's name.

| | `kubernetes` | `ecs` | `cloudrun` | `containerapps` | `lambda` |
|---|---|---|---|---|---|
| Workloads | `kubernetes_deployment_v1`, `_stateful_set_v1`, `_daemon_set_v1`, `_job_v1`, `_cron_job_v1`; `helm_release` with the docuconf chart | `aws_ecs_task_definition` (`container_definitions` JSON) | `google_cloud_run_v2_service`, `_job` | `azurerm_container_app` | `aws_lambda_function` |
| Literal | `value` | `environment` | `env.value` | `env.value` | `environment.variables` |
| Secret reference | `secret_key_ref` | `secrets[].valueFrom`: a Secrets Manager ARN with an optional JSON key, or an SSM parameter ARN | `value_source.secret_key_ref`: a Secret Manager secret and version | `secret_name`, naming one of the app's `secret` blocks | none: secret inputs must be `injected` (a Lambda extension) |
| `config_map` | `config_map_key_ref` | none | none | none | none |
| `field`, `resource_field` | Downward API | none | none | none | none |
| `injected` | as in SPEC 4.5.1 | as in SPEC 4.5.1 (no pod annotations) | as in SPEC 4.5.1 (no pod annotations) | as in SPEC 4.5.1 (no pod annotations) | as in SPEC 4.5.1 (no pod annotations) |
| File inputs | every source | none | `secret` (Secret Manager volumes, one file each: no `tls`) | `secret` (Key Vault secrets in a Secret volume, one file each: no `tls`) | none |
| `$` escaping | yes | no | no | yes | no |
| Phase | 1 (built) | 2 (built) | 2 (built) | 3 (built) | 3 |

A file input on a target that cannot mount it is a generation error, not a runtime surprise: `docuconf terraform -target ecs` on a contract with a required file input fails, naming the input. An optional one is left out with a warning.

Wire encodings are the same on every target; only `$` escaping differs. The kubelet expands `$(VAR)` in an env value, and so does Container Apps, which runs containers on Kubernetes, so those two targets double `$`; the others pass values through.

### 5.1 `ecs`

The arguments have `value` and `injected` for a variable, and `secret` and `injected` for a secret:

```hcl
DATABASE_URL = { secret = { value_from = aws_secretsmanager_secret.db.arn } }
API_TOKEN    = { secret = { value_from = aws_secretsmanager_secret.app.arn, json_key = "token" } }
LOG_LEVEL    = { value = "warn" }
```

- `secret.value_from` is what ECS takes in `secrets[].valueFrom`: a Secrets Manager ARN, a Parameter Store ARN, or the name of either in the task's own account and region. `json_key`, `version_stage` and `version_id` select from a Secrets Manager secret, so they need its full ARN, and `version_stage` and `version_id` exclude each other. The module renders them as ECS writes them, `arn:json-key:version-stage:version-id`.
- `injected` has `provider` and `ref`, and no pod metadata: an ECS injector is a wrapper process or a sidecar, not a webhook.
- An `indexed` key set is one variable per key, which one ECS secret cannot carry, so it has only `injected`.
- File inputs and overlays are left out, with a warning for each, because ECS mounts no files; a required file input fails generation (section 5).

The outputs are `ecs_environment`, a list of `{name, value}`, and `ecs_secrets`, a list of `{name, valueFrom}`, in the shapes `container_definitions` takes, so they go into `jsonencode` as they are. Values are not `$`-escaped. `ecs_environment` carries the arguments' checks again as preconditions, as `kubernetes_env` does. The task execution role must be allowed to read each secret; the module does not grant it.

`docuconf lint terraform` reads an `aws_ecs_task_definition`'s planned `container_definitions` (section 7.1). When they are unknown at plan, because they include an ARN created in the same apply, it checks the module's constant arguments instead and says so (`deferred`).

### 5.2 `cloudrun`

The arguments have `value` and `injected` for a variable, `secret` and `injected` for a secret, and `secret` and `injected` for a file input:

```hcl
DATABASE_URL = { secret = { secret = "projects/acme-prod/secrets/orders-db", version = "7" } }
LOG_LEVEL    = { value = "warn" }
discounts    = { secret = { secret = google_secret_manager_secret.discounts.secret_id } }
```

- `secret.secret` is a Secret Manager secret ID, in the service's own project, or `projects/PROJECT/secrets/SECRET`; `secret.version` is `latest` (the default) or a version number. Pinning a version makes a new value a new revision, as a content-hashed ConfigMap does on Kubernetes.
- A file input is a secret volume holding one file: the secret's version, mounted at the input's path. A `tls` input is a directory of several files, which one secret cannot hold, so it is left out with a warning, and a required one fails generation. Overlays need a ConfigMap and are left out with a warning.
- `injected` has `provider` and `ref`, and no pod metadata. An `indexed` key set has only `injected`.

The outputs are `cloudrun_env`, a list of `{name, value, value_from}` with `value_from` a `{secret_key_ref = {secret, version}}` or null, for a `dynamic "env"` block; `cloudrun_volumes`, each `{name, secret = {secret, default_mode, items}}`; and `cloudrun_volume_mounts`, each `{name, mount_path}`. Values are not `$`-escaped. The service account the revision runs as must be allowed to access each secret.

`docuconf lint terraform` reads a `google_cloud_run_v2_service`'s or `_job`'s revision template: its containers' env and volume mounts and its volumes. As on Kubernetes, `env` usually comes from a `dynamic` block, which the plan's configuration omits, so a reference elsewhere binds the service to its module (`labels = { app = module.orders_config.contract.name }`). A secret is `gcpSecretManager` to the policy; a bare secret ID's project is unknown at plan (`policy_deferred`).

### 5.3 `containerapps`

The arguments have `value` and `injected` for a variable, `secret` and `injected` for a secret, and `secret` and `injected` for a file input:

```hcl
DATABASE_URL = { secret = { key_vault_secret_id = azurerm_key_vault_secret.orders_db.versionless_id } }
WEBHOOK_KEYS = { secret = { key_vault_secret_id = "https://orders-kv.vault.azure.net/secrets/webhook-keys", identity = azurerm_user_assigned_identity.orders.id } }
LOG_LEVEL    = { value = "warn" }
discounts    = { secret = { key_vault_secret_id = azurerm_key_vault_secret.discounts.versionless_id } }
```

- `secret.key_vault_secret_id` is a Key Vault secret ID, with or without a version, in any Azure cloud; `secret.identity` is the managed identity that reads it, `System` (the default) or a user-assigned identity's resource ID. A versionless ID follows the secret's latest version; a versioned one makes a new value a new revision.
- Each secret input becomes one of the app's secrets, named by the module: `dc-` and the input's name in lower case, with runs of other characters as `-` (`DATABASE_URL` is `dc-database-url`), and `dc-file-` for a file input. Two inputs whose names fold to the same secret fail generation. The variable's env entry names the secret (`secret_name`).
- A file input is one secret in the module's Secret volume, `dc-secrets`, mounted at the input's path with `sub_path` naming the secret: a Secret volume holds every secret of the app, one file each, named after the secret, and the provider cannot select some of them. Container Apps sets the file's permissions, so the contract's `0400` for a secret file is not applied. A `tls` input is a directory of several files and is left out with a warning, a required one failing generation; overlays need a ConfigMap and are left out with a warning.
- `injected` has `provider` and `ref`, and no pod metadata. An `indexed` key set has only `injected`.

The outputs are `containerapp_secrets`, each `{name, key_vault_secret_id, identity}`, for a `dynamic "secret"` block on the app; `containerapp_env`, each `{name, value, secret_name}` with one of `value` and `secret_name` null, for a `dynamic "env"` block; `containerapp_volumes`, the one `{name, storage_type = "Secret"}` when a file input is set; and `containerapp_volume_mounts`, each `{name, path, sub_path}`. Values are `$`-escaped (section 5). The identity must be able to read each secret (the Key Vault Secrets User role); a system-assigned identity exists only once the app does, so a user-assigned one granted in advance avoids a failed first revision.

`docuconf lint terraform` reads an `azurerm_container_app`'s template: its containers' env and volume mounts, its volumes, and the app's `secret` blocks, which each env entry's `secret_name` resolves to. As on Cloud Run, a reference outside the `dynamic` blocks binds the app to its module (`tags = { app = module.orders_config.contract.name }`). A Key Vault secret is `azureKeyVault` to the policy, and a secret the app holds itself (a `value`, which the linter never reads) is `containerAppSecret`. A file input is checked for a mount at its path, from `dc-secrets`, with its secret as `sub_path` (`mount_mismatch`).

## 6. Pulumi

### 6.1 The command

```sh
docuconf pulumi -contract contract.cue -lang typescript -target kubernetes -out sdk/orders-config
docuconf pulumi policy -contract contract.cue -target kubernetes -out policy/orders-config
```

```sh
pulumi preview --json > preview.json
docuconf lint pulumi preview.json -policy prod.cue
```

The first generates a **component** in one language (`typescript`, `python`, `go`, `csharp` or `java`): a local package the program imports, with no provider plugin. The second generates a **CrossGuard policy pack** (TypeScript, which Pulumi runs against programs in every language). Both take `-check` as in section 4.1. The third applies an environment policy (section 7.3) to a preview: the policy pack checks the contract's rules, but does not evaluate CUE, so environment policy runs in `docuconf lint pulumi`, which reads the resource inputs of `pulumi preview --json` and applies sections 7.2 and 7.3 as for Terraform.

Implemented so far: the TypeScript, Go and Python components for the kubernetes, ecs, cloudrun and containerapps targets (`-lang go` takes `-package`, by default the contract's name without separators, then `config`; a Python component is imported by its `-out` directory's name), the policy pack for those targets (section 6.3), and `docuconf lint pulumi` (section 6.4). C# and Java are not.

### 6.2 The component

The component has the arguments and outputs of the Terraform module, in the language's own types. Each argument is written in the vocabulary of the values and files documents ([SPEC.md section 6](SPEC.md)), so a source reads the same in `values.yaml`, Helm, Terraform and Pulumi: a literal is `{ value }` (a `json` input's object would otherwise be ambiguous), and every other source is as the documents write it: `secretKeyRef`, `configMapKeyRef`, `fieldRef`, `resourceFieldRef` and `injected` for a variable; `inline`, `configMap`, `secret`, `certificate`, `csi`, `image` and `injected` for a file input. The shared pod metadata is `podMetadata: { annotations, labels }`, and overlay values are `overlayValues`, by overlay; neither name can be an input's.

```ts
// Code generated by docuconf. DO NOT EDIT.
export interface OrdersApiConfigArgs {
  /** Postgres connection string for the orders database (required, secret, url) */
  DATABASE_URL: SecretKeyRef | Injected;            // required, secret: no value accepted
  /** HTTP listen port (int) */
  PORT?: Value<number> | ConfigMapKeyRef | ResourceFieldRef | Injected;
  LOG_LEVEL?: Value<"debug" | "info" | "warn" | "error"> | ConfigMapKeyRef | Injected;
  discounts?: InlineFile<string | JsonObject | Json[]> | ConfigMapFile | SecretFile | CsiFile | ImageFile | InjectedFile;
  podMetadata?: pulumi.Input<PodMetadata>;
}

export class OrdersApiConfig extends pulumi.ComponentResource {
  readonly kubernetesEnv: pulumi.Output<EnvVar[]>;
  readonly kubernetesVolumes: pulumi.Output<Volume[]>;
  readonly kubernetesVolumeMounts: pulumi.Output<VolumeMount[]>;
  readonly kubernetesConfigMaps: ConfigMapSpec[];     // known at construction
  readonly restartTriggers: pulumi.Output<RestartTrigger[]>;
  readonly podAnnotations: pulumi.Output<Record<string, string>>;
  readonly podLabels: pulumi.Output<Record<string, string>>;
  readonly bindings: Bindings;
  readonly checked: pulumi.Output<boolean>;
  constructor(name: string, args: OrdersApiConfigArgs, opts?: pulumi.ComponentResourceOptions) { /* … */ }
}
```

The outputs are in the shapes of the Kubernetes API, so they go straight into a container's `env`, `volumeMounts` and the pod's `volumes`. The generated package is the typed component (`index.ts`), the contract (`contract.ts`), and a runtime every component shares (`runtime.ts`), which renders exactly as `#Render` does: the same env entries and `$` escaping, volumes, mounts, ConfigMap content and content hashes, restart triggers and pod metadata.

Rules:

- **Type-level checks** where the language has them: a secret input's type has no `value` variant, an `enum` is a union of its values, a required input is not optional. TypeScript, C# and Java catch these at compile time. Go has no unions or required fields: each source is a constructor (`String`, `Int`, `SecretKeyRef`, `ConfigMapKeyRef`, `Injected`, `Inline`, `Certificate` and so on), and each input's field is an interface only its allowed sources implement, so a secret's `SecretVar` takes no literal at compile time, while a required input and an enum's values are checked at construction. Python's arguments are `TypedDict`s in the sources' vocabulary (`{"secretKeyRef": {...}}`), so a type checker (mypy, pyright) refuses a value for a secret or an enum's unknown value, and the component checks them again when it is constructed. The runtime checks them again in every language, for programs that bypass the types.
- **Value checks** use the SDK of the component's language (`@docuconf/core` for TypeScript, the Go SDK for Go, whose messages read as vet's, and the docuconf Python SDK's contract-first mode for Python, in pydantic's words): each literal is rendered in its wire encoding and checked by the engine the app runs at boot, so a value fails the deployment exactly when it would fail the app's boot, with the message the app would print, such as `WORKER_COUNT: must be at most 64 (got 100) (out_of_range)`. Inline file content is checked the same way, so a malformed YAML file or an unparsable certificate fails too. What the SDK cannot see in the rendered text is checked by the runtime, in `docuconf vet`'s words: the sources and their shapes, a certificate's DNS names, key algorithm and `renewBefore`, pod metadata, precision a duration's encoding drops, and the rules of overlays. The policy pack and `docuconf lint pulumi` keep vet's wording, since they run vet's engine.
- **When.** Arguments the program writes as literals are checked when the component is constructed, together, and a failure throws a `ConfigViolation` listing every problem: `pulumi preview` fails. A value that is an `Output` is checked when it resolves; a known one fails `pulumi preview`, an unknown one fails `pulumi up` when it resolves, and since every rendered output depends on it, no resource that uses the outputs is created or changed. The `checked` output resolves once every check has passed, and is registered, so a late failure fails the deployment even if nothing uses the outputs.
- **Literals stay literals.** Inline file content and overlay values are written into the platform's code, so the component takes them as values, never `Output`s. That makes `kubernetesConfigMaps` known at construction, and the program can declare one ConfigMap for each in a plain loop.
- **Pulumi secrets are not secret references.** A value wrapped in `pulumi.secret()` is encrypted in the state, but rendered as a plain env value it is plaintext in the pod spec or task definition. The component accepts a secret input only as the target's secret reference or as `injected`, never as a value, secret or not.
- **On ecs and cloudrun**, a component carries what the Terraform module for the target does (sections 5.1 and 5.2), with the same sources and the same checks of their forms: a variable is `{ value }`, `{ secret }` or `{ injected }` with `ref`. On ecs, `secret` is `{ valueFrom, jsonKey?, versionStage?, versionId? }`, and the outputs are `ecsEnvironment` and `ecsSecrets`, ready for a container definition's JSON. On cloudrun, `secret` is `{ secret, version? }` for a variable or a file input, and the outputs are `cloudrunEnv`, `cloudrunVolumes` and `cloudrunVolumeMounts`, in the Cloud Run (v2) API's shapes; in Go they are the google provider's `cloudrunv2` types. File inputs and overlays the target cannot carry are left out with a warning, and a required one fails generation, as for the module. Values are not `$`-escaped. In Go, each target's sources (`Secret(...).WithJSONKey(...)`, `Secret(...).WithVersion(...)`, `SecretFile`, `InjectedFile`) are in the package for that target only, so a Kubernetes source does not compile in an ecs component.
- **On containerapps**, `secret` is `{ keyVaultSecretId, identity? }` for a variable or a file input (`Secret(...).WithIdentity(...)` in Go), with the module's checks and app secret names (section 5.3). The outputs are in azure-native's `app.ContainerApp` shapes: `containerappSecrets` (`{name, keyVaultUrl, identity}`) for `configuration.secrets`, `containerappEnv` (`{name, value}` or `{name, secretRef}`), `containerappVolumes` and `containerappVolumeMounts`; in Go, azure-native's `app` types (`ContainerAppEnv`, …). Values are `$`-escaped, as on kubernetes. Unlike the module, which can only use one Secret volume with `sub_path`, a component gives each file input its own Secret volume holding only its secret (`{name: dc-NAME, storageType: Secret, secrets: [{secretRef, path}]}`), mounted at the input's directory, as on kubernetes.
- The component is a `ComponentResource` of type `docuconf:<contract-name>:Config`, and registers `bindings`, `kubernetesConfigMaps`, `checked`, its arguments and its contract's name and digest (with its target and language) as outputs, so the policy pack can find it in the stack: a stack policy sees a component's outputs, not its inputs.
- **In Python** the outputs are snake_case attributes (`kubernetes_env`, `cloudrun_volume_mounts`) holding the providers' Python input dicts (`{"name": ..., "value_from": {"secret_key_ref": ...}}`), so they go straight into a resource's arguments; an ECS container definition is JSON, so `ecs_environment` and `ecs_secrets` keep ECS's keys. The component needs the docuconf Python SDK, PyYAML and tomli-w, listed in its `requirements.txt`.
- **Its arguments are its inputs.** The component records `{ contract: { name, digest }, arguments }` as its Pulumi inputs (in Go, with `RegisterComponentResourceV2`), each argument as the program set it, in the sources' vocabulary. A preview shows a component's inputs but not its outputs, so this is what lets `docuconf lint pulumi` see how each input is set (section 6.4). Secret inputs are references, so no secret value is recorded; a value the program wrapped in `pulumi.secret()` stays a Pulumi secret in the state.

### 6.3 The policy pack

```sh
docuconf pulumi policy -contract contract.cue -target kubernetes -out policy/orders-config
cd policy/orders-config && npm install
pulumi preview --policy-pack policy/orders-config
```

The policy pack applies the rules of section 7.2 during `pulumi preview` and `pulumi up` (`--policy-pack`, or published with `pulumi policy publish` and enforced from Pulumi Cloud), to every stack that uses a component of its contract, in any language. It is a TypeScript pack, one per contract and target, and holds the contract and the rules (`policy.ts`), which are those `docuconf lint pulumi` applies to a preview (section 6.4), with the same codes:

- `docuconf-<contract>`, **mandatory**, a stack policy: for each component of the contract and the target (by the type, digest and target it registers), the workloads that take its env (by env names, as in section 6.4) and their app containers get no undeclared name, no name twice, no secret as a plain value, every name the component renders, every required input, file inputs mounted where the app reads them, whole and read-only, and the pod metadata an injector asks for; `envFrom` is refused. A value written into a container by hand is checked with `@docuconf/core`, as the app would check it at boot (`invalid_value`, in the SDK's words). A component generated from another version of the contract fails (`stale_module`). Its settings, `unmanagedEnv` and `allowEnvFrom`, are a module's `docuconf_options`.
- `docuconf-<contract>-advisory`: a component no workload takes (`module_unused`).

CrossGuard gives a policy the stack's values, Pulumi secrets in plaintext, but a value unknown at preview cannot be read: a workload whose containers hold one is not checked, and an unknown argument is not compared, since the component checks those values when they resolve (section 6.2). `docuconf lint pulumi` and the pack check the same rules; the linter also applies environment policy (`-policy`), which needs CUE, and lists what it could not check.

### 6.4 The linter

```sh
pulumi preview --json > preview.json
docuconf lint pulumi preview.json                    # in the program's directory, or -dir
docuconf lint pulumi preview.json -policy prod.cue -contract contract.cue -explain
```

`docuconf lint pulumi` applies the rules of section 7.2, and the policy of section 7.3, to a preview, as `docuconf lint terraform` does to a plan, with the same codes, flags and output formats. It is offline: it reads the preview and the generated components' `docuconf.json`, found under `-dir` (the program's directory by default) by the contract digest each component records.

- **Components** are the preview's resources of type `docuconf:<contract-name>:Config`, and their arguments are the inputs each records (section 6.2): what a Terraform module's arguments are to `lint terraform`.
- **Workloads** are the preview's planned inputs of the target's resources: on kubernetes, `Deployment`, `StatefulSet`, `DaemonSet`, `ReplicaSet`, `Job`, `CronJob`, `ReplicationController` and `Pod`; on ecs, `aws:ecs/taskDefinition:TaskDefinition`'s `containerDefinitions`; on cloudrun, a `cloudrunv2` `Service`'s or `Job`'s template; on containerapps, an `azure-native:app:ContainerApp`'s template, with each `secretRef` resolved to its `configuration.secrets`.
- **Binding.** A preview records no references between resources, so a workload binds to the component whose contract its app container's env names match (section 7.1, as for a workload that references no module). Pulumi writes a container list that holds a secret as `"[secret]"`, which no name can match; such a workload binds to the one component for its target, if there is one, and only that component's arguments are checked (`deferred`).
- **Unknown and secret values.** A value Pulumi knows only during the update, and a Pulumi secret, are `deferred`, as an unknown or sensitive value is in a plan: the component checks them when they resolve (section 6.2), and the app at boot.
- **What a module's `docuconf_options` say** that a component has no argument for are flags: `-unmanaged-env NAME,…` and `-allow-env-from`.
- **`stale_module`** regenerates each component from the contract its `docuconf.json` holds, in its language and for its target, and compares the files, as for a module.

Messages name the component's outputs as the program reads them: `orders.kubernetesEnv` in TypeScript, `orders.KubernetesEnv` in Go.

## 7. The linter

```sh
terraform plan -out plan.out && terraform show -json plan.out > plan.json
docuconf lint terraform plan.json                      # one line per problem, as docuconf vet prints
docuconf lint terraform plan.json -explain             # the binding table of section 7.1
docuconf lint terraform plan.json -policy prod.cue     # environment policy, as for vet
docuconf lint terraform plan.json -format json|sarif
docuconf lint terraform plan.json -dir infra/prod      # the root where terraform init ran
docuconf lint terraform plan.json -contract contract.cue  # also check the modules were generated from this contract
```

The linter is offline: it reads the plan JSON, the modules' `docuconf.json` (located through `.terraform/modules/modules.json` in the root given by `-dir`, or a local module source), and optionally `terraform providers schema -json` for attribute types (`-schema`, not yet implemented). OpenTofu's plan JSON has the same shape. It exits 0 when there are no errors (warnings and notices allowed), 1 when there are, and 2 on bad usage. Messages never contain a secret value, nor the value of anything the plan marks sensitive.

### 7.1 Reconstructing bindings

For each docuconf module call in the plan (including modules nested in other modules; the instances of a `count` or `for_each` call share its arguments' expressions), the linter:

1. reads each input argument's expression from the plan's `configuration` section and resolves its **origin**: a literal, or the `references` the expression makes (`aws_db_instance.orders.address`, `var.port`, `random_password.api.result`);
2. finds every **bound workload**, and in it the **app container**:
   - A workload is bound to the modules its configuration references. The plan's `configuration` section does not show references made inside `dynamic` blocks, which is how a container usually takes `kubernetes_env`, so a reference elsewhere (the pod template's `annotations = module.orders_config.pod_annotations`, a label from `module.orders_config.contract.name`) is what binds it.
   - A workload that references no docuconf module is bound by the env names its containers set, when exactly one module in its scope has a contract they match. With more than one, the linter cannot tell which, says so (`binding_ambiguous`), and checks nothing for it.
   - The app container is the one that sets the most of the contract's variables; other containers (sidecars) are ignored.
3. reads what the app container receives, from the plan's planned values (`after`, `after_unknown`, `after_sensitive`): each env entry, value or reference, known or not, sensitive or not; its volume mounts; and the pod's volumes and metadata. This is the configuration the app will boot with, whether the module rendered it or someone wrote it by hand. For the `ecs` target, the containers are the task definition's `container_definitions` JSON: `environment`, `secrets` (as references) and `environmentFiles` (as `env_from`). For `cloudrun`, they are the revision template's `containers`, whose secret volume mounts are read-only by construction. For `containerapps`, they are the template's containers, with each `secret_name` resolved to the app's `secret` block.
4. rebuilds the values document from it: each variable's env entry read back from its wire encoding ([SPEC.md section 5](SPEC.md#5-wire-encoding-and-parsing)), a reference in the values document's form, an injected source from the module's argument; and the files and overlays documents from the module's constant arguments. An unknown or sensitive value is left out and reported as `deferred`. `docuconf vet`'s engine then checks the documents, with `-policy` if given, so every constraint the contract states is checked on every known value, in vet's words.

`-explain` prints the result, one row per input:

```
orders_config → kubernetes_deployment_v1.orders, container "orders"
INPUT            SOURCE      ORIGIN                                   AT PLAN
DATABASE_URL     secret      kubernetes_secret_v1.orders_db           known (reference)
PORT             value       literal 8080                             known, checked
LOG_LEVEL        value       var.environment                          known, checked
OTEL_ENDPOINT    injected    otel-operator                            checked at boot
REDIS_URL        value       aws_elasticache_cluster.cache.address    unknown: checked at apply
WORKER_COUNT     —           unset (optional, default 4)              —
```

### 7.2 Rules

Each rule has a code. Errors fail the lint; warnings and notices do not. Codes shared with [SPEC.md](SPEC.md) and `docuconf vet` mean the same thing.

| Code | Level | Rule |
|---|---|---|
| `missing_required` | error | A required input is unset, and no selected profile supplies it ([SPEC.md section 4.4](SPEC.md#44-config-files-and-profiles)). With an unknown selector, every profile that could be selected must supply it. |
| `undeclared_input` | error | A bound container sets an env variable, or mounts a volume, the contract does not declare and the module did not render. This is the typo check: `PORTT`, or a second `LOG_LEVEL` added by hand. Names the platform sets for itself (a sidecar's settings, `OTEL_*` from an injector) are listed in `docuconf_options.unmanaged_env`, and the linter then ignores them. |
| `duplicate_env` | error | A bound container sets one name twice, for example the module's entry and a hand-written one. Kubernetes keeps the last; the linter refuses both. |
| `unbound_container` | error | A workload runs the contract's image (by digest, or by the repository in `docuconf.json`) but its env does not come from the module. With several containers, only the app container is checked; the others are ignored. |
| `secret_as_value` | error | A secret input reaches a plain env value: a literal, or an attribute that is sensitive or comes from a resource listed as secret-bearing (`random_password`, `aws_db_instance.password`, `tls_private_key`, `aws_iam_access_key.secret`, and others the linter knows). The module's types prevent this for its own arguments; the rule catches it when someone writes the value into the workload directly. |
| `invalid_value` | error | A known value breaks the input's constraint, checked by the `docuconf vet` engine on the reconstructed documents, with vet's message. This covers what HCL cannot (a `json` value's schema, a config file's schema) and what the module never saw (a value written into the container by hand). |
| `origin_type_mismatch` | warning | An unknown value's origin has a type that cannot satisfy the input, from the provider schema (`-schema`): a `bool` attribute into a `url` input, a `list` into a `string`. A `string` attribute into an `int` input is a warning too, since it may hold a number; the apply-time precondition decides. |
| `unsupported_source` | error | An input is set with a source the target cannot express (section 5), or a file input is bound on a target that cannot mount it. |
| `env_from_unchecked` | error | A bound container uses `env_from` (a whole ConfigMap or Secret as env). Its keys are unknown before apply, so it hides typos and missing inputs. Set `docuconf_options.allow_env_from = true` to downgrade it to a warning. |
| `mount_mismatch` | error | A file input's mount is missing, at a different path, uses `sub_path`, or is writable ([SPEC.md section 4.6](SPEC.md#46-file-inputs)). |
| `pod_metadata_misplaced` | error | The module's `pod_annotations` or `pod_labels` are set on the workload's metadata instead of the pod template's. |
| `pod_metadata_missing` | error | An annotation or label an injected source asks for (with its placeholders expanded) is not on the pod template, so the injector would not act. |
| `module_unused` | warning | No workload in the plan takes the module's outputs, so only its arguments are checked. |
| `binding_ambiguous` | notice | A workload's env matches more than one module's contract and references none of them outside a `dynamic` block, so it is not checked (section 7.1). |
| `target_unchecked` | notice | The module is for a target the linter does not check yet; only `stale_module` applies to it. |
| `contract_image_mismatch` | error | The bound container's image digest is not the one the contract was exported from (section 4.7). |
| `stale_module` | error | The module's files differ from what `docuconf terraform` generates from the contract its `docuconf.json` holds, so it was edited by hand; or, with `-contract`, the module was generated from another version of that contract. The contract digest is the sha256 of its compact JSON. |
| `policy_mismatch` | error | A value or a reference breaks the environment policy (section 7.3). The message names the input, the field and the constraint, such as `DATABASE_URL: awsSecretsManager.account: 210987654321 is not 123456789012 (policy prod.cue)`. A reference is not secret, so its fields are printed; a secret's value never is. |
| `policy_deferred` | notice | A policy constrains a field that is unknown at plan, such as the account of a secret created in the same apply. The notice names the input, the field and the constraint, and says whether the module checks it at apply (the constraint was compiled, section 4.6, and the selected `environment` is this policy) or nothing checks it before boot. |
| `deprecated_set` | warning | A deprecated input is set ([SPEC.md section 4.2](SPEC.md#42-variables)), printed with its message. |
| `deferred` | notice | A value is unknown at plan. The notice names the input and its origin, and says where it is checked: at apply by the module's preconditions, or at boot. |
| `image_unverified` | notice | The module records no image digest, so `contract_image_mismatch` could not run. |

What the linter cannot know, it says it cannot know: it never passes an input silently because a value was unknown.

Not yet implemented: `-schema` and `origin_type_mismatch`; `contract_image_mismatch`, which needs a module generated from an OCI reference (section 4.7), so every module gets `image_unverified`; and `unsupported_source`, which the targets' argument types already prevent.

### 7.3 Policy

`-policy` takes the same CUE policy as `docuconf vet` ([SPEC.md section 7](SPEC.md#7-platform-validation)): it is unified with the values the platform sets, so it can narrow what an environment allows without editing the app's contract. The linter builds that values document from the bindings of section 7.1, one field per input that is set, and adds two maps for rules that apply to every reference at once.

**Values.** A literal appears as its typed value, as in a values document. A reference appears in one of the forms below. The linter parses each target's reference into named fields, so a policy matches an account, a region or a name, not a substring of an ARN:

| Form | Target and source | Fields |
|---|---|---|
| `secretKeyRef` | `kubernetes`, `secret` | `name`, `key` (as in SPEC.md section 4.5) |
| `configMapKeyRef` | `kubernetes`, `config_map` | `name`, `key` |
| `fieldRef`, `resourceFieldRef` | `kubernetes` | as in SPEC.md section 4.5 |
| `awsSecretsManager` | `ecs`, `secret`, a Secrets Manager `valueFrom` | `arn` (as written), `partition`, `region`, `account`, `name` (without the 6-character suffix Secrets Manager appends to the ARN), and, when the `valueFrom` selects them, `jsonKey`, `versionStage`, `versionId` |
| `awsSsmParameter` | `ecs`, `secret`, a Parameter Store `valueFrom` | `arn`, `partition`, `region`, `account`, `name` (with its leading `/`) |
| `gcpSecretManager` | `cloudrun`, `secret` | `project`, `secret`, `version` (`latest` or a number) |
| `azureKeyVault` | `containerapps`, `secret`, a secret with `key_vault_secret_id` | `vault` (the vault's host name), `name`, `version` (empty when the ID has none), `identity` |
| `containerAppSecret` | `containerapps`, `secret`, a secret held by the app itself | `name` |
| `injected` | every target | `provider`, `ref` (as in SPEC.md section 4.5.1) |

A `valueFrom` that names a parameter without an ARN (ECS accepts a bare name in the task's own region and account) is an `awsSsmParameter` with only its `name`: the plan does not record the task's region and account, so a policy on them is `policy_deferred`, not passed.

A reference is one form: the linter closes it, so a policy that asks for another (`awsSecretsManager` where the input is wired to Parameter Store) fails with `WEBHOOK_KEYS: is set from awsSsmParameter, and the policy allows only awsSecretsManager here`, rather than adding the form beside it. To allow either, make both optional: `awsSecretsManager?: {…}`, `awsSsmParameter?: {…}`. `docuconf vet` checks the contract with the values document's own forms; only the policy sees these.

**`references` and `secretReferences`.** Beside the inputs, the document has two lowercase fields, which never collide with an input, since variable names are upper case ([SPEC.md section 4.2](SPEC.md#42-variables)): `references` holds every input set by reference, in the form above, keyed by input name, and `secretReferences` holds only the secret inputs among them. A rule for every secret is then one CUE pattern constraint instead of one rule per input.

A production policy for an ECS service:

```cue
// prod.cue
// Every secret comes from Secrets Manager in the prod account and region, under orders/prod/.
secretReferences: [string]: {
	awsSecretsManager: {
		account: "123456789012"
		region:  "us-east-1"
		name:    =~"^orders/prod/"
	}
	awsSsmParameter?: _|_ // Parameter Store is not allowed for secrets in prod
}

// A non-secret ARN the app reads: the contract checks its shape (pattern: "^arn:aws:sqs:"),
// the policy pins it to this environment's account and region.
ORDERS_QUEUE_ARN?: =~"^arn:aws:sqs:us-east-1:123456789012:"

LOG_LEVEL?: "info" | "warn" | "error"
```

With it, a `DATABASE_URL` wired to `arn:aws:secretsmanager:us-east-1:210987654321:secret:orders/prod/db-AbCdEf` fails with `policy_mismatch` on `account`, and one wired to a Parameter Store parameter fails on `awsSsmParameter`.

**Unknown fields.** A reference to a secret created in the same apply has an ARN that is unknown at plan. The linter fills in what the origin resource tells it: the secret's `name` from its configuration, and the `region` and `account` from the provider configuration or an `aws_caller_identity` data source when those are known. A constraint on a field that stays unknown is reported as `policy_deferred`, never passed. A policy compiled into the module (section 4.6) checks such a field at apply, before the workload changes; a constraint the generator could not compile is checked by nobody before boot, so a platform that needs that guarantee keeps the field known at plan: names from configuration, the account and region from provider configuration rather than from a lookup.

**`docuconf vet`.** The forms above that a values document can hold (`secretKeyRef`, `configMapKeyRef`, `fieldRef`, `resourceFieldRef`, `injected`) are what `vet` checks a policy against, and `vet` fills `references` and `secretReferences` the same way ([SPEC.md section 7](SPEC.md#7-platform-validation)), so one policy file works with `vet`, the Helm path and the linter for Kubernetes.

## 8. Conformance

The generated modules join the conformance suite of [SPEC.md section 12](SPEC.md#12-conformance-suite):

- **Render.** For every case in `cue/testdata/render`, the Terraform module and each Pulumi component, given the same inputs, MUST produce the same env entries, volumes, mounts, ConfigMap data, annotations and labels as `#Render`, value for value and in the same order. The suite runs the modules with `terraform console` and the components in each language's test runner.
- **Arguments.** For every value case in the load suite, the module's argument validation and the component's value checks MUST accept and reject exactly what `#Validate` accepts and rejects, with the same message.
- **Lint.** `conformance/iac/` holds plan JSON fixtures, each with the problems the linter must report: a typo, a missing required input, a secret written as a value, an unbound container, an unknown origin, `env_from`, a mount with `sub_path`, a stale module, a policy that pins the account and name of every secret (one plan within it, one outside, one with an unknown account), and one clean plan per target.

## 9. Compatibility

The module is generated from one contract, so contract changes ([SPEC.md section 9](SPEC.md#9-compatibility)) reach the platform as ordinary Terraform or compiler errors when the module is regenerated:

| Contract change | What the platform sees |
|---|---|
| A new required input | `Missing required argument` at validate. |
| A removed input | `Unsupported argument` at validate, wherever the platform still sets it. Deprecate first: a deprecated input keeps its argument, with a warning. |
| A tightened constraint | A validation error at plan, on values that no longer qualify. |
| A secret that was not secret before | The exactly-one validation fails, saying a secret is never a literal value: the platform must switch to a secret reference. |

`-check` in CI (section 4.1) keeps the committed module in step with the contract, and `stale_module` catches a module that drifted anyway. The module's outputs and their shapes are versioned with this document: in `v1alpha1` they may change; from `v1beta1` on, only additively.

## 10. Edge cases

- **Values known only after apply.** An RDS endpoint for a new database is unknown at plan. The linter reports `deferred`; the module's output preconditions check it during apply, before the workload changes. If the precondition fails, the database exists but the workload was not touched.
- **Unknown at plan and secret.** Nothing to check before boot: the module only checks that it is a reference of the right kind. Contents are the SDK's job, as for every secret.
- **Sensitive non-secret values.** A value Terraform marks sensitive but the contract does not declare secret (a hostname from a sensitive output) is checked like any value, but messages print `(sensitive)` instead of the value.
- **Several environments.** Each workspace or stack has its own arguments. `-policy` applies environment policy ([SPEC.md section 7](SPEC.md#7-platform-validation)) per run, as for `docuconf vet`.
- **One image, several workloads.** A worker and a web Deployment of the same image each bind their own module instance, possibly with different arguments. `unbound_container` applies to each.
- **Init containers and sidecars.** Only the container running the contract's image is bound. An init container of the same image is a second bound container and needs its own module instance or the same rendered env.
- **`helm_release`.** When the workload is a Helm chart using the docuconf library chart, the module's `helm_values` output feeds the release, and the chart's `values.schema.json` and templates do the rendering. The linter checks the release's values against the contract, not the rendered manifests, which the plan does not contain.
- **Plans from Terraform Cloud.** The plan JSON comes from the run's API instead of `terraform show`; its format is the same.
- **Drift.** Someone edits the Deployment's env in the cluster. Terraform reports the drift on the next plan, and the lint of that plan sees the module's values again. Correcting drift is the platform's existing job.
- **Origins the linter does not know.** A value from a `data` source of an unfamiliar provider is checked by type when `-schema` is given, and by value when it is known; otherwise it is `deferred`.

## 11. Phases

1. **Kubernetes, Terraform and OpenTofu.** `docuconf terraform -target kubernetes`, `docuconf lint terraform`, the render and argument conformance cases, and a docuconf.dev page. The orders example gets a Terraform platform beside its Helm one.
2. **Pulumi and more targets.** The TypeScript and Go components, the policy pack, and the `ecs` and `cloudrun` targets.
3. **The rest.** C# and Java components, the `containerapps` target (built, for Terraform and Pulumi), the `lambda` target, and SARIF output with source locations, when Terraform's plan JSON gains them.

## 12. Open questions

1. Should the module declare the workload resource itself, behind an option, for platforms that want a one-line deploy? This document says no (section 1.2), so that the platform keeps control of the resource, but it would make `unbound_container` impossible by construction.
2. Should the list of secret-bearing attributes (rule `secret_as_value`) be data the platform can extend, for providers docuconf does not know?
3. Should the linter also read the state, to check bindings that did not change in this plan? A plan only shows what changes; a clean plan with stale values elsewhere passes. Running `terraform plan -refresh-only` gives the linter the whole picture without reading the state directly.
4. A Terraform provider with a `docuconf_contract` data source could validate during plan without the extra lint step. It would be a binary to install and sign; is the trade worth it once the module and linter exist?
