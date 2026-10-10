# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately, through GitHub's private vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**
([direct link](https://github.com/docuconf/docuconf-go/security/advisories/new)). Do not open a public issue, pull
request or discussion for a suspected vulnerability.

Include what you can of:

- the affected component and version (CLI version or image digest, SDK module version, chart version);
- what an attacker can do, and what they need first;
- steps or a minimal contract, values file or program that reproduces it.

We work on the fix in a private security advisory, credit you in it unless you prefer otherwise, and publish the
advisory when a fixed release is out.

## Response targets

| | |
|---|---|
| Acknowledge the report | within 3 business days |
| First assessment (confirmed or not, severity) | as soon as we can reproduce it, and we keep you updated in the advisory |
| Fix | released as a patch to the supported version, then the advisory is published |

## Supported versions

Each artifact is released separately (see [RELEASING.md](RELEASING.md)). Security fixes go to the latest minor release
of each, as a new patch release:

| Artifact | Tag | Supported |
|---|---|---|
| CLI (`cmd/docuconf`, binaries and `ghcr.io/docuconf/docuconf`) | `cmd/docuconf/v*` | latest minor |
| Go SDK (`github.com/docuconf/docuconf-go`) | `v*` | latest minor |
| Helm library chart (`oci://ghcr.io/docuconf/charts/docuconf`) | `chart-v*` | latest minor |

**During the beta, only the latest release of each artifact is supported.** Upgrade to it to get a fix.

## Scope

In scope:

- the `docuconf` CLI, including its container image;
- the Go SDK, `github.com/docuconf/docuconf-go`;
- the Helm library chart in [`helm/docuconf`](helm/docuconf);
- the CUE meta-schema in [`spec/cue`](spec/cue), for example a contract that passes validation but should not, or
  rendering that leaks a value marked `secret`.

Out of scope: the example applications under [`examples`](examples), vulnerabilities in dependencies that docuconf does
not make reachable (report those upstream), and issues in a platform or cluster that only arise from its own
misconfiguration. Other language SDKs live in their own repositories and follow their own policies.

Release artifacts are signed; [RELEASING.md](RELEASING.md#verifying-a-release) shows how to verify them.
