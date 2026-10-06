{{/*
docuconf library chart.

An app chart ships its contract as files/docuconf/contract.json (written
by `docuconf helm -contract contract.cue -chart .`) and keeps its inputs under `docuconf` in
values.yaml:

  docuconf:
    values:            # environment variables, as typed values
      PORT: 9090
      DATABASE_URL: {secretKeyRef: {name: db, key: url}}
    files:             # where each file input comes from
      serving-tls: {certificate: {name: api-tls, secretName: api-tls}}

These helpers render them exactly as the docuconf CUE renderer does
(SPEC §5 and §4.6): each variable in its wire encoding with `$` escaped,
secrets and other references as valueFrom, file inputs as volumes
projected with items (never subPath), and inline content as immutable,
content-hashed ConfigMaps.

  env:          {{ include "docuconf.env" . | trim | nindent 12 }}
  volumeMounts: {{ include "docuconf.volumeMounts" . | trim | nindent 12 }}
  volumes:      {{ include "docuconf.volumes" . | trim | nindent 8 }}
  ConfigMaps:   {{ include "docuconf.configMaps" . }}   (in their own template)
  annotations:  {{ include "docuconf.reloaderAnnotations" . | trim | nindent 4 }}
                (empty when no input restarts the pod; wrap it in `with`)

Values are checked by the chart's values.schema.json, generated from the
same contract; these helpers also refuse names the contract does not declare.
*/}}

{{- define "docuconf.contract" -}}
{{- $raw := .Files.Get "files/docuconf/contract.json" -}}
{{- if not $raw -}}
{{- fail "docuconf: files/docuconf/contract.json is missing; run `docuconf helm -contract contract.cue -chart .`" -}}
{{- end -}}
{{- $raw -}}
{{- end -}}

{{/*
The inputs, with nulls removed. Overlays switch a source with `old: null`
(Helm cannot replace a map with another), and Helm keeps those nulls in
.Values, so a null value or source key means "not set". Data is left
alone: inline file content and json values may hold nulls of their own.
*/}}
{{- define "docuconf.inputs" -}}
{{- $in := default dict .Values.docuconf -}}
{{- $out := dict -}}
{{- range $section := list "values" "files" "overlays" -}}
{{- $clean := dict -}}
{{- range $k, $v := (get $in $section | default dict) -}}
{{- if not (kindIs "invalid" $v) -}}
{{- $isSource := and (kindIs "map" $v) (or (eq $section "files") (eq $section "overlays") (gt (len (pick $v "secretKeyRef" "configMapKeyRef" "fieldRef" "resourceFieldRef" "injected")) 0)) -}}
{{- if $isSource -}}
{{- $m := dict -}}
{{- range $k2, $v2 := $v -}}
{{- if not (kindIs "invalid" $v2) -}}
{{- $_ := set $m $k2 $v2 -}}
{{- end -}}
{{- end -}}
{{- $_ := set $clean $k $m -}}
{{- else -}}
{{- $_ := set $clean $k $v -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $_ := set $out $section $clean -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* Kubernetes expands $(VAR) in env values and reduces $$ to $. */}}
{{- define "docuconf.escape" -}}
{{- replace "$" "$$" . -}}
{{- end -}}

{{- define "docuconf.isRef" -}}
{{- $v := . -}}
{{- if and (kindIs "map" $v) (eq (len $v) 1) (or (hasKey $v "secretKeyRef") (hasKey $v "configMapKeyRef") (hasKey $v "fieldRef") (hasKey $v "resourceFieldRef")) -}}
true
{{- end -}}
{{- end -}}

{{/* One list item or scalar as a string. Helm reads numbers as float64, so ints are printed with %d. */}}
{{- define "docuconf.scalar" -}}
{{- $type := .type -}}
{{- $v := .value -}}
{{- if eq $type "int" -}}
{{- printf "%d" (int64 $v) -}}
{{- else if eq $type "bool" -}}
{{- ternary "true" "false" $v -}}
{{- else if eq $type "json" -}}
{{- toJson $v -}}
{{- else -}}
{{- toString $v -}}
{{- end -}}
{{- end -}}

