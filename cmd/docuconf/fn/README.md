# function-docuconf

A Crossplane composition function that checks a workload's configuration
against the docuconf contract of the image it runs, before anything is
deployed. In a composition's pipeline, after the step that declares the
workload, it:

1. **pulls the contract** attached to the composite resource's image
   (`docuconf push`, [SPEC.md section 8](../../../spec/SPEC.md#8-distribution)),
   resolving a tag to its digest each time, or takes a contract committed
   beside the composition;
2. **checks the platform's values**, file sources and overlay values against
   it, with the environment's policy, as `docuconf vet -policy` does;
3. **sets the `ContractValid` condition** on the composite resource and its
   claim, with one line per bad input in `docuconf vet`'s words, and never a
   secret's value;
4. **renders the configuration into the workload**: the app container's
   `env`, which the function owns, the docuconf volumes and mounts (`dc-*`),
   the pod annotations and labels injectors need, the inline content's
   ConfigMaps as composed resources, and the image pinned by digest.

| The values | `ContractValid` | What happens |
|---|---|---|
| satisfy the contract and the policy | `True`, `Valid` | the configured workload is applied |
| break it | `False`, `Invalid`, one line per input | a fatal result: nothing is applied, and the running workload keeps its configuration |
| are not set yet (a `fromFieldPath` that does not exist) | `Unknown`, `Waiting` | a workload not created yet is held back, so it never starts half-configured; a running one keeps its configuration |
| come with an image that has no contract | `False`, `ContractUnavailable` | a fatal result |
| go to a container with `envFrom`, whose keys the contract cannot check | `False`, `TargetRejected` | a fatal result, unless `allowEnvFrom` |

It also turns off service links (`enableServiceLinks: false`), which would
otherwise inject `<SERVICE>_PORT` variables that collide with the app's, and
annotates the workload with the contract, the pinned image and a hash of the
rendered env, so drift is visible ([docs/EDGE_CASES.md](../../../docs/EDGE_CASES.md)).
What it rendered is also in the pipeline context, at `docuconf.dev/rendered`,
for later functions.

## Input

```yaml
apiVersion: docuconf.fn.crossplane.io/v1alpha1
kind: Input
image:
  fromFieldPath: spec.image          # or value: registry.example.com/orders-api:2.3.0
values:
  fromFieldPath: spec.config.values  # the values document, in the composite resource
  set:                               # entries the composition fixes, over the composite's
    LOG_LEVEL: warn
files:
  fromFieldPath: spec.config.files
overlays:
  fromFieldPath: spec.config.overlays
policy: |                            # the environment's policy, CUE
  secretReferences: [string]: secretKeyRef?: name: =~"^orders-"
target:
  resource: deployment               # the desired composed resource an earlier step declares
  container: app                     # the first container by default
```

`contract` takes the contract itself (CUE or JSON) instead of pulling it, for
platforms that commit it beside the composition, or `oci://IMAGE`. The
documents use the vocabulary of `values.yaml` and `files.yaml` ([the
README](../../../README.md#8-deploy)). The input's schema is
[package/input](package/input).

[example/](example) is a complete composition: function-go-templating
declares the Deployment, and function-docuconf configures it. Its test,
`example_test.go`, runs the function with the example's own input and App.

## Build

The function shares the CLI's validation, rendering and OCI code, so its
image is built with `cmd/docuconf` as the context, from the repository root:

```sh
docker build -f cmd/docuconf/fn/Dockerfile -t function-docuconf cmd/docuconf
crossplane xpkg build -f cmd/docuconf/fn/package --embed-runtime-image=function-docuconf -o function-docuconf.xpkg
crossplane xpkg push -f function-docuconf.xpkg ghcr.io/<you>/function-docuconf:v0.1.0
```

## Secrets

The function checks that each Secret the values and file sources name
exists in the composite resource's namespace (its claim's, for a claim),
with the key they name, and adds a line for each that does not:
`DATABASE_URL: Secret orders-db has no key url`. It reads only the Secrets'
keys, never their data. A Secret the composition itself creates, or an
ExternalSecret that makes it, is still syncing rather than missing, and the
function waits for it. It asks Crossplane for the Secrets as required
resources, which Crossplane v2 supplies; with an older one it warns and
skips the check. `checkSecrets: false` turns it off.

## Private registries

To pull contracts from a private registry, give the step a credential named
`registry`: a Secret of type `kubernetes.io/dockerconfigjson`, as for an
image pull secret.

```yaml
    - step: configuration
      functionRef:
        name: function-docuconf
      credentials:
        - name: registry
          source: Secret
          secretRef:
            namespace: crossplane-system
            name: registry-pull
      input: ...
```

The function writes it where only that request reads it, and removes it
after.
