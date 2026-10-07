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
  before it deploys ([`contract.cue`](contract.cue)).

| Variable | Type | Rules |
|---|---|---|
| `PORT` | int | 1–65535, default `8080` |
| `LOG_LEVEL` | enum | `debug`, `info`, `warn`, `error`; default `info` |
| `DATABASE_URL` | url | secret, required, scheme `postgres` |
| `ALLOWED_ORIGINS` | list of strings, comma-separated | at least 1 item; default `http://localhost:3000` |
| `REQUEST_TIMEOUT` | duration | `1s`–`5m`, default `30s` |
| `WORKER_COUNT` | int | 1–64, default `4` |

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
{"ALLOWED_ORIGINS":["http://localhost:3000"],"DATABASE_URL":"***","LOG_LEVEL":"info","PORT":8080,"REQUEST_TIMEOUT":"30s","WORKER_COUNT":4}
```

`/config` shows the typed values from `docuconf.Redacted`; the secret is always `***`.

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

[`smoke.sh`](smoke.sh) checks all three runs; CI runs it on every push.

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
