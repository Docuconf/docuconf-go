# Edge cases

Things that will go wrong in real clusters, and what docuconf does about each. Status:

- **Handled:** in the schema, with a test in `spec/cue`.
- **Spec:** needs a spec change. The proposed fix is listed.
- **Build:** a rule for the CLI, the Crossplane function or an SDK.

## Kubernetes itself

| Case | What goes wrong | Response | Status |
|---|---|---|---|
| `$(VAR)` expansion | Kubernetes expands `$(NAME)` in env values and turns `$$` into `$`. A password or template containing `$(HOSTNAME)` arrives rewritten. | `#Render` doubles every `$` in literal values. Tested in `testdata/render/escapingEnv.yaml`. | Handled |
| Service links | With `enableServiceLinks` on (the default), a Service named `redis` injects `REDIS_PORT=tcp://10.0.0.7:6379`. An app expecting `REDIS_PORT=6379` crashes or connects wrongly. | The function sets `enableServiceLinks: false`. `vet` warns when a contract variable matches `*_SERVICE_HOST`, `*_SERVICE_PORT` or `*_PORT`. | Build |
| `envFrom` | A ConfigMap or Secret bulk-loaded with `envFrom` adds variables the contract never saw. Explicit `env` entries override it silently. | The function owns the container's `env`, and rejects `envFrom` on contract-managed containers unless explicitly allowed. | Build |
| Mutating webhooks | The OpenTelemetry operator (`OTEL_*`), the Datadog admission controller (`DD_*`) and others inject env after composition. Validation never sees those values, and a required `OTEL_SERVICE_NAME` fails composition even though the webhook would have set it. | Platform-declared **injected variables**: a list the platform guarantees, which count as set and cannot be set in values. | Spec |
| Downward API and other references | `GOMEMLIMIT` from `resourceFieldRef`, `POD_IP` from `fieldRef`, a value from `configMapKeyRef`. None are literals or secrets, so v1alpha1 rejects them. | Allowed for non-secret variables (SPEC §4.5): `fieldRef` only for strings, `resourceFieldRef` only for ints. A `configMapKeyRef` value is checked at boot. | Handled |
| Secret not there yet | An ExternalSecret created in the same composition has not synced, so the pod sits in `CreateContainerConfigError`. | The function checks that the Secret and key exist, using Crossplane required resources. While an ExternalSecret it owns is syncing, it waits rather than fails. | Build |
| Values that do not exist yet | A database endpoint comes from a composed RDS instance that is still provisioning. | Treat "not yet known" (incomplete in CUE) as pending, not invalid. The condition reads `Waiting`, not `ContractValid=False`. | Build |
| Boot failures are invisible | The SDK rejects a secret's contents at boot, and the pod crash-loops with the reason buried in logs. | SDKs also write the violations to `/dev/termination-log`, so `kubectl describe pod` shows them. | Build |
| Size limits | Linux caps a single env string at 128 KiB, and the whole environment shares the argument-size limit. A large JSON list or a PEM bundle can make the container fail to start. | `vet` warns above 32 KiB per value. Certificates and other large blobs belong in mounted files. | Build |

## Files