{{/* A Go-syntax duration ("1m30s") in the app's encoding (SPEC §5). */}}
{{- define "docuconf.duration" -}}
{{- $in := .in -}}
{{- if eq .encoding "go" -}}
{{- $in -}}
{{- else -}}
{{- $ms := 0 -}}
{{- range $part := regexFindAll "[0-9]+(ns|us|ms|s|m|h)" $in -1 -}}
{{- $n := regexFind "^[0-9]+" $part -}}
{{- $unit := trimPrefix $n $part -}}
{{- $n = int64 $n -}}
{{- if eq $unit "h" }}{{ $ms = add $ms (mul $n 3600000) }}{{ end -}}
{{- if eq $unit "m" }}{{ $ms = add $ms (mul $n 60000) }}{{ end -}}
{{- if eq $unit "s" }}{{ $ms = add $ms (mul $n 1000) }}{{ end -}}
{{- if eq $unit "ms" }}{{ $ms = add $ms $n }}{{ end -}}
{{- end -}}
{{- $secs := div $ms 1000 -}}
{{- $frac := mod $ms 1000 -}}
{{- $fracStr := ternary "" (printf ".%s" (regexReplaceAll "0+$" (printf "%03d" $frac) "")) (eq $frac 0) -}}
{{- if eq .encoding "seconds" -}}
{{- printf "%d%s" $secs $fracStr -}}
{{- else if eq .encoding "iso8601" -}}
{{- printf "PT%d%sS" $secs $fracStr -}}
{{- else if eq .encoding "timespan" -}}
{{- $days := div $secs 86400 -}}
{{- if gt $days 0 }}{{ printf "%d." $days }}{{ end -}}
{{- printf "%02d:%02d:%02d%s" (div (mod $secs 86400) 3600) (div (mod $secs 3600) 60) (mod $secs 60) $fracStr -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* File inputs with their sources, as a JSON list. */}}
{{- define "docuconf.fileSpecs" -}}
{{- $root := . -}}
{{- $c := include "docuconf.contract" . | fromJson -}}
{{- $sources := (include "docuconf.inputs" . | fromJson).files | default dict -}}
{{- $declared := $c.files | default dict -}}
{{- range $name := keys $sources -}}
{{- if not (hasKey $declared $name) -}}
{{- fail (printf "docuconf: file input %q is not in the %s contract" $name $c.metadata.name) -}}
{{- end -}}
{{- end -}}
{{- $out := list -}}
{{- range $name := keys $declared | sortAlpha -}}
{{- $f := get $declared $name -}}
{{- if hasKey $sources $name -}}
{{- $s := get $sources $name -}}
{{- $isDir := eq $f.type "tls" -}}
{{- $e := dict "name" $name "vol" (printf "dc-%s" $name) "path" $f.path "pathEnv" ($f.pathEnv | default "") "reload" ($f.reload | default "restart") "isDir" $isDir "source" $s -}}
{{- $_ := set $e "mountPath" (ternary $f.path (dir $f.path) $isDir) -}}
{{- $_ := set $e "fileName" (base $f.path) -}}
{{- $_ := set $e "mode" (ternary 256 292 (default false $f.secret)) -}}
{{- if hasKey $s "inline" -}}
{{- $content := "" -}}
{{- if kindIs "string" $s.inline -}}
{{- $content = $s.inline -}}
{{- else if eq ($f.format | default "") "json" -}}
{{- $content = toJson $s.inline -}}
{{- else if eq ($f.format | default "") "yaml" -}}
{{- $content = printf "%s\n" (toYaml $s.inline) -}}
{{- else -}}
{{- fail (printf "docuconf: give %s's inline content as a string (structured content needs a json or yaml config file)" $name) -}}
{{- end -}}
{{- $_ := set $e "content" $content -}}
{{- $_ := set $e "configMap" (printf "%s-%s-%s" $root.Release.Name $name (sha256sum $content | trunc 10) | trunc 63 | trimSuffix "-") -}}
{{- end -}}
{{- $out = append $out $e -}}
{{- end -}}
{{- end -}}
{{- /*
Config-file overlays (SPEC §4.7): each value at its configKey, in native
types, in a file of the app's own format. watch: a stable, mutable
ConfigMap the kubelet updates in place. restart: content-hashed.
*/ -}}
{{- $ovals := (include "docuconf.inputs" . | fromJson).overlays | default dict -}}
{{- $odecl := $c.overlays | default dict -}}
{{- range $o := keys $ovals -}}
{{- if not (hasKey $odecl $o) -}}
{{- fail (printf "docuconf: overlay %q is not in the %s contract" $o $c.metadata.name) -}}
{{- end -}}
{{- end -}}
{{- range $o := keys $odecl | sortAlpha -}}
{{- if hasKey $ovals $o -}}
{{- $ov := get $odecl $o -}}
{{- $data := dict -}}
{{- range $n, $x := get $ovals $o -}}
{{- if not (hasKey $c.vars $n) -}}
{{- fail (printf "docuconf: %s (in overlay %s) is not in the %s contract" $n $o $c.metadata.name) -}}
{{- end -}}
{{- $var := get $c.vars $n -}}
{{- if $var.secret -}}
{{- fail (printf "docuconf: %s is secret, so it cannot go in overlay %s; supply it as a secretKeyRef or injected" $n $o) -}}
{{- end -}}
{{- if not $var.configKey -}}
{{- fail (printf "docuconf: %s has no configKey, so overlay %s has nowhere to put it" $n $o) -}}
{{- end -}}
{{- $native := $x -}}
{{- if eq $var.type "duration" -}}
{{- $native = include "docuconf.duration" (dict "in" (toString $x) "encoding" ($var.encoding | default "go")) -}}
{{- else if eq $var.type "int" -}}
{{- $native = int64 $x -}}
{{- else if and (eq $var.type "list") (eq ($var.items | default "string") "int") -}}
{{- $l := list -}}
{{- range $i := $x }}{{ $l = append $l (int64 $i) }}{{ end -}}
{{- $native = $l -}}
{{- end -}}
{{- $parts := splitList $ov.keySeparator $var.configKey -}}
{{- $cur := $data -}}
{{- range $p := initial $parts -}}
{{- if not (hasKey $cur $p) }}{{ $_ := set $cur $p dict }}{{ end -}}
{{- $cur = get $cur $p -}}
{{- end -}}
{{- $_ := set $cur (last $parts) $native -}}
{{- end -}}
{{- $content := "" -}}
{{- if eq $ov.format "json" -}}
{{- $content = printf "%s\n" (toPrettyJson $data) -}}
{{- else if eq $ov.format "yaml" -}}
{{- $content = printf "%s\n" (toYaml $data) -}}
{{- else -}}
{{- $content = toToml $data -}}
{{- end -}}
{{- $watch := eq ($ov.reload | default "restart") "watch" -}}
{{- $cm := printf "%s-overlay-%s" $root.Release.Name $o -}}
{{- if not $watch }}{{ $cm = printf "%s-%s" $cm (sha256sum $content | trunc 10) }}{{ end -}}
{{- $e := dict "name" $o "vol" (printf "dc-overlay-%s" $o) "path" $ov.path "pathEnv" "" "reload" ($ov.reload | default "restart") "isDir" false "source" (dict "overlay" $o) -}}
{{- $_ := set $e "mountPath" (dir $ov.path) -}}
{{- $_ := set $e "fileName" (base $ov.path) -}}
{{- $_ := set $e "mode" 292 -}}
{{- $_ := set $e "content" $content -}}
{{- $_ := set $e "configMap" ($cm | trunc 63 | trimSuffix "-") -}}
{{- $_ := set $e "mutable" $watch -}}
{{- $out = append $out $e -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{- define "docuconf.env" -}}
{{- $c := include "docuconf.contract" . | fromJson -}}
{{- $values := (include "docuconf.inputs" . | fromJson).values | default dict -}}
{{- range $o, $m := (include "docuconf.inputs" . | fromJson).overlays | default dict -}}
{{- range $n := keys $m -}}
{{- if hasKey $values $n -}}
{{- fail (printf "docuconf: %s is set both in values and in overlay %s; set it in one place (the environment would win)" $n $o) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $name := keys $values -}}
{{- if not (hasKey $c.vars $name) -}}
{{- fail (printf "docuconf: %s is not in the %s contract" $name $c.metadata.name) -}}
{{- end -}}
{{- end -}}
{{- range $name := keys $c.vars | sortAlpha -}}
{{- $var := get $c.vars $name -}}
{{- if hasKey $values $name -}}
{{- $v := get $values $name -}}
{{- if and (kindIs "map" $v) (hasKey $v "injected") -}}
{{- /* Supplied at runtime (SPEC §4.5.1): the injector's reference, or nothing. */ -}}
{{- with $v.injected.ref }}
- name: {{ $name }}
  value: {{ include "docuconf.escape" . | quote }}
{{- end -}}
{{- else if include "docuconf.isRef" $v }}
- name: {{ $name }}
  valueFrom:
    {{- toYaml $v | nindent 4 }}
{{- else if $var.secret -}}
{{- fail (printf "docuconf: %s is secret; supply it as a secretKeyRef or injected, never as a value" $name) -}}
{{- else if eq $var.type "list" -}}
{{- $enc := $var.encoding | default "csv" -}}
{{- if eq $enc "indexed" -}}
{{- range $i, $x := $v }}
- name: {{ printf "%s__%d" $name $i }}
  value: {{ include "docuconf.escape" (include "docuconf.scalar" (dict "type" $var.items "value" $x)) | quote }}
{{- end -}}
{{- else if eq $enc "json" }}
- name: {{ $name }}
  value: {{ include "docuconf.escape" (toJson $v) | quote }}
{{- else -}}
{{- $parts := list -}}
{{- range $x := $v -}}
{{- $parts = append $parts (include "docuconf.scalar" (dict "type" $var.items "value" $x)) -}}
{{- end }}
- name: {{ $name }}
  value: {{ include "docuconf.escape" (join ($var.separator | default ",") $parts) | quote }}
{{- end -}}
{{- else if eq $var.type "duration" }}
- name: {{ $name }}
  value: {{ include "docuconf.duration" (dict "in" (toString $v) "encoding" ($var.encoding | default "go")) | quote }}
{{- else }}
- name: {{ $name }}
  value: {{ include "docuconf.escape" (include "docuconf.scalar" (dict "type" $var.type "value" $v)) | quote }}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $e := include "docuconf.fileSpecs" . | fromJsonArray -}}
{{- if $e.pathEnv }}
- name: {{ $e.pathEnv }}
  value: {{ include "docuconf.escape" $e.path | quote }}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "docuconf.volumeMounts" -}}
{{- range $e := include "docuconf.fileSpecs" . | fromJsonArray }}
{{- if not (hasKey $e.source "injected") }}
- name: {{ $e.vol }}
  mountPath: {{ $e.mountPath }}
  readOnly: true
{{- end }}
{{- end -}}
{{- end -}}

