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
| `WEBHOOK_KEYS` | list of strings, comma-separated | secret, optional; 1–2 keys of 32–256 characters each |

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
```

[`smoke.sh`](smoke.sh) checks all three runs, and the webhook key set below; CI runs it on every push.

## Rotate a key

`WEBHOOK_KEYS` is a key set: `POST /webhooks/payments` accepts a body
whose `X-Signature` header is the hex HMAC-SHA256 of the body under any
key in the list ([`internal/webhook`](internal/webhook/webhook.go)). A
variable is read once, at start, so a new key reaches the service only
when the pods restart; with two keys valid at once, no webhook is turned
away while that happens:

1. Add the new key as the second item (`old,new` in the Secret), and roll out.
2. Switch the sender to the new key.
3. Remove the old key (`new`), and roll out.

The contract allows 1 or 2 keys of 32 to 256 characters each, so a
trailing comma or a truncated key stops the service at boot instead of
locking out the sender:

```console
$ DATABASE_URL=postgres://orders:pw@localhost:5432/orders \
    WEBHOOK_KEYS=old-webhook-key-0123456789abcdef0123, go run .
docuconf: 1 configuration problem:
  WEBHOOK_KEYS: item 1: value is 0 characters, below itemMinLength 32 (out_of_range)
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
details. `WEBHOOK_KEYS`'s details carry its rotation steps as a list.

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