| Case | What goes wrong | Response | Status |
|---|---|---|---|
| `subPath` mounts | A file mounted with `subPath` never receives updates. A renewed certificate silently never reaches the pod, which keeps serving the old one until it expires. | `#Render` projects files with `items` and mounts the directory, never `subPath`. | Handled |
| Mounting over a directory | Mounting a CA bundle at `/etc/ssl/certs/private.pem` mounts over `/etc/ssl/certs` and hides the system trust store. Mounting at `/app/config.json` hides the application. | The contract rejects mount directories in a reserved list, and two inputs sharing a directory. | Handled |
| Secrets in the platform repo | Someone pastes a TLS key or keystore into a values file as inline content. | Secret file inputs only accept `secret`, `certificate` or `csi` sources. | Handled |
| Certificate renewal window | cert-manager renews a 90-day certificate 10 days before expiry, but the app (or a partner pinning policy) needs 30 days of validity. | `minRemaining` in the contract, checked against the Certificate's `renewBefore` before deploy, and against the real certificate at boot. | Handled |
| Wrong certificate | The Certificate does not cover a hostname the app serves, or uses a key algorithm the app or its clients cannot use. | `dnsNames` and `keyAlgorithms`, checked against the Certificate spec before deploy and the real certificate at boot. | Handled |
| ConfigMap size | ConfigMaps and Secrets are limited to 1 MiB. A GeoIP database or ML model does not fit. | `binary` inputs from an `image` source (an image volume). | Handled |
| Non-root containers | Secret volumes are owned by root. With mode `0400`, a container running as a non-root user cannot read them unless the pod sets `fsGroup`. | The function sets `fsGroup` when it mounts secret files for a non-root container. The SDK reports `file_unreadable` with that hint. | Build |
| Atomic updates | Kubernetes updates projected files by swapping a `..data` symlink. A watcher on the file itself misses the change, or sees a half-updated set of files. | SDKs implementing `reload: watch` watch the directory and re-read all files in it together (SPEC §11.2). | Spec |
| Certificate chains | `tls.crt` holds the leaf only, or the chain in the wrong order. Some clients fail while browsers succeed. | At boot the SDK builds the chain and, with `requireCA`, verifies it against `ca.crt`. | Build |
| Encoding of text files | A config file saved with a UTF-8 byte-order mark, or Windows line endings, fails some parsers. A licence file gains a trailing newline. | Parsers in the toolchain and SDKs accept a BOM in JSON and YAML. `text` patterns can allow an optional trailing newline, as the gateway example does. | Build |
| Inline config never changes | The app reads its config file once, and the platform edits the inline content. | Inline content becomes a content-hashed, immutable ConfigMap, so any edit rolls the pods. For referenced sources, `reload: restart` lists them in `restartTriggers`. | Handled |

## YAML and the platform's values

| Case | What goes wrong | Response | Status |
|---|---|---|---|
| YAML 1.1 types | Kubernetes YAML follows YAML 1.1, so in a claim `COUNTRY: NO` is the boolean `false`, `LEVEL: off` is `false`, `VERSION: 1.10` is the float `1.1`, and `MODE: 0755` is octal `493`. | The typed check already rejects these. The error translator adds "quote this value" when a string variable receives a bool or number. | Handled, plus Build for the hint |
| Secrets in error messages | CUE prints the offending value. A secret pasted as a literal would appear in the XR condition, events and logs. | The error translator never prints a value for a `secret` variable, or for any value that fails the secret-reference check. | Build |
| Policy against the contract | The policy requires a value the contract forbids, so nothing can satisfy both. | The error names both sources (contract or policy) and both constraints. | Build |
| Manual drift | Someone runs `kubectl set env` on the Deployment. | Crossplane reconciles the composed Deployment back. The function records the rendered env hash so drift is visible. | Build |

## Images and distribution

| Case | What goes wrong | Response | Status |
|---|---|---|---|
| Tags move | A claim references `app:1.4`. The tag is repushed, so the image and its contract change under a running platform. | The function resolves tags to a digest, validates against that digest's contract, and pins the digest in the Deployment. | Build |
| Multi-arch images | The contract is attached to one platform manifest, but the claim references the image index. | The CLI attaches to the index digest; `pull` falls back to checking the index's manifests. | Build |
| Registry mirrors | Air-gapped mirrors and replication rules often copy images but not their referrers, so the contract is lost. | Document mirror flags (`crane copy --referrers`, oras, Harbor replication settings). A missing contract is an explicit error, not a silent skip. | Build |
| Third-party images | Redis, nginx and vendor images have no contract. | Platform-authored contracts, keyed by image repository and version range, in a shared catalogue. Without one, an opt-out per claim. | Spec |
| Old images, new spec | The cluster runs images whose contracts use an older `apiVersion`. | The function supports every non-removed `apiVersion` and converts it, as Kubernetes does for CRDs. | Build |
| Contract tampering | An unsigned contract could be swapped for one with looser constraints. | Verify the contract's signature with the same cosign identity as the image. | Build |

