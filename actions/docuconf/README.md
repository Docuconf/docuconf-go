# docuconf GitHub Action

A composite action that installs the `docuconf` CLI and runs one of its checks:

- `vet`: check platform values (and file sources, overlays, policy) against a contract, or against the contract
  pulled for an image digest;
- `diff`: classify the changes between two contracts (spec section 9.1) and fail on an unacknowledged breaking
  change;
- `docs-check`: fail when committed docs (`CONFIG.md`, `CONFIG.agents.md`, `docs.json`) are out of date;
- `none`: only install the CLI, for your own `run:` steps.

There are no release tags for the action yet. Pin it to a commit SHA:

```yaml
- uses: Docuconf/docuconf-go/actions/docuconf@<commit sha>
```

## Installing the CLI

`version` picks the CLI:

| `version` | What is installed |
|---|---|
| `latest` (default) | The newest `cmd/docuconf/v*` GitHub Release that is not a pre-release. |
| `0.2.0` | That release. |
| `source` | The CLI built from the action's own commit, so `@<sha>` runs the CLI at that same commit. Needs Go on the runner (GitHub-hosted runners have it; the CLI's toolchain is fetched with `GOTOOLCHAIN=auto`). |

A release binary (`docuconf_<version>_<os>_<arch>.tar.gz`, or `.zip` on Windows) is downloaded with the release's
`SHA256SUMS`, and installed only if its checksum matches. When there is no such release, or no binary for the
runner's platform, the action falls back to `go install github.com/docuconf/docuconf-go/cmd/docuconf@<version>` (or
`@latest`). Linux, macOS and Windows runners on x64 and arm64 are supported.

## Inputs

| Input | Default | Used by | Meaning |
|---|---|---|---|
| `command` | (required) | | `vet`, `diff`, `docs-check` or `none`. |
| `version` | `latest` | all | CLI version, `latest` or `source` (above). |
| `contract` | `contract.cue` | all | vet: the contract; diff: the new contract; docs-check: the contract the docs come from. |
| `values` | | vet | Variable values (YAML, JSON or CUE). |
| `files` | | vet | File input sources. |
| `overlays` | | vet | Config-file overlay values. |
| `policy` | | vet | Environment policy (CUE). |
| `image` | | vet | `registry/repo@sha256:...` (or a tag): pull this image's contract with `docuconf pull` and vet against it instead of `contract`. Log in to the registry first. |
| `old` | | diff | The old contract. Empty: take it from `base-ref`. |
| `base-ref` | | diff | A branch or commit holding the old contract at the same path as `contract`. It is fetched from `origin`; when the contract does not exist there yet, diff is skipped. |
| `ack` | | diff | File of acknowledged breaking changes, `<input> <change-id>` per line. |
| `allow-breaking` | `false` | diff | `true`: report breaking changes without failing. |
| `docs` | `CONFIG.md` | docs-check | The committed docs file. |
| `docs-format` | `markdown` | docs-check | `markdown`, `agents` or `model`. |
| `working-directory` | `.` | all | Directory the paths are relative to. |
| `repository` | `docuconf/docuconf-go` | install | Where CLI releases are downloaded from. |
| `token` | `github.token` | install | Token for the GitHub API, used to find the latest release. |

Outputs: `version` (the CLI installed, or `source` / `go install`) and `exit-code` (the command's exit status). GitHub drops a composite action's outputs when the action fails, so `exit-code` is only readable when the action passes (for example `0` with `allow-breaking: true`); on failure, check the step's `outcome`.

The step fails with the CLI's exit status: 1 for problems found by vet, a breaking change found by diff, or stale
docs; 2 for a usage or parse error. diff's lines also appear as annotations (breaking as errors, notable as
warnings), and vet's and diff's output goes to the job summary.

## App repository: block breaking contract changes

