# Releasing

This repository has three things to release, each with its own tag:

| What | Tag | Published by |
|---|---|---|
| The SDK module `github.com/docuconf/docuconf-go` | `v0.1.0` | The tag alone. The Go module proxy fetches it on first use; there is no workflow. |
| The CLI module `github.com/docuconf/docuconf-go/cmd/docuconf` | `cmd/docuconf/v0.1.0` | `.github/workflows/release.yml` (binaries, container image) |
| The Helm library chart `helm/docuconf` | `chart-v0.1.0` | `.github/workflows/release.yml` (OCI chart) |

The CLI is its own Go module (`cmd/docuconf/go.mod`), and Go requires a module in a subdirectory to be tagged with
the directory as prefix: `go install github.com/docuconf/docuconf-go/cmd/docuconf@v0.1.0` resolves the tag
`cmd/docuconf/v0.1.0`. The release workflow uses that same tag, so the binaries, the image and `go install` always
agree on the version. Plain `v*` tags stay reserved for the SDK, and the chart's `chart-v*` tags match neither (Go
ignores them), so the three never clash.

## Each release

- **SDK:** `git tag v0.2.0 && git push origin v0.2.0`.
- **CLI:** `git tag cmd/docuconf/v0.2.0 && git push origin cmd/docuconf/v0.2.0`. (The CLI does not depend on the SDK
  module today; if it ever does, tag the SDK first.)
- **Chart:** set `version` in `helm/docuconf/Chart.yaml`, commit, then
  `git tag chart-v0.2.0 && git push origin chart-v0.2.0`. The workflow fails if the tag and `Chart.yaml` differ.

A tag with a hyphen (`cmd/docuconf/v0.2.0-beta.1`) is a pre-release: its GitHub Release is marked as one and the image
does not move `latest`.

## GitHub Packages and Releases

Everything this repository publishes goes to GitHub:

| Tag | Job | Result |
|---|---|---|
| `cmd/docuconf/v*` | `cli-test` | The CLI's `gofmt`, build, vet and tests, as in `go.yml`. Both jobs below need it. |
| `cmd/docuconf/v*` | `cli-binaries` | A GitHub Release with `docuconf_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) for linux, darwin and windows on amd64 and arm64, static (`CGO_ENABLED=0`), each with an SPDX SBOM (`<archive name>.spdx.json`), plus `SHA256SUMS` over all of them and its cosign signature `SHA256SUMS.sigstore.json`. |
| `cmd/docuconf/v*` | `cli-image` | `ghcr.io/docuconf/docuconf:<version>` and `:latest`, for linux/amd64 and linux/arm64, built with buildx from `cmd/docuconf/Dockerfile` (a static binary on `gcr.io/distroless/static-debian12:nonroot`, at `/docuconf`). The image is signed with cosign, and its SPDX SBOM is attached as a signed cosign attestation. |
| `chart-v*` | `chart` | After the checks from `helm.yml`, `helm package` and `helm push` to `oci://ghcr.io/docuconf/charts` (so `oci://ghcr.io/docuconf/charts/docuconf`), and a GitHub Release with the chart `.tgz` and its `.sha256`. The OCI chart is signed with cosign. |

All of it uses only the workflow's own `GITHUB_TOKEN` (`contents: write`, `packages: write`) and the job's GitHub OIDC
token (`id-token: write`, for signing): no accounts and no secrets. The only requirement is that the `Docuconf` organization lets `GITHUB_TOKEN` write packages, which it does
unless package creation has been restricted under Organization settings > Packages.

One step after the very first push of each: container images and OCI charts start out **private** on ghcr.io. Open
the `docuconf` and `charts/docuconf` packages under the organization's Packages tab, link them to this repository if
they are not already, and set their visibility to public (Package settings > Change visibility). Later versions keep
the setting.

### Installing

No token is needed for anything public on ghcr.io or on a GitHub Release.

- **CLI with Go:** `go install github.com/docuconf/docuconf-go/cmd/docuconf@v0.1.0` (or `@latest`).
- **CLI binary:** download the archive for your platform and `SHA256SUMS` from the `cmd/docuconf/v0.1.0` release on
  the [Releases page](https://github.com/docuconf/docuconf-go/releases) (or
  `gh release download cmd/docuconf/v0.1.0 -R docuconf/docuconf-go -p 'docuconf_*_linux_amd64.tar.gz' -p SHA256SUMS`),
  then check and unpack it:
  ```sh
  sha256sum --ignore-missing -c SHA256SUMS
  tar -xzf docuconf_0.1.0_linux_amd64.tar.gz    # docuconf_0.1.0_linux_amd64/docuconf
  ```