## The app side

| Case | What goes wrong | Response | Status |
|---|---|---|---|
| Undeclared reads | Code calls `os.Getenv`, `ENV[]` or `process.env.X` directly, bypassing the declaration, so the contract is incomplete. | Lint rules per SDK: ESLint `n/no-process-env`, a RuboCop cop, a Roslyn analyzer, a Go analyzer. Each allows reads only through the config class. | Build |
| Libraries read env too | Rails reads `RAILS_ENV`, `RAILS_MAX_THREADS` and ActiveRecord's `DATABASE_URL`. Node reads `NODE_ENV` and `NODE_OPTIONS`; Go `GOMAXPROCS` and `GOMEMLIMIT`; the AWS SDK `AWS_REGION`; OpenTelemetry `OTEL_*`. Under strict unknown-variable checks, the platform cannot set any of them. | Ship **well-known fragments** per ecosystem (rails, node, go-runtime, dotnet-runtime, aws-sdk, otel) that an app includes in its declaration. | Spec + Build |
| One image, several processes | A Rails image runs `rails server`, `sidekiq` and `rails db:migrate`. Each needs different variables, and the migration job does not need `ALLOWED_ORIGINS`. | **Roles**: a variable lists the roles that need it, and `required` applies per role. The claim states the role for each workload. | Spec |
| Conditional requirements | `TLS_CERT_PATH` is required only when `TLS_ENABLED=true`. The data-only contract cannot express "required if". | Add `requiredIf: {VAR: value}` as data. The meta-schema can enforce it, since CUE handles the conditional. | Spec |
| Dynamic names | `ENV["#{tenant}_API_KEY"]`, or a caarlos0/env map parsed from a variable-name prefix. | Out of scope for v1. The SDK warns, and the variables must be declared explicitly. | Spec (later) |
| Integers in JavaScript | CUE and Go ints are 64-bit, but a JavaScript `number` is exact only to 2^53. `z.coerce.number()` silently rounds larger values. | The TypeScript exporter caps `max` at `Number.MAX_SAFE_INTEGER`, or uses `bigint` when a variable needs more. | Build |
| Float edge values | `NaN`, `Inf` and `1e3` are accepted by some parsers and not others. .NET parsing follows the current culture unless told otherwise, so `0,5` and `0.5` differ by locale. | The spec forbids NaN and Inf. SDKs parse with the invariant culture. The renderer never emits exponents for values that fit plainly. | Spec |
| Whitespace and newlines | A trailing space or newline from `kubectl create secret --from-file` breaks a token. A multi-line PEM certificate breaks some `.env` parsers. | No SDK trims values (SPEC §5). Secrets created from files are a known trap; the SDK hints at it when a secret value ends in `\n`. Certificates go in files. | Spec + Build |
| Same name, different case | In .NET, configuration keys are case-insensitive, so `Inventory__Port` and `INVENTORY__PORT` both bind to the same setting. | Names are uppercase in the contract. `vet` rejects values that differ only by case. | Build |
| Build-time variables | `NEXT_PUBLIC_*` is baked into the bundle. Setting it on a pod does nothing. | Excluded from the runtime contract (SPEC §11.1). | Handled |
| Renames | Renaming `DB_URL` to `DATABASE_URL` breaks every environment at once. | Use `deprecated.replacedBy`. The SDK reads the new name, then falls back to the old one with a warning, for one release. `diff` flags the removal. | Spec |
| Tests and CI | Unit tests and `assets:precompile` boot the app without production values. | Every SDK documents a test mode and skips validation for build-time tasks (see the Rails note in PLAN.md). | Build |
| Feature flags in disguise | `ENABLE_NEW_CHECKOUT` gets toggled by redeploying. | A naming lint and the SPEC §10 guidance. | Handled (lint is Build) |
