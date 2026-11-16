# Example: shop

One image, two processes: the shop's web server and its job worker. The
example shows three contract features end to end:

- **roles** (spec §4.9): the struct declares the roles `web` and `worker`
  with a `docuconf.Roles` field, and each input only one role uses names
  it in a `roles` tag. The platform sets `DOCUCONF_ROLE` to the workload's
  role, and the SDK requires only the inputs that role uses;
- **requiredIf** (spec §4.2.1): `LOG_SCHEMA_URL` is required only while
  `LOG_FORMAT` is `json`, and the `webhook-signing-key` file only while
  `WEBHOOKS_ENABLED` is `true`;
- an **included fragment** (spec §4.8): a `docuconf.Fragments` field
  includes `otel@1`, so the contract carries the OpenTelemetry SDK's
  variables, and the SDK checks their values at boot, though the app never
  reads them.

The config is [`internal/config/config.go`](internal/config/config.go),
and [`contract.cue`](contract.cue) is exported from it. The program
([`main.go`](main.go)) runs no server, database or queue: it loads its
config, prints one line for its role and exits 0. On bad config it prints
every problem and exits 1, and never prints a secret.

| Variable | Type | Rules |
|---|---|---|
| `DOCUCONF_ROLE` | enum | `web` or `worker`; required (declared by docuconf) |
| `DATABASE_URL` | url | secret, required, scheme `postgres` or `postgresql` |
| `PORT` | int | `web` only; 1–65535, default `8080` |
| `ALLOWED_ORIGINS` | list of strings, comma-separated | `web` only; required |
| `QUEUES` | list of strings, comma-separated | `worker` only; required |
| `WEBHOOKS_ENABLED` | bool | `worker` only; default `false` |
| `LOG_FORMAT` | enum | `text` or `json`; default `text` |
| `LOG_SCHEMA_URL` | url | scheme `https`; required when `LOG_FORMAT` is `json` |
| `OTEL_*` | from the `otel@1` fragment | optional; checked at boot |

| File input | Type | Rules |
|---|---|---|
| `webhook-signing-key` at `/etc/shop/webhooks/signing.key` | text | `worker` only; secret, at least 32 characters; required when `WEBHOOKS_ENABLED` is `true` |

The example has its own `go.mod`, which builds against the SDK in this
repository through a `replace` directive.

## Run each role

```console
$ cd examples/shop
$ DATABASE_URL=postgres://shop@db:5432/shop DOCUCONF_ROLE=web ALLOWED_ORIGINS=https://shop.example go run .
shop web: port 8080, origins https://shop.example
$ DATABASE_URL=postgres://shop@db:5432/shop DOCUCONF_ROLE=worker QUEUES=default,mail go run .
shop worker: queues default,mail, webhooks off
```

The worker needs no `ALLOWED_ORIGINS`: that input is the web server's.
With JSON logs but no schema, the worker refuses to start:

```console
$ DATABASE_URL=postgres://shop@db:5432/shop DOCUCONF_ROLE=worker QUEUES=default LOG_FORMAT=json go run .
docuconf: 1 configuration problem:
  LOG_SCHEMA_URL: is required when LOG_FORMAT is "json" but not set (missing_required)
exit status 1
```

To turn webhooks on, the worker needs the signing key. Locally, put it
under a directory and point `DOCUCONF_FILE_ROOT` at it:

```console
$ mkdir -p dev/etc/shop/webhooks
$ openssl rand -hex 32 > dev/etc/shop/webhooks/signing.key
$ DATABASE_URL=postgres://shop@db:5432/shop DOCUCONF_ROLE=worker QUEUES=default WEBHOOKS_ENABLED=true DOCUCONF_FILE_ROOT=./dev go run .
shop worker: queues default, webhooks on
```

## Test matrix

[`main_test.go`](main_test.go) runs each case with `go test`, from the
base env `DATABASE_URL=postgres://shop@db:5432/shop` and a fresh, empty
file root, and [`smoke.sh`](smoke.sh) runs the built program for #1, #2
and #6. Lists are comma-separated (`csv`), the Go SDK's default.

| # | Env (plus the base) | Expected |
|---|---|---|
| 1 | `DOCUCONF_ROLE=web ALLOWED_ORIGINS=https://shop.example` | ok: `shop web: port 8080, origins https://shop.example` |
| 2 | `DOCUCONF_ROLE=worker QUEUES=default,mail` | ok: the worker needs no `ALLOWED_ORIGINS` |
| 3 | `DOCUCONF_ROLE=web` | `ALLOWED_ORIGINS` `missing_required` |
| 4 | `QUEUES=default` (no role) | `DOCUCONF_ROLE` `missing_required`, and nothing else |
| 5 | `DOCUCONF_ROLE=cron` | `DOCUCONF_ROLE` `not_in_enum`, and nothing else |
| 6 | `DOCUCONF_ROLE=worker QUEUES=default LOG_FORMAT=json` | `LOG_SCHEMA_URL` `missing_required`: `is required when LOG_FORMAT is "json" but not set` |
| 7 | #6 plus `LOG_SCHEMA_URL=https://logs.example/v1.json` | ok |
| 8 | `DOCUCONF_ROLE=worker QUEUES=default WEBHOOKS_ENABLED=true`, no key file | `webhook-signing-key` `file_missing`: `… does not exist, and is required when WEBHOOKS_ENABLED is true` |
| 9 | #8 with a key of 32 or more characters at the path under the root | ok: `webhooks on` |
| 10 | `DOCUCONF_ROLE=web ALLOWED_ORIGINS=https://shop.example WEBHOOKS_ENABLED=true` | ok: the key file is the worker's |
| 11 | `DOCUCONF_ROLE=web ALLOWED_ORIGINS=https://shop.example OTEL_EXPORTER_OTLP_PROTOCOL=carrier-pigeon` | `OTEL_EXPORTER_OTLP_PROTOCOL` `not_in_enum`: fragment variables are checked at boot |

```sh
go test ./...
examples/shop/smoke.sh
```

## Export the contract

`contract.cue` is generated; never edit it by hand. Re-export it after
changing `internal/config/config.go` (CI fails if it is out of date):

```sh
go tool docuconf-export -pkg ./internal/config -name shop -package shop -o contract.cue
go tool docuconf-export -pkg ./internal/config -name shop -package shop -check contract.cue
```

The contract lists the roles, each role-specific input's roles,
`DOCUCONF_ROLE`, the conditions, and `includes: ["otel@1"]` with the
fragment's variables inline.

## Generate docs

[`CONFIG.md`](CONFIG.md), [`CONFIG.agents.md`](CONFIG.agents.md) and
[`docs.json`](docs.json) are generated from `contract.cue` by the
`docuconf` CLI; never edit them by hand either. They say which role uses
each input and when a conditional input is required. Regenerate them after
exporting the contract (CI fails if they are out of date):

```sh
docuconf docs contract.cue -o CONFIG.md
docuconf docs contract.cue --format agents -o CONFIG.agents.md
docuconf docs contract.cue --format model -o docs.json
```