The app exports `contract.cue` from code (`docuconf export -check`, or its SDK's equivalent) and commits it. On every
pull request, compare it with the base branch's:

```yaml
# .github/workflows/contract.yml
name: contract
on:
  pull_request:
    paths: ["contract.cue", "contract-ack.txt"]

jobs:
  diff:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: Docuconf/docuconf-go/actions/docuconf@<commit sha>
        with:
          command: diff
          contract: contract.cue
          base-ref: ${{ github.base_ref }}
          ack: contract-ack.txt # optional: breaking changes the platform team has accepted
      - uses: Docuconf/docuconf-go/actions/docuconf@<commit sha>
        with:
          command: docs-check
          contract: contract.cue
          docs: CONFIG.md
```

To merge a breaking change on purpose, add its line to `contract-ack.txt` in the same pull request (the change id is
in brackets in the diff output), and have the platform team review it:

```
# PORT moves to LISTEN_ADDR; platform values updated in platform#142
PORT var-removed
```

Empty the file after the release that carried the change. On release, push the contract next to the image, so the
platform can fetch it by digest:

```yaml
      - id: build
        uses: docker/build-push-action@v6
        with:
          push: true
          tags: ghcr.io/acme/orders:${{ github.sha }}
      - uses: Docuconf/docuconf-go/actions/docuconf@<commit sha>
        with:
          command: none
      - run: |
          artifact=$(docuconf push --image ghcr.io/acme/orders@${{ steps.build.outputs.digest }} contract.cue)
          cosign sign --yes "$artifact"
```

## GitOps repository: vet values against the image's contract

A platform repository holds each service's values and the image digest it deploys. Vet the values against the
contract that was pushed with that exact image, so image B is never validated against contract A:

```yaml
# .github/workflows/vet.yml
name: vet
on:
  pull_request:
    paths: ["apps/orders/**"]

jobs:
  vet:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: read
    steps:
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: image
        run: echo "ref=$(cat apps/orders/image.txt)" >> "$GITHUB_OUTPUT" # ghcr.io/acme/orders@sha256:...
      - uses: Docuconf/docuconf-go/actions/docuconf@<commit sha>
        with:
          command: vet
          image: ${{ steps.image.outputs.ref }}
          values: apps/orders/values.yaml
          files: apps/orders/files.yaml
          policy: policies/production.cue
```

## GitLab CI

There is no GitLab component; the same checks are a few lines of script. The CLI's container image
(`ghcr.io/docuconf/docuconf`) is distroless and has no shell, so GitLab cannot run `script:` in it. Install the
release binary instead, checked against `SHA256SUMS`:

```yaml
# .gitlab-ci.yml
.docuconf:
  image: alpine:3.20
  variables:
    DOCUCONF_VERSION: "0.2.0"
  before_script:
    - apk add --no-cache curl git
    - base="https://github.com/docuconf/docuconf-go/releases/download/cmd%2Fdocuconf%2Fv$DOCUCONF_VERSION"
    - name="docuconf_${DOCUCONF_VERSION}_linux_amd64"
    - curl -fsSLO "$base/$name.tar.gz" && curl -fsSLO "$base/SHA256SUMS"
    - grep " $name.tar.gz\$" SHA256SUMS | sha256sum -c -
    - tar -xzf "$name.tar.gz" && mv "$name/docuconf" /usr/local/bin/

contract-diff:
  extends: .docuconf
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  script:
    - git fetch --depth=1 origin "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"
    - git show "FETCH_HEAD:contract.cue" > /tmp/base-contract.cue || { echo "no contract on the target branch"; exit 0; }
    - docuconf diff /tmp/base-contract.cue contract.cue --ack contract-ack.txt

docs-check:
  extends: .docuconf
  script:
    - docuconf docs contract.cue --check CONFIG.md

vet-values: # in the GitOps repository
  extends: .docuconf
  script:
    # Registry credentials as a Docker config.json, from a CI/CD variable of type File.
    - export DOCKER_CONFIG="$(mktemp -d)" && cp "$REGISTRY_DOCKER_CONFIG" "$DOCKER_CONFIG/config.json"
    - docuconf pull --image "$(cat apps/orders/image.txt)" -o /tmp/contract.cue
    - docuconf vet -contract /tmp/contract.cue -values apps/orders/values.yaml -files apps/orders/files.yaml
```

Until there is a CLI release, use `image: golang:1.25` and
`go install github.com/docuconf/docuconf-go/cmd/docuconf@<commit>` in `before_script` instead.

`docuconf diff --format json` gives the same changes as a list of `{input, change, class, reason}` objects, for a bot
that comments on the merge request.