{{- define "docuconf.volumes" -}}
{{- range $e := include "docuconf.fileSpecs" . | fromJsonArray }}
{{- $s := $e.source }}
{{- if not (hasKey $s "injected") }}
- name: {{ $e.vol }}
{{- if hasKey $e "configMap" }}
  configMap:
    name: {{ $e.configMap }}
    defaultMode: {{ int64 $e.mode }}
    items:
      - key: {{ $e.fileName }}
        path: {{ $e.fileName }}
{{- else if hasKey $s "configMap" }}
  configMap:
    name: {{ $s.configMap.name }}
    defaultMode: {{ int64 $e.mode }}
    items:
      - key: {{ $s.configMap.key }}
        path: {{ $e.fileName }}
{{- else if hasKey $s "secret" }}
  secret:
    secretName: {{ $s.secret.name }}
    defaultMode: {{ int64 $e.mode }}
{{- if not $e.isDir }}
    items:
      - key: {{ $s.secret.key }}
        path: {{ $e.fileName }}
{{- end }}
{{- else if hasKey $s "certificate" }}
  secret:
    secretName: {{ $s.certificate.secretName }}
    defaultMode: {{ int64 $e.mode }}
{{- else if hasKey $s "csi" }}
  csi:
    driver: {{ $s.csi.driver | default "secrets-store.csi.k8s.io" }}
    readOnly: true
    volumeAttributes:
      secretProviderClass: {{ $s.csi.secretProviderClass }}
{{- else if hasKey $s "image" }}
  image:
    reference: {{ $s.image.reference }}
{{- with $s.image.pullPolicy }}
    pullPolicy: {{ . }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}
{{- end -}}