- **CLI in a container image:**
  ```dockerfile
  COPY --from=ghcr.io/docuconf/docuconf:0.1.0 /docuconf /usr/local/bin/docuconf
  ```
  or run it directly: `docker run --rm -v "$PWD:/work" -w /work ghcr.io/docuconf/docuconf:0.1.0 vet -contract
  contract.cue -values values.yaml`. The image has no Go toolchain, so `docuconf export` (which loads Go packages)
  belongs in a Go build image; `vet`, `render` and `helm` work anywhere.
- **Helm chart:** depend on it from OCI (Helm 3.8 or later):
  ```yaml
  # Chart.yaml
  dependencies:
    - name: docuconf
      version: 0.1.0
      repository: oci://ghcr.io/docuconf/charts
  ```
  then `helm dependency update`. To fetch it alone: `helm pull oci://ghcr.io/docuconf/charts/docuconf --version
  0.1.0`.

### Verifying a release

Releases are signed with [cosign](https://docs.sigstore.dev/cosign/) keyless signing. There is no key to download:
each signing job gets a short-lived certificate from Sigstore's Fulcio for its GitHub Actions OIDC identity, and the
signature is recorded in the public Rekor transparency log. To verify, you check that the certificate was issued to
this repository's release workflow, running for a release tag:

| | |
|---|---|
| OIDC issuer (`--certificate-oidc-issuer`) | `https://token.actions.githubusercontent.com` |
| Identity of a CLI release (`--certificate-identity-regexp`) | `^https://github\.com/[Dd]ocuconf/docuconf-go/\.github/workflows/release\.yml@refs/tags/cmd/docuconf/v` |
| Identity of a chart release (`--certificate-identity-regexp`) | `^https://github\.com/[Dd]ocuconf/docuconf-go/\.github/workflows/release\.yml@refs/tags/chart-v` |

The identity is the workflow file and the ref that ran it, so a signature made by any other workflow, fork or branch
does not match. To pin one release exactly, use `--certificate-identity` with the full value, such as
`https://github.com/Docuconf/docuconf-go/.github/workflows/release.yml@refs/tags/cmd/docuconf/v0.1.0`. The examples
need cosign v3 or later (the release workflow signs with v3, which writes Sigstore bundles).

- **Binaries:** verify the signature on `SHA256SUMS`, then check the archives against it, as above:
  ```sh
  gh release download cmd/docuconf/v0.1.0 -R docuconf/docuconf-go \
    -p 'docuconf_0.1.0_linux_amd64.*' -p 'SHA256SUMS*'
  cosign verify-blob SHA256SUMS \
    --bundle SHA256SUMS.sigstore.json \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp '^https://github\.com/[Dd]ocuconf/docuconf-go/\.github/workflows/release\.yml@refs/tags/cmd/docuconf/v'
  sha256sum --ignore-missing -c SHA256SUMS
  ```
  `SHA256SUMS` also covers the SBOMs, `docuconf_<version>_<os>_<arch>.spdx.json`.
- **Container image:**
  ```sh
  cosign verify ghcr.io/docuconf/docuconf:0.1.0 \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp '^https://github\.com/[Dd]ocuconf/docuconf-go/\.github/workflows/release\.yml@refs/tags/cmd/docuconf/v'
  ```
  The signature is on the multi-arch index digest, so it covers both platforms. For the SBOM attestation, run the same
  command as `cosign verify-attestation --type spdxjson`; it prints the signed in-toto statement, whose `predicate` is
  the SPDX document. Pin the image by the digest you verified (`ghcr.io/docuconf/docuconf@sha256:...`).
- **Helm chart:**
  ```sh
  cosign verify ghcr.io/docuconf/charts/docuconf:0.1.0 \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp '^https://github\.com/[Dd]ocuconf/docuconf-go/\.github/workflows/release\.yml@refs/tags/chart-v'
  ```
  The chart `.tgz` on the GitHub Release is the same file; compare it with the OCI digest or with its `.sha256`.

Releases before this signing was added are not signed.

The SDK itself is not on GitHub Packages: Go modules come straight from the repository through the module proxy.
