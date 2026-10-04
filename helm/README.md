# docuconf for Helm

A Helm library chart that renders an app's docuconf inputs, plus a
`values.schema.json` generated from the app's contract. Together they give a
Helm-based platform the same guarantees as the CUE/Crossplane path:

| Check | Where it runs |
| --- | --- |
| Types, ranges, patterns, enum values, URL schemes, list bounds | `values.schema.json`, on every `helm lint`, `template`, `install` and `upgrade` |
| Required inputs, unknown names (typos) | `values.schema.json` |
| Secrets given only as `secretKeyRef`, never as literals | `values.schema.json` |
| Structured config-file content against the file's own JSON Schema | `values.schema.json` |
| Wire encodings (lists, durations), `$` escaping, file projections, content-hashed ConfigMaps | the library chart's templates, identical to the CUE renderer |
| Duration bounds, certificate details resolved from the cluster, environment policy | `docuconf vet`, in CI before `helm upgrade` |

## Use it in a chart

1. Add the library as a dependency, named `docuconf`:

   ```yaml
   # Chart.yaml
   dependencies:
     - name: docuconf
       version: 0.1.0
       repository: file://path/to/helm/docuconf   # not yet published to a chart registry
   ```

2. Generate the chart's contract and values schema from the app's
   `contract.cue` (the file the app's SDK exports). Re-run it whenever the
   contract changes, and commit the output:

   ```sh
   docuconf helm -contract contract.cue -chart .
   # wrote files/docuconf/contract.json
   # wrote values.schema.json
   ```

3. Put the inputs under `docuconf` in `values.yaml`:

   ```yaml
   docuconf:
     values:                          # environment variables, as typed values
       LOG_LEVEL: warn
       RATE_LIMITS: {perMinute: 600, burst: 50}
       POD_NAMESPACE: {fieldRef: {fieldPath: metadata.namespace}}
       GOMEMLIMIT: {resourceFieldRef: {resource: limits.memory}}
       PARTNER_KEYSTORE_PASSWORD: {secretKeyRef: {name: partner-keystore, key: password}}
     files:                           # where each file input comes from
       serving-tls: {certificate: {name: gateway-tls, secretName: gateway-tls}}
       routes:
         inline:                      # structured content, checked against the file's schema
           routes: [{match: /billing, upstream: "http://billing-api.billing.svc:8080"}]
       upstream-ca: {configMap: {name: internal-ca-bundle, key: ca.crt}}
       partner-keystore: {csi: {secretProviderClass: partner-keystore}}
   ```

4. Include the helpers in the Deployment:

   ```yaml
   metadata:
     annotations:
       {{- include "docuconf.reloaderAnnotations" . | trim | nindent 4 }}
   spec:
     template:
       spec:
         containers:
           - name: app
             env:
               {{- include "docuconf.env" . | trim | nindent 12 }}
             volumeMounts:
               {{- include "docuconf.volumeMounts" . | trim | nindent 12 }}
         volumes:
           {{- include "docuconf.volumes" . | trim | nindent 8 }}
   ```

   and render the ConfigMaps for inline content in their own template:

   ```yaml
   {{- include "docuconf.configMaps" . }}
   ```

[`examples/helm/gateway`](../examples/helm/gateway) is a complete chart that
uses every input kind.

## What a bad value looks like

```text
$ helm install gw . --set docuconf.values.LOG_LEVEL=verbose
Error: values don't meet the specifications of the schema(s) in the following chart(s):
gateway:
- at '/docuconf/values/LOG_LEVEL': 'anyOf' failed
  - at '/docuconf/values/LOG_LEVEL': value must be one of 'debug', 'info', 'warn', 'error'
  - at '/docuconf/values/LOG_LEVEL': got string, want object

$ helm install gw . --set docuconf.values.LOG_LEVL=warn
- at '/docuconf/values': additional properties 'LOG_LEVL' not allowed
```

(The second `anyOf` branch is the `configMapKeyRef` form a variable may take
instead.) A secret written as a literal fails with "got string, want
object", and Helm does not print the value.

## Things to know

- **Switching a file's source in an overlay.** Helm merges an overlay into the
  chart's defaults, so changing `routes` from `inline` to `configMap` in a
  `-f prod.yaml` leaves both keys and the schema rejects it. Set the old key
  to `null`:

  ```yaml
  docuconf:
    files:
      routes:
        inline: null
        configMap: {name: routes, key: routes.json}
  ```

- **`global`.** Helm copies `global` into the values of every dependency, and
  the library chart is a dependency named `docuconf`, so the schema allows a
  `docuconf.global` key and the helpers ignore it.

- **Profiles.** A variable the contract's default profile supplies is
  optional in the schema; every other required variable must be set, because
  Helm cannot know which profile a release selects.

- **Numbers.** Helm reads YAML numbers as float64; the helpers print integers
  without an exponent, so `perMinute: 600` renders as `600`, not `6e+02`.

- **Restarts.** `docuconf.reloaderAnnotations` emits
  [Reloader](https://github.com/stakater/Reloader) annotations for inputs
  declared `reload: restart` that come from Secrets or ConfigMaps. Inline
  content needs no annotation: its ConfigMap name carries a content hash, so
  a change rolls the pods on its own.

## Tests

`helm/test.sh` renders the example chart with the CUE example's inputs and
checks that env, volumes, mounts, ConfigMaps and restart triggers equal the
CUE renderer's golden output, then checks Helm rejects a set of bad values.
It runs against every Helm binary listed in `$HELM`:

```sh
HELM="helm3 helm4" helm/test.sh
```
