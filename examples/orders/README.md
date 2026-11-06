# Example: orders

A tiny `net/http` service whose configuration is declared with docuconf. It
shows the three things the Go SDK gives an app:

- a normal [caarlos0/env](https://github.com/caarlos0/env) struct, with
  docuconf tags for descriptions, secrets, constraints and file inputs
  ([`internal/config/config.go`](internal/config/config.go)), and a test
  for it ([`config_test.go`](internal/config/config_test.go));
- one check at boot that reports every problem at once, with stable codes
  ([`main.go`](main.go));
- a CUE contract exported from the struct, for the platform to validate
  before it deploys ([`contract.cue`](contract.cue)), and docs generated
  from it for developers and AI agents ([`CONFIG.md`](CONFIG.md),
  [`CONFIG.agents.md`](CONFIG.agents.md)).

| Variable | Type | Rules |
|---|---|---|
| `PORT` | int | 1–65535, default `8080` |
| `LOG_LEVEL` | enum | `debug`, `info`, `warn`, `error`; default `info` |
| `DATABASE_URL` | url | secret, required, scheme `postgres`, at most 2048 characters |
| `ALLOWED_ORIGINS` | list of strings, comma-separated | at least 1 item; default `http://localhost:3000` |
| `REQUEST_TIMEOUT` | duration | `1s`–`5m`, default `30s` |
| `WORKER_COUNT` | int | 1–64, default `4` |
| `WEBHOOK_KEYS` | key set, comma-separated | secret, optional; 1–2 keys of 32–256 characters each |

| File input | Type | Rules |
|---|---|---|
| `serving-tls` at `/etc/orders/tls` | TLS key pair | optional; covers `orders.example.com`, 30 days left; reloaded on change |
| `discounts` at `/etc/orders/discounts/discounts.yaml` | YAML config | optional; `codes`: a map of code to percent |

The example has its own `go.mod`, which builds against the SDK in this
repository through a `replace` directive.

## Run it

```console
$ cd examples/orders
$ DATABASE_URL=postgres://orders:pw@localhost:5432/orders go run .
$ curl localhost:8080/healthz
ok
$ curl localhost:8080/config
{"ALLOWED_ORIGINS":["http://localhost:3000"],"DATABASE_URL":"***","LOG_LEVEL":"info","PORT":8080,"REQUEST_TIMEOUT":"30s","WEBHOOK_KEYS":"***","WORKER_COUNT":4}
```

`/config` shows the typed values from `docuconf.Redacted`; secrets are always `***`, set or not.

## When the configuration is wrong

With `PORT=0` and no `DATABASE_URL`, the service refuses to start, exits 1
and lists every problem, not just the first:

```console
$ PORT=0 go run .
docuconf: 2 configuration problems:
  PORT: 0 is below min 1 (out_of_range)
  DATABASE_URL: is required but not set (missing_required)
exit status 1
```

## Run it with files

`DOCUCONF_FILE_ROOT=./dev` reads `/etc/orders/tls` from
`./dev/etc/orders/tls`. Make a certificate that satisfies the declaration
and a discounts file, then the service serves HTTPS:

```console
$ mkdir -p dev/etc/orders/tls dev/etc/orders/discounts
$ openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 90 \
    -subj /CN=orders.example.com -addext subjectAltName=DNS:orders.example.com \
    -keyout dev/etc/orders/tls/tls.key -out dev/etc/orders/tls/tls.crt
$ echo 'codes: {WELCOME10: 10}' > dev/etc/orders/discounts/discounts.yaml
$ DATABASE_URL=postgres://orders:pw@localhost:5432/orders DOCUCONF_FILE_ROOT=./dev go run .
$ curl -k https://localhost:8080/discounts
{"WELCOME10":10}
$ curl -k https://localhost:8080/reloadz
{"serving-tls":{"generation":1}}
```

The certificate is watched: replace `tls.crt` and `tls.key` and, within
10 seconds, the next handshake gets the new certificate, the service
logs it, and `/reloadz` shows generation 2. A replacement that fails the
checks is not used; `/reloadz` then shows it under `lastRejected`, with
its codes. [`internal/upstream`](internal/upstream/upstream.go) shows an
HTTP client that rebuilds itself when a watched CA bundle changes.

[`smoke.sh`](smoke.sh) checks all three runs, and the webhook key set below; CI runs it on every push.

## Rotate a key

`WEBHOOK_KEYS` is a key set: `POST /webhooks/payments` accepts a body
whose `X-Signature` header is the hex HMAC-SHA256 of the body under any
key in the set ([`internal/webhook`](internal/webhook/webhook.go), with
`docuconf.KeySet`'s `Verify`). A variable is read once, at start, so a new
key reaches the service only when the pods restart; with two keys valid
at once, no webhook is turned away while that happens:

1. Add the new key as the second key (`old,new` in the Secret), and roll out.
2. Switch the sender to the new key.
3. Remove the old key (`new`), and roll out.

[`CONFIG.md`](CONFIG.md) prints these steps for every key set, so the
field's doc comment does not repeat them.

The contract allows 1 or 2 keys of 32 to 256 characters each, so a
trailing comma or a truncated key stops the service at boot instead of
locking out the sender:

```console
$ DATABASE_URL=postgres://orders:pw@localhost:5432/orders \
    WEBHOOK_KEYS=old-webhook-key-0123456789abcdef0123, go run .
docuconf: 1 configuration problem:
  WEBHOOK_KEYS: key 2 is empty (out_of_range)
exit status 1
```

[`webhook_test.go`](internal/webhook/webhook_test.go) walks through a
rotation, and [`smoke.sh`](smoke.sh) posts webhooks signed with both
keys. [SPEC section 6.1](../../spec/SPEC.md#61-rotation) covers rotation in general.

## Export the contract

`contract.cue` is generated; never edit it by hand. Re-export it after
changing `internal/config/config.go` (CI fails if it is out of date). The
exporter is pinned as a Go tool in `go.mod`:

```console
$ go tool docuconf-export -pkg ./internal/config -name orders-api -package orders -o contract.cue
$ go tool docuconf-export -pkg ./internal/config -name orders-api -package orders -check contract.cue
```

The struct lives in its own package because the exporter imports it, and
package `main` cannot be imported.

## Generate docs

[`CONFIG.md`](CONFIG.md), [`CONFIG.agents.md`](CONFIG.agents.md) and
[`docs.json`](docs.json) are generated from `contract.cue` by the
`docuconf` CLI; never edit them by hand either. The first is the
reference for developers, the second the rules and facts AI agents need
to change the code or set deployment values, and the third the docs
model both are rendered from. Regenerate them after exporting the
contract (CI fails if they are out of date):

```console
$ docuconf docs contract.cue -o CONFIG.md
$ docuconf docs contract.cue --format agents -o CONFIG.agents.md
$ docuconf docs contract.cue --format model -o docs.json
$ docuconf docs contract.cue --check CONFIG.md
```

`WORKER_COUNT` shows where the text comes from: the first paragraph of
its doc comment is the description, and the second paragraph its
details. `WEBHOOK_KEYS` is a key set, so its docs carry the rotation
steps, and its details need not repeat them.

## Deploy

The app ships `contract.cue`, and the platform checks its inputs
([`deploy/values.yaml`](deploy/values.yaml),
[`deploy/files.yaml`](deploy/files.yaml)) against it before anything
reaches the cluster: `docuconf vet` reports every bad or
missing value, secret given as a literal or policy violation, and
`docuconf render` turns valid inputs into the pod's env. A Crossplane
composition can evaluate the same contract in plain CUE, and a Helm-based
platform can use the
[docuconf Helm chart](https://github.com/docuconf/docuconf-go/tree/main/helm),
which generates a `values.schema.json` from the contract. The
[walkthrough](../walkthrough) shows all three paths end to end.

## Deploy with Terraform

[`deploy/terraform`](deploy/terraform) sets the same inputs from
Terraform or OpenTofu. `docuconf terraform` generates a module from the
contract, with one argument per input:

```sh
docuconf terraform -contract contract.cue -out deploy/terraform/modules/orders-config
```

The platform sets each input through the module, from wherever the value
comes, and takes the container's env, volumes, mounts and ConfigMaps from
its outputs:

```hcl
module "orders_config" {
  source = "./modules/orders-config"

  DATABASE_URL = { secret = { name = "orders-db", key = "url" } }
  LOG_LEVEL    = { value = var.environment == "prod" ? "warn" : "debug" }
  WORKER_COUNT = { value = 8 }
  discounts    = { inline = { codes = { WELCOME10 = 10, SPRING25 = 25 } } }
  # …
}
```

Each argument accepts only the sources its input allows, so
`DATABASE_URL = { value = "postgres://…" }` fails at `terraform validate`:
a secret is never a literal. The module checks every value against the
contract at plan, with the same messages as `docuconf vet`:

```
Error: Invalid value for variable

WORKER_COUNT: 100 is above max 64
```

and repeats the checks at apply for values Terraform only learns then.
Then `docuconf lint terraform` checks the plan: what the container will
actually receive, including env written by hand beside the module's:

```sh
terraform plan -out plan.out
terraform show -json plan.out > plan.json
docuconf lint terraform plan.json -contract ../../contract.cue -explain
```

It reports a typo such as `PORTT` (with a "did you mean `PORT`?"), a
secret set as a plain value, a file mounted at the wrong path, or a module
generated from an older contract. See
[spec/IAC.md](../../spec/IAC.md) for the module and the linter's rules.

### ECS, Cloud Run and Azure Container Apps

[`deploy/ecs`](deploy/ecs), [`deploy/cloudrun`](deploy/cloudrun) and
[`deploy/containerapps`](deploy/containerapps) set the same inputs for
Amazon ECS, Cloud Run and Azure Container Apps, each through a module
generated for its target:

```sh
docuconf terraform -contract contract.cue -target ecs -out deploy/ecs/modules/orders-config
docuconf terraform -contract contract.cue -target cloudrun -out deploy/cloudrun/modules/orders-config
docuconf terraform -contract contract.cue -target containerapps -out deploy/containerapps/modules/orders-config
```

A secret there is a Secrets Manager or Parameter Store ARN, a Secret
Manager secret, or a Key Vault secret ID read by a managed identity. ECS
mounts no files, so its module has no `discounts` or `serving-tls`. A Cloud
Run secret volume and a Container Apps secret each hold a single file, so
those modules take `discounts` from Secret Manager or Key Vault but have no
`serving-tls`. In all three, TLS ends in front of the service. CI plans and
lints each on every change, without a cloud account: the azurerm provider
wants a token before it plans, so CI points it at
[`ci/fakeazure`](deploy/containerapps/ci/fakeazure), a stand-in for the few
Azure endpoints it calls first.

### End-to-end test

[`deploy/terraform/e2e/e2e.sh`](deploy/terraform/e2e/e2e.sh) builds the
service's image ([`Dockerfile`](Dockerfile)), applies `deploy/terraform`
to a kind cluster with cert-manager, and checks the running service:

- a value the contract forbids fails at plan;
- the plan passes `docuconf lint`;
- the service serves HTTPS with the issued certificate;
- `/config` shows the values set in `main.tf`, with secrets redacted;
- `/discounts` serves the inline file;
- both rotating webhook keys are accepted;
- a discount code added through Terraform rolls out;
- a renewed certificate is served without a restart.

It needs docker, kind, kubectl, terraform, docuconf, curl and openssl, and
runs in CI on every change:

```sh
deploy/terraform/e2e/e2e.sh
```

## Deploy with Pulumi

[`deploy/pulumi`](deploy/pulumi) sets the same inputs from a Pulumi
program in TypeScript, through a component `docuconf pulumi` generates
from the contract:

```sh
docuconf pulumi -contract contract.cue -out deploy/pulumi/orders-config
```

Each argument is a source in the vocabulary of `values.yaml`: `{ value }`,
`{ secretKeyRef }`, `{ inline }`, `{ certificate }` and so on. A secret
has no `value`, so `DATABASE_URL: { value: "postgres://…" }` does not
compile. The component checks every value with `@docuconf/core`, the
engine the service runs at boot, so a value that would stop the service
from starting stops the deployment instead:

```
docuconf:orders-api:Config (orders):
  error: orders-api config: 1 configuration problem:
    WORKER_COUNT: must be at most 64 (got 100) (out_of_range)
```

CI runs the program with the kubernetes provider rendering YAML to a
directory, and checks that the manifests match `docuconf render` of
[`deploy/values.yaml`](deploy/values.yaml) and
[`deploy/files.yaml`](deploy/files.yaml).

`docuconf lint pulumi` checks a preview as `docuconf lint terraform`
checks a plan: what each workload's containers get, against the contract
and the component's arguments, which it records as its inputs. It catches
what the component cannot see, such as an env entry written into the
Deployment by hand, and applies an environment policy with `-policy`:

```sh
cd deploy/pulumi
pulumi preview --json > preview.json
docuconf lint pulumi preview.json -contract ../../contract.cue -explain
```

[`deploy/pulumi-policy`](deploy/pulumi-policy) holds the same rules as
CrossGuard policy packs, one per target, which Pulumi applies itself
during `pulumi preview` and `pulumi up`, or Pulumi Cloud enforces on
every stack:

```sh
docuconf pulumi policy -contract contract.cue -target kubernetes -out deploy/pulumi-policy/kubernetes
(cd deploy/pulumi-policy/kubernetes && npm ci)
cd deploy/pulumi && pulumi preview --policy-pack ../pulumi-policy/kubernetes
```

An env entry written into the Deployment by hand, such as `PORTT`, fails
the preview:

```
[mandatory]  docuconf-orders-api  (kubernetes:apps/v1:Deployment: orders-api)
  The orders-api contract's rules: each workload that takes a docuconf component's env gets what the contract declares, through the component (docuconf lint's errors).
  PORTT: is not declared in the contract, and the component did not render it; did you mean PORT? (undeclared_input)
```

CI previews every program with its target's pack.

[`deploy/pulumi-go`](deploy/pulumi-go) is the same program in Go,
through the component `docuconf pulumi -lang go` generates:

```sh
docuconf pulumi -contract contract.cue -lang go -out deploy/pulumi-go/ordersapiconfig
```

Each source is a constructor, and each input's field accepts only the
sources its input allows: `DATABASE_URL` is a `SecretVar`, which
`ordersapiconfig.SecretKeyRef(...)` implements and
`ordersapiconfig.String(...)` does not, so a literal secret does not
compile. Values are checked with the Go SDK.

[`deploy/pulumi-python`](deploy/pulumi-python) is the same program in
Python, through the component `docuconf pulumi -lang python` generates:

```sh
docuconf pulumi -contract contract.cue -lang python -out deploy/pulumi-python/orders_api_config
```

Each argument is a `TypedDict` in the same vocabulary, so mypy or pyright
refuses `{"value": ...}` for `DATABASE_URL`, and values are checked with
the docuconf Python SDK, in its words:

```
error: orders-api config: 1 configuration problem:
  WORKER_COUNT: Input should be less than or equal to 64 (got '100') (out_of_range)
```

The outputs are snake_case, as the kubernetes provider's Python arguments
are: `env=orders.kubernetes_env`.

### ECS, Cloud Run and Container Apps, from Pulumi

[`deploy/pulumi-ecs`](deploy/pulumi-ecs) and
[`deploy/pulumi-cloudrun`](deploy/pulumi-cloudrun) are the Terraform roots
above, `deploy/ecs` and `deploy/cloudrun`, as Pulumi programs in
TypeScript; [`deploy/pulumi-go-ecs`](deploy/pulumi-go-ecs) and
[`deploy/pulumi-go-cloudrun`](deploy/pulumi-go-cloudrun) are the same in
Go, and [`deploy/pulumi-go-containerapps`](deploy/pulumi-go-containerapps)
is `deploy/containerapps` in Go, on azure-native. Each sets the same
inputs through a component generated for its target:

```sh
docuconf pulumi -contract contract.cue -target ecs -out deploy/pulumi-ecs/orders-config
docuconf pulumi -contract contract.cue -target cloudrun -out deploy/pulumi-cloudrun/orders-config
docuconf pulumi -contract contract.cue -target ecs -lang go -out deploy/pulumi-go-ecs/ordersapiconfig
docuconf pulumi -contract contract.cue -target cloudrun -lang go -out deploy/pulumi-go-cloudrun/ordersapiconfig
docuconf pulumi -contract contract.cue -target containerapps -lang go -out deploy/pulumi-go-containerapps/ordersapiconfig
```

The component's outputs go straight into the resource:
`ecsEnvironment` and `ecsSecrets` into the task definition's container
definition, `cloudrunEnv`, `cloudrunVolumeMounts` and `cloudrunVolumes`
into the Cloud Run service's template, and `ContainerAppSecrets` into the
container app's configuration, `ContainerAppEnv`,
`ContainerAppVolumeMounts` and `ContainerAppVolumes` into its template.
The Terraform module mounts files from one Secret volume with `sub_path`,
which is all azurerm can express; the component gives each file its own
volume, which azure-native can, and the check below compares which secret
each path holds.

[`deploy/check-cloud-plan.sh`](deploy/check-cloud-plan.sh) previews a
program without a cloud account, and checks that it plans the same
container configuration as the Terraform root of its target, that the
preview passes `docuconf lint pulumi`, and that a forbidden value fails at
preview. CI runs it for all five on every change:

```sh
deploy/check-cloud-plan.sh ecs deploy/pulumi-ecs
deploy/check-cloud-plan.sh cloudrun deploy/pulumi-go-cloudrun
deploy/check-cloud-plan.sh containerapps deploy/pulumi-go-containerapps
```

### End-to-end test, from Pulumi

[`deploy/pulumi/e2e.sh`](deploy/pulumi/e2e.sh) is the end-to-end test
above for the Pulumi programs (`deploy/pulumi/e2e.sh deploy/pulumi-go`
for the Go one, `deploy/pulumi/e2e.sh deploy/pulumi-python` for the
Python one): it deploys to a kind cluster with `pulumi
up`, and checks the running service the same way, with a forbidden value
failing at preview, the preview passing `docuconf lint pulumi`, and a
discount code added through stack config rolling out. Both tests share [`deploy/e2e`](deploy/e2e): the cluster's fixtures
and the checks.
