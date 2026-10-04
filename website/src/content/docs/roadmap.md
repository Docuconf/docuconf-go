---
title: Roadmap
description: What is done, what is next, and what comes later.
---

## Now: the contract

- [x] Draft contract specification (`v1alpha1`)
- [x] CUE meta-schema with validation, policy, rendering and a test suite
- [x] Wire encodings for each language's list and duration formats
- [x] Config files and profiles (`appsettings`, Spring profiles, Rails YAML)
- [ ] Settle the open questions in the specification
- [ ] Language-neutral conformance suite

## Next: Go and the platform

- [ ] Go SDK rebuilt on caarlos0/env
- [ ] `docuconf` CLI: `export`, `vet`, `render`, `diff`, `docs`, `push`, `pull`
- [ ] Crossplane composition function, with readable conditions
- [ ] An end-to-end demo: one deploy succeeds; one with a bad value is rejected

## Then: more languages

- [ ] TypeScript on T3 Env
- [ ] Ruby on anyway_config
- [ ] .NET on the Options pattern
- [ ] Python on pydantic-settings
- [ ] Java on Spring Boot

## Later

- Platform-authored contracts for third-party images
- Roles, for one image that runs several processes
- Conditional requirements (`requiredIf`)
- A browser playground for contracts and values
- A neutral foundation home as the community grows

The detailed plan, with phases and risks, is in [PLAN.md](https://github.com/docuconf/docuconf-go/blob/main/docs/PLAN.md).
