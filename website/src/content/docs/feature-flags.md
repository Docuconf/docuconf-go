---
title: Config is not feature flags
description: Why docuconf deliberately leaves feature flags out.
---

Feature flags and environment config look alike, since both are often booleans. They differ in every way that matters:

| | Environment config (docuconf) | Feature flags |
|---|---|---|
| Changes | With a rollout. Fixed for the life of a pod. | At runtime, with no deploy. |
| Scope | Per deployment. | Per request, user, tenant or percentage. |
| Owner | App and platform engineers. | Product, often through a UI. |
| Validated | Before deploy, and at boot. | At evaluation, with a fallback value. |
| Audit trail | Git history. | The flag service's audit log. |

**The rule:** a variable belongs in a docuconf contract if, and only if, changing it requires a new rollout.

For flags, use [OpenFeature](https://openfeature.dev) with the provider of your choice. The provider's own connection settings, such as `FLAGD_HOST` or an SDK key, *are* environment config, and belong in the contract.

SDKs warn when a variable is named like a flag (`FEATURE_*`, `FF_*`, `ENABLE_*`). The warning is only a hint, since some kill switches are deliberately deploy-time.

A separate, optional contract for flag keys and types may come later. It would be its own file with its own lifecycle.
