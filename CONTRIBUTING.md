# Contributing to docuconf-go

## Commit messages and pull request titles

Pull requests are squash-merged, so each PR title becomes one commit message on `main`. Titles follow [Conventional
Commits](https://www.conventionalcommits.org/en/v1.0.0/), and the `pr-title` check enforces it:

```
<type>(<optional scope>): <summary>
```

For example `feat: add a duration type`, `fix(export): escape quotes in descriptions` or `docs: explain
contract-first mode`.

| Type | Use it for | Release | Changelog section |
|---|---|---|---|
| `feat` | a new feature | minor | Features |
| `fix` | a bug fix | patch | Bug Fixes |
| `perf` | a performance improvement | patch | Performance Improvements |
| `refactor` | a code change that is neither a fix nor a feature | patch | Code Refactoring |
| `docs` | documentation only | patch | Documentation |
| `revert` | reverting an earlier change | patch | Reverts |
| `build`, `ci`, `test`, `chore` | build, CI, tests and housekeeping | none | hidden |

A breaking change adds `!` after the type (`feat!: rename Export to ToCue`) or a `BREAKING CHANGE:` footer in the
PR description. It bumps the major version from 1.0 on; while the version is below 1.0, it bumps the minor version.
Commits on their own branches can say anything: only the PR title reaches `main`.

## How releases happen

Releases are automated with [release-please](https://github.com/googleapis/release-please).

1. Every push to `main` updates one open release PR, titled `chore: release main`. It bumps the version from the
   commit types above, updates `docuconf.Version` in `export.go` (written into exported contracts as
   `metadata.generator.version`), and adds the new entries to `CHANGELOG.md`.
2. A maintainer ships a release by merging the release PR. Nothing is released until then, and the PR can wait
   while more changes land: it updates itself.
3. Merging it tags the commit `vX.Y.Z` and creates the GitHub release with the changelog entries. There are two
   release components: the SDK module at the repository root (tags `vX.Y.Z`, changelog `CHANGELOG.md`) and the CLI
   module in `cmd/docuconf` (tags `cmd/docuconf/vX.Y.Z`, the form Go requires for a nested module, changelog
   `cmd/docuconf/CHANGELOG.md`). Each is released only when commits touch it. The Go module proxy serves a tagged
   version without any publish step: `go get github.com/docuconf/docuconf-go@vX.Y.Z` and `go install
   github.com/docuconf/docuconf-go/cmd/docuconf@vX.Y.Z`.

The release workflow is `.github/workflows/release-please.yml`; its configuration is `release-please-config.json` and
`.release-please-manifest.json`.

## Downstream SDKs

This repository owns the spec, the CUE meta-schema (`spec/cue`), the conformance suite (`conformance/cases.json`)
and the `docuconf` CLI. Every SDK repository (Docuconf/docuconf-js, -dotnet, -python, -ruby, -java, -kotlin,
-rust, -swift, -elixir, -gleam, -cpp, -php, -cobol) is tested against them, in three ways.

- **The gate.** A pull request that touches `spec/`, `conformance/`, `cmd/docuconf/`, the Go library (`*.go`,
  `internal/`, `go.mod`, `go.sum`) runs `.github/workflows/downstream.yml`: one job per SDK, which checks out
  the SDK's `main` and runs its `scripts/conformance.sh` with `DOCUCONF_GO_DIR` set to this pull request's
  files. A red job means the change breaks that SDK: fix the SDK first, or change the spec deliberately and
  follow up in the SDK. While some SDKs don't have the script yet, they are skipped with a warning; once all
  have it, set `REQUIRE_SCRIPT: "1"` at the top of the workflow.
- **The dispatch.** A push to `main` that touches the same paths runs `.github/workflows/notify-sdks.yml`,
  which sends a `docuconf-go-updated` repository dispatch, with the commit SHA, to every SDK.
- **The bump PRs.** Each SDK pins the docuconf-go commit it is tested against in `.github/docuconf-go.ref`
  (COBOL also in `go.mod`). Its `docuconf-go-bump` workflow, on the dispatch or daily, opens or updates a
  `build(deps): bump docuconf-go to <sha>` pull request on the `docuconf-go-bump` branch; the SDK's CI on
  that pull request is the compatibility check. Each SDK's CI also runs nightly against docuconf-go `main`.

**The release GitHub App.** The `RELEASE_APP_ID` and `RELEASE_APP_PRIVATE_KEY` secrets (here and in each SDK)
hold a GitHub App installed on the Docuconf organization's SDK repositories, with Contents and Pull requests
read and write, and Actions read and write. It is needed for:

- the dispatch: `GITHUB_TOKEN` cannot reach other repositories. Without the App, `notify-sdks` only leaves a
  notice, and the SDKs pick the change up with their daily bump run instead;
- private SDK repositories: without the App, the gate can only check out public ones, and fails with a clear
  message for a private one;
- CI on the bump PRs: a pull request opened with `GITHUB_TOKEN` triggers no workflows, so without the App the
  bump workflow starts the SDK's CI on the bump branch itself (`gh workflow run`). Either way each SDK must
  allow it under Settings, Actions, General: "Allow GitHub Actions to create and approve pull requests".
