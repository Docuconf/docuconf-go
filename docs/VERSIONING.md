# Versioning and deprecation

docuconf has two kinds of version: the **spec version**, which is a contract's `apiVersion`, and the **release
version** of each artifact (SDKs, CLI, Helm chart), which is semver. This page says what each one promises, how they
relate, and how things are removed.

## Spec versions

A contract states the spec version it is written against:

```cue
apiVersion: "docuconf.dev/v1alpha1"
```

The spec moves through three stages, as [SPEC section 9](../spec/SPEC.md#9-compatibility) says:

| Stage | `apiVersion` | Promise |
|---|---|---|
| Alpha | `docuconf.dev/v1alpha1` | Fields, types and rules may change in any release. |
| Beta | `docuconf.dev/v1beta1` | Additive only. Nothing is removed or renamed, and no rule gets stricter. |
| Stable | `docuconf.dev/v1` | Stable. Breaking changes need a new major `apiVersion` (`v2`). |

The docs model (`docs.docuconf.dev/...`, SPEC section 14) is versioned the same way.

### What beta means for a contract

From `v1beta1` on, for the life of `v1beta1`:

- **A contract that validates keeps validating.** A spec release does not reject a contract, or a values file, that
  the previous one accepted, and an accepted contract keeps its meaning.
- **New fields are optional.** A field added to the meta-schema has a default that keeps today's behaviour, so leaving
  it out never changes how an existing contract renders or checks.
- **New things are additions.** A new type, format or source kind can be added. A tool that does not know it reports
  an unknown value instead of guessing, and the conformance suite marks such cases with a capability tag.
- **Removals wait for the next stage.** A field can be deprecated during beta (see below), but it is only removed in
  `v1`.

"Additive only" is about the contract format. It does not stop a tool from adding warnings, or from fixing a bug where
it accepted something the spec already forbade.

## Release versions

Every artifact follows [semver](https://semver.org/). [RELEASING.md](../RELEASING.md) lists the tags.

- **SDKs stay `0.x` during beta.** An SDK reaches 1.0 only once the spec is at `v1`. Within `0.x`, a minor release
  (`0.3.0` to `0.4.0`) may change the SDK's own API, following the deprecation rule below; a patch release does not.
- **Each SDK README states the spec version it implements**, for example "implements spec `v1beta1`". That is the
  newest `apiVersion` it reads and the one its export writes. An SDK that implements `v1beta1` also reads `v1alpha1`
  contracts for as long as the migration below allows.
- **The CLI and the Helm chart** say in their release notes which spec version they validate. The CLI, the chart and
  the meta-schema in [`spec/cue`](../spec/cue) move together: a release of each supports the same spec versions.
- The spec version and the release versions are independent numbers. A spec change ships in new releases of the
  tools; it does not reset or align their version numbers.

## Deprecation

Deprecation applies to two things, with the same rule:

- a **contract field** in the meta-schema (for example a field of a variable or a file input);
- a **public SDK API** (a function, type, option or struct tag) or a CLI flag or command.

The rule:

1. **Mark it deprecated for at least one minor release before removal.** The SPEC, the meta-schema comments and the
   SDK's API docs (in Go, a `// Deprecated:` comment) say so and name the replacement. The release notes list it.
2. **Warn when it is used.** Tools that read a contract warn when it uses a deprecated field (`docuconf vet`, `diff`
   and the SDKs at export or boot). An SDK API warns through the language's own mechanism (in Go, `staticcheck` and
   editors pick up `// Deprecated:`). A deprecated CLI flag prints a warning on stderr.
3. **Remove it only at the next spec stage.** A deprecated contract field stays valid for the whole of its stage and
   is removed only by the next one (deprecated in `v1beta1`, removed in `v1`). An SDK API deprecated during beta is
   removed no earlier than the SDK release that implements the next spec stage, and never in a patch release.

This is about the docuconf format and tools. A contract's own `deprecated` key, which marks one of *your* variables
or files as deprecated (SPEC section 4.2), is a separate feature for contract authors; how to retire one of your own
variables safely is in SPEC section 9.

## Migrating from `v1alpha1` to `v1beta1`

For a `v1alpha1` contract, **the only change is `apiVersion`**:

```diff
-apiVersion: "docuconf.dev/v1alpha1"
+apiVersion: "docuconf.dev/v1beta1"
```

Nothing else in the contract, the values files or the file sources has to change. A contract exported by an SDK
gets the new `apiVersion` when that SDK is upgraded to a release that implements `v1beta1`; a hand-written contract is
edited by hand.

This is provisional: the PR that freezes `v1beta1` will confirm it, and will list any other change here if the freeze
needs one. Until then, keep contracts on `v1alpha1`.
