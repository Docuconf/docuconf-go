# docuconf-go

The Go SDK for [docuconf](https://github.com/docuconf): typed environment configuration that exports a CUE contract, so a Kubernetes platform can reject bad configuration before it deploys.

```
go get github.com/docuconf/docuconf-go
```

> **Status:** early. This module is the original Go code generator, now moving to the `docuconf` org. It will be rebuilt on [caarlos0/env](https://github.com/caarlos0/env) to match the spec. Until v1, expect breaking changes. The module path changed from `github.com/autoscalerhq/docuconf` to `github.com/docuconf/docuconf-go`.

- [Contract specification](spec/SPEC.md), with its CUE meta-schema in [`spec/cue`](spec/cue)
- [Implementation plan](docs/PLAN.md)
- [Edge cases](docs/EDGE_CASES.md)

## Current features

- [x] Generate configuration structs
  - [x] Add string
  - [x] Add int
  - [x] Add bool
  - [x] Add float
- [x] Generate configuration documentation
  - [x] Generate markdown
- [x] Allow for separate services that can share some configuration and keep some separate
