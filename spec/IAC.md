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

- `-contract` is the contract file, or an OCI reference to one (`oci://registry/orders@sha256:…`, [SPEC.md section 8](SPEC.md#8-distribution)), in which case the image digest it belongs to is recorded (section 4.6).
- `-target` names one or more targets (section 5), comma-separated. Each adds its outputs; the input arguments are the same for all.
- `-out` is the module directory. The command writes only the files of section 4.2, each starting with `# Code generated by docuconf. DO NOT EDIT.`, and removes generated files it no longer writes.
- `-check` writes nothing and exits 1 with a diff when the directory is not what the command would write, so CI can keep a committed module in step with the contract, as `docuconf docs --check` does for docs.

The module is plain HCL and runs on Terraform 1.5 or later and OpenTofu 1.6 or later. It declares no provider and creates no resources, except the ConfigMaps of section 4.5 when the platform asks for them.

### 4.2 Files

| File | Holds |
|---|---|
| `variables.tf` | One input argument per contract input and overlay value (section 4.3), plus `docuconf_options` (below). |
| `render.tf` | `locals` that render each input in its wire encoding (section 4.4). |
| `outputs.tf` | Per target, the blocks the workload takes; `bindings`; `contract`. |
| `docuconf.json` | The contract as JSON, its digest, the generator version and the image digest if known. The linter reads it (section 7). |
| `README.md` | The input table, generated with the docs model ([SPEC.md section 14](SPEC.md#14-generated-docs)), and a usage example for each target. |

`docuconf_options` is the one argument that is not a contract input. All of its fields are optional:

| Field | Default | Meaning |
|---|---|---|
| `create_config_maps` | `false` | The module declares the `kubernetes_config_map_v1` resources for inline content and overlays itself, and needs the `kubernetes` provider. |
| `namespace` | `null` | The namespace for those ConfigMaps. |
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
    condition     = var.PORT == null || length([for k, v in var.PORT : k if v != null]) == 1
    error_message = "PORT: set exactly one of value, config_map or injected"
  }
  validation {
    condition     = try(var.PORT.value == null || (floor(var.PORT.value) == var.PORT.value && var.PORT.value >= 1 && var.PORT.value <= 65535), false)
    error_message = "PORT: must be a whole number from 1 to 65535"
  }
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
- **Secrets have no `value`.** A `secret: true` input, and every `keySet`, has only `secret` and `injected`, so `DATABASE_URL = { value = "postgres://…" }` is `Unsupported attribute` at validate. A secret file input has no `inline` and no `config_map`.
- **Required.** A required input has `nullable = false` and no default. A required input satisfied by a profile ([SPEC.md section 4.4](SPEC.md#44-config-files-and-profiles)) is optional here, since the module cannot know which profile the platform selects until the selector's argument is known; the linter checks it (section 7, rule `missing_required`).
- **Constraints** become `validation` blocks on the `value` attribute, one per constraint, with the error message `docuconf vet` prints for the same mistake ([SPEC.md section 7](SPEC.md#7-platform-validation)): ranges, `values`, `pattern`, `schemes`, lengths, list bounds and item bounds. They run at plan on known values. A constraint HCL cannot express exactly (a duration's bounds, a `json` value's `schema`, a `float`'s shortest round-trip form) is left to the linter and to boot, and the generated README says so.
- **Regular expressions.** Terraform's `regex` and `can(regex(...))` use Go's RE2, the dialect of [SPEC.md section 4.3](SPEC.md#43-types), so a `pattern` is copied unchanged and matches anywhere, as the spec requires.
- **Lengths count code points.** Terraform's `length()` counts grapheme clusters, not code points, so `"é"` written as `e` plus a combining accent is 1 to Terraform and 2 to the spec. The generator MUST count with `length(regexall("(?s).", v))`, which counts code points, never with `length(v)`.
- **Origins are free.** The argument says how an input is set, not where the value comes from: `DATABASE_URL = { secret = { name = kubernetes_secret_v1.db.metadata[0].name, key = "url" } }` and `PORT = { value = var.port }` are both fine. The linter reconstructs the origin from the plan (section 7.1).
- **Deprecated inputs** keep their argument. Setting one is a plan-time warning: the generator emits a `check` block whose assertion fails, with the input's `deprecated.message`, when the argument is set. A `check` block reports a warning and never stops the plan, which is the warning of [SPEC.md section 7](SPEC.md#7-platform-validation).
- **Overlays.** An overlay's values ([SPEC.md section 4.7](SPEC.md#47-config-file-overlays)) are arguments too, prefixed by the overlay: `overlay_platform = { CATALOG__PAGESIZE = 50 }`, a typed object of the overlay's variables, each optional. Supplying a variable both as an env argument and in an overlay fails a validation, since the environment would silently win.

### 4.4 Rendering

`render.tf` turns the arguments into env entries, volumes and mounts, with exactly the results of the CUE `#Render` ([SPEC.md section 7](SPEC.md#7-platform-validation)) and the Helm library chart:

- the wire encodings of [SPEC.md section 5](SPEC.md#5-wire-encoding-and-parsing): `csv`, `json` and `indexed` lists (an `indexed` list becomes one entry per item), the duration encodings, `int` and `float` forms, compact JSON;
- `$` escaped as `$$` for the `kubernetes` target, where the kubelet expands `$(VAR)`, and left alone for targets that do not;
- one entry per `pathEnv`, file inputs projected with `items` (never `subPath`), content-hashed ConfigMap names for inline content, the pod annotations and labels of injected sources with their placeholders expanded ([SPEC.md section 4.5.2](SPEC.md#452-enabling-the-injector)), and the restart triggers of [section 4.6.2](SPEC.md#462-rotation).

Every output that carries rendered values has `precondition`s that repeat each input's constraint checks. Terraform evaluates a precondition at plan when its values are known, and otherwise during apply, before anything that depends on the output. A workload that takes its env from the output therefore cannot be created or changed with a value that breaks the contract, even when the value came from an attribute computed earlier in the same apply.

### 4.5 Outputs

For the `kubernetes` target:

| Output | Shape | Goes into |
|---|---|---|
| `kubernetes_env` | list of `{name, value, value_from}`, exactly one of `value` and `value_from` non-null (one object type, so `for_each` accepts it) | a `dynamic "env"` block of the container |
| `kubernetes_volumes`, `kubernetes_volume_mounts` | lists in the provider's block shapes | `dynamic "volume"` in the pod spec, `dynamic "volume_mount"` in the container |
| `kubernetes_config_maps` | map of name to data, for inline content and overlays | `kubernetes_config_map_v1` resources the platform declares, or that the module declares when `docuconf_options.create_config_maps` is true |
| `pod_annotations`, `pod_labels` | maps | the pod template's metadata, never the workload's ([SPEC.md section 4.5.2](SPEC.md#452-enabling-the-injector)) |
| `restart_triggers` | map of source to content hash | a pod template annotation, or the platform's reloader |
| `helm_values` | the `docuconf:` values of the Helm library chart | a `helm_release` that uses the chart instead of `kubernetes_deployment_v1` |

The other targets have their own outputs in section 5. Every module also has:

- `bindings`: for each input, `{source = "secret" | "value" | …, set = true | false}`, plus `contract_digest`. The linter uses it to find the module in the plan and to tell an unset optional input from one the module failed to render.
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

### 4.6 Contract and image

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
| File inputs | every source | none | `secret` (Secret Manager volumes) | `secret` (secret volumes) | none |
| `$` escaping | yes | no | no | no | no |
| Phase | 1 | 2 | 2 | 3 | 3 |

A file input on a target that cannot mount it is a generation error, not a runtime surprise: `docuconf terraform -target ecs` on a contract with a required file input fails, naming the input. An optional one is left out with a warning.

Wire encodings are the same on every target; only `$` escaping is specific to Kubernetes.

## 6. Pulumi

### 6.1 The command

```sh
docuconf pulumi -contract contract.cue -target kubernetes -lang typescript -out sdk/orders-config
docuconf pulumi policy -contract contract.cue -target kubernetes -out policy/orders-config
```

The first generates a **component** in one language (`typescript`, `python`, `go`, `csharp` or `java`): a local package the program imports, with no provider plugin. The second generates a **CrossGuard policy pack** (TypeScript, which Pulumi runs against programs in every language). Both take `-check` as in section 4.1.

### 6.2 The component

The component has the arguments and outputs of the Terraform module, in the language's own types:

```ts
// Code generated by docuconf. DO NOT EDIT.
export interface OrdersConfigArgs {
  /** Primary Postgres connection string */
  DATABASE_URL: SecretRef | Injected;                 // required, secret: no literal accepted
  /** HTTP listen port */
  PORT?: { value: pulumi.Input<number> } | ConfigMapRef | Injected;
  LOG_LEVEL?: { value: pulumi.Input<"debug" | "info" | "warn" | "error"> } | ConfigMapRef | Injected;
}

export class OrdersConfig extends pulumi.ComponentResource {
  readonly kubernetesEnv: pulumi.Output<k8s.types.input.core.v1.EnvVar[]>;
  readonly kubernetesVolumes: pulumi.Output<k8s.types.input.core.v1.Volume[]>;
  readonly kubernetesVolumeMounts: pulumi.Output<k8s.types.input.core.v1.VolumeMount[]>;
  readonly podAnnotations: pulumi.Output<Record<string, string>>;
  readonly bindings: pulumi.Output<Bindings>;
  constructor(name: string, args: OrdersConfigArgs, opts?: pulumi.ComponentResourceOptions) { /* … */ }
}
```

Rules:

- **Type-level checks** where the language has them: a secret input's type has no `value` variant, an `enum` is a union of its values, a required input is not optional. TypeScript, Go, C# and Java catch these at compile time. Python checks them when the component is constructed.
- **Value checks** run on each argument when it resolves, inside `apply`, and throw `ConfigViolation` with the message `docuconf vet` prints. A known value fails `pulumi preview`. An unknown one fails `pulumi up` when it resolves, and since every rendered output depends on it, no resource that uses the outputs is created or changed.
- **Pulumi secrets are not secret references.** A value wrapped in `pulumi.secret()` is encrypted in the state, but rendered as a plain env value it is plaintext in the pod spec or task definition. The component accepts a secret input only as the target's secret reference or as `injected`, never as a value, secret or not.
- The component is a `ComponentResource` of type `docuconf:<contract-name>:Config`, and registers `bindings` as an output, so the policy pack can find it in the stack.

### 6.3 The policy pack

The policy pack applies the rules of section 7 during `pulumi preview` and `pulumi up` (`--policy-pack policy/orders-config`, or enforced from Pulumi Cloud):

- a **resource policy** for each workload type of the target, checking its containers' env, volumes and image;
- a **stack policy** for the cross-resource rules: each bound container's env equals the component's rendered output, and every component instance is used by a workload.

Rules that fail on an error are `mandatory`; warnings are `advisory`. Inputs that are unknown in preview are checked by the component during `up` (section 6.2) and are reported as `deferred` notices.

## 7. The linter

```sh
terraform plan -out plan.out && terraform show -json plan.out > plan.json
docuconf lint terraform plan.json                      # one line per problem, as docuconf vet prints
docuconf lint terraform plan.json -explain             # the binding table of section 7.1
docuconf lint terraform plan.json -policy prod.cue     # environment policy, as for vet
docuconf lint terraform plan.json -format json|sarif
```

The linter is offline: it reads the plan JSON, the modules' `docuconf.json` (located through the plan's module sources), and optionally `terraform providers schema -json` for attribute types (`-schema`). OpenTofu's plan JSON has the same shape. It exits 0 when there are no errors (warnings and notices allowed), 1 when there are, and 2 on bad usage. Messages never contain a secret value, nor the value of anything the plan marks sensitive.

### 7.1 Reconstructing bindings

For each docuconf module instance in the plan (including `count` and `for_each` instances, and modules nested in other modules), the linter:

1. reads each input argument's expression from the plan's `configuration` section and resolves its **origin**: a literal, or the `references` the expression makes (`aws_db_instance.orders.address`, `var.port`, `random_password.api.result`);
2. reads the argument's planned value, and whether it is known and sensitive (`after_unknown`, `after_sensitive`);
3. finds every **bound container**: a container in a workload of the target whose env (and volume, mount and annotation) expressions reference the module's rendered outputs.

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
| `invalid_type`, `out_of_range`, `pattern_mismatch`, `not_in_enum`, `invalid_scheme`, `too_few_items`, `too_many_items`, `schema_mismatch` | error | A known value breaks the input's constraint, checked by the `docuconf vet` engine on the reconstructed values document. This covers what HCL cannot: duration bounds, a `json` value's schema, a policy (`-policy`). |
| `origin_type_mismatch` | warning | An unknown value's origin has a type that cannot satisfy the input, from the provider schema (`-schema`): a `bool` attribute into a `url` input, a `list` into a `string`. A `string` attribute into an `int` input is a warning too, since it may hold a number; the apply-time precondition decides. |
| `unsupported_source` | error | An input is set with a source the target cannot express (section 5), or a file input is bound on a target that cannot mount it. |
| `env_from_unchecked` | error | A bound container uses `env_from` (a whole ConfigMap or Secret as env). Its keys are unknown before apply, so it hides typos and missing inputs. Set `docuconf_options.allow_env_from = true` to downgrade it to a warning. |
| `mount_mismatch` | error | A file input's mount is missing, at a different path, uses `sub_path`, or is writable ([SPEC.md section 4.6](SPEC.md#46-file-inputs)). |
| `pod_metadata_misplaced` | error | The module's `pod_annotations` or `pod_labels` are set on the workload's metadata instead of the pod template's. |
| `contract_image_mismatch` | error | The bound container's image digest is not the one the contract was exported from (section 4.6). |
| `stale_module` | error | The module's `docuconf.json` does not match the contract it says it was generated from (`-contract` given), or was edited by hand. |
| `deprecated_set` | warning | A deprecated input is set ([SPEC.md section 4.2](SPEC.md#42-variables)), printed with its message. |
| `deferred` | notice | A value is unknown at plan. The notice names the input and its origin, and says where it is checked: at apply by the module's preconditions, or at boot. |
| `image_unverified` | notice | The module records no image digest, so `contract_image_mismatch` could not run. |

What the linter cannot know, it says it cannot know: it never passes an input silently because a value was unknown.

## 8. Conformance

The generated modules join the conformance suite of [SPEC.md section 12](SPEC.md#12-conformance-suite):

- **Render.** For every case in `cue/testdata/render`, the Terraform module and each Pulumi component, given the same inputs, MUST produce the same env entries, volumes, mounts, ConfigMap data, annotations and labels as `#Render`, value for value and in the same order. The suite runs the modules with `terraform console` and the components in each language's test runner.
- **Arguments.** For every value case in the load suite, the module's argument validation and the component's value checks MUST accept and reject exactly what `#Validate` accepts and rejects, with the same message.
- **Lint.** `conformance/iac/` holds plan JSON fixtures, each with the problems the linter must report: a typo, a missing required input, a secret written as a value, an unbound container, an unknown origin, `env_from`, a mount with `sub_path`, a stale module, and one clean plan per target.

## 9. Compatibility

The module is generated from one contract, so contract changes ([SPEC.md section 9](SPEC.md#9-compatibility)) reach the platform as ordinary Terraform or compiler errors when the module is regenerated:

| Contract change | What the platform sees |
|---|---|
| A new required input | `Missing required argument` at validate. |
| A removed input | `Unsupported argument` at validate, wherever the platform still sets it. Deprecate first: a deprecated input keeps its argument, with a warning. |
| A tightened constraint | A validation error at plan, on values that no longer qualify. |
| A secret that was not secret before | `Unsupported attribute "value"`: the platform must switch to a secret reference. |

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
3. **The rest.** Python, C# and Java components, the `containerapps` and `lambda` targets, and SARIF output with source locations, when Terraform's plan JSON gains them.

## 12. Open questions

1. Should the module declare the workload resource itself, behind an option, for platforms that want a one-line deploy? This document says no (section 1.2), so that the platform keeps control of the resource, but it would make `unbound_container` impossible by construction.
2. Should the list of secret-bearing attributes (rule `secret_as_value`) be data the platform can extend, for providers docuconf does not know?
3. Should the linter also read the state, to check bindings that did not change in this plan? A plan only shows what changes; a clean plan with stale values elsewhere passes. Running `terraform plan -refresh-only` gives the linter the whole picture without reading the state directly.
4. A Terraform provider with a `docuconf_contract` data source could validate during plan without the extra lint step. It would be a binary to install and sign; is the trade worth it once the module and linter exist?