{{- define "docuconf.configMaps" -}}
{{- $c := include "docuconf.contract" . | fromJson -}}
{{- range $e := include "docuconf.fileSpecs" . | fromJsonArray }}
{{- if hasKey $e "configMap" }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $e.configMap }}
  labels:
    app.kubernetes.io/part-of: {{ $c.metadata.name }}
{{- if not $e.mutable }}
immutable: true
{{- end }}
data:
  {{ $e.fileName }}: {{ $e.content | quote }}
{{- end }}
{{- end -}}
{{- end -}}

{{/*
Annotations for stakater/Reloader, so pods roll when a referenced ConfigMap or
Secret changes for inputs the app only reads at startup (reload: restart).
Inline content needs none: it is content-hashed. Put on the Deployment's metadata.
*/}}
{{- define "docuconf.reloaderAnnotations" -}}
{{- $configMaps := list -}}
{{- $secrets := list -}}
{{- range $e := include "docuconf.fileSpecs" . | fromJsonArray -}}
{{- if eq $e.reload "restart" -}}
{{- $s := $e.source -}}
{{- if hasKey $s "configMap" }}{{ $configMaps = append $configMaps $s.configMap.name }}{{ end -}}
{{- if hasKey $s "secret" }}{{ $secrets = append $secrets $s.secret.name }}{{ end -}}
{{- if hasKey $s "certificate" }}{{ $secrets = append $secrets $s.certificate.secretName }}{{ end -}}
{{- end -}}
{{- end -}}
{{- with $configMaps }}
configmap.reloader.stakater.com/reload: {{ join "," (uniq .) | quote }}
{{- end }}
{{- with $secrets }}
secret.reloader.stakater.com/reload: {{ join "," (uniq .) | quote }}
{{- end }}
{{- end -}}
