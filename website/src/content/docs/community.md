---
title: Get involved
description: How to help shape docuconf.
---

docuconf is at the stage where a single good comment can change the design. The most useful things you can do right now:

- **Review the specification.** Read [SPEC.md](https://github.com/docuconf/docuconf-go/blob/main/spec/SPEC.md) and open an issue on anything unclear, wrong or missing. Its open questions section lists what is still undecided.
- **Bring your edge cases.** If your platform has bitten you with environment config in a way not yet in [EDGE_CASES.md](https://github.com/docuconf/docuconf-go/blob/main/docs/EDGE_CASES.md), tell us.
- **Lead an SDK.** Each language SDK should be led by someone who lives in that ecosystem. If you know T3 Env, anyway_config, .NET Options or pydantic-settings well, we would like to hear from you.
- **Try the schema.** `spec/cue/test.sh` runs the CUE meta-schema against valid and invalid examples. Add a case that should fail and does not.

## Where things live

Everything is under the [docuconf GitHub organization](https://github.com/docuconf). Today the specification, the Go SDK and this website share one repository. As the project grows, they will split into a repository for the specification, one per language SDK, and one for the platform tooling.

## Licence

The licence is being finalized. Apache-2.0 is proposed, as is common for Kubernetes-adjacent projects.
