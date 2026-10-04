---
title: Vision
description: Why docuconf exists, what it believes, and where it is going.
---

## Environment variables are an API

Every application has an interface to the platform that runs it, and for most applications that interface is a list of environment variables. It is a real API, with inputs, types, required fields and failure modes. It is just almost never treated like one.

Each variable is a string that some line of code, somewhere, parses. Which variables exist, which are required, and what a valid value looks like lives in a README if you are lucky, and in someone's memory if you are not. The first real validation happens when the process starts and crashes.

This was tolerable when one team owned one app on one server. It does not hold up in a modern organization:

- **Platform teams run hundreds of services they did not write.** They need to know what each one requires without reading its source.
- **Organizations are polyglot.** A Rails monolith, TypeScript frontends, .NET services and Go infrastructure all deploy to the same clusters, each with its own config conventions.
- **Deploys are declarative.** With GitOps and Crossplane, configuration is data in a repository, which means it can be checked before it is applied, if there is something to check it against.

## What docuconf is

docuconf gives that API a **contract**.

An application declares its variables once, in its own language, using the env library it already uses. docuconf exports that declaration as a CUE document: the contract. The platform checks every deploy against the contract, so a missing, mistyped or out-of-range value stops the rollout, not the pod. At boot, the same declaration checks the real environment and gives the code typed values.

CUE is the contract language because it was built for exactly this: it combines types, constraints and values in one model, and lets independent sources of rules (the app's contract, the platform's policy) be checked together without either editing the other.

## What we believe

1. **Meet developers where they are.** Every ecosystem already has an env library that teams trust. docuconf extends it. A Rails developer should never have to learn CUE.
2. **The contract travels with the build.** A contract describes one specific build of an app, so it is published alongside the image and tied to its digest. The platform never checks image B against contract A.
3. **The app and the platform each own their part.** The contract says what the app can accept. Platform policy says what an environment allows. A deploy must satisfy both.
4. **Fail early, explain clearly.** Each problem gets one line that names the variable and the rule, in terms a person can act on.
5. **Secrets are references.** A contract never holds a secret. The platform supplies secrets only as references, and nothing docuconf prints ever includes one.
6. **Configuration is not feature flags.** Values that change without a rollout belong in a flag system. [More on that](../feature-flags/).
7. **Open and neutral.** docuconf is a specification first, with SDKs that anyone can implement and a conformance suite that proves they agree.

## Where it is going

The near-term goal is concrete: SDKs for Go, TypeScript, Ruby and .NET, a CLI, and a Crossplane composition function, so a team can go from "add a package" to "bad config is rejected before deploy" in an afternoon.

Longer term, we want a contract to be as normal a part of a container image as an SBOM:

- **Every image carries its contract.** Platforms can read what any image needs, including third-party images, through a shared catalogue of platform-authored contracts.
- **Every platform can check it.** Crossplane first, then other routes such as admission policy, Helm and Argo CD.
- **Tools build on it.** Generated documentation, IDE completion for environment files, and compatibility checks that flag a breaking config change in code review.

We intend docuconf to be governed in the open, under its own GitHub organization, with a path toward a neutral foundation as the community grows.
