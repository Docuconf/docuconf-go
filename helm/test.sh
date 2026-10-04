#!/usr/bin/env bash
# Tests the docuconf library chart through the gateway example chart:
#   1. regenerates the chart's contract and values schema from spec/cue;
#   2. renders the chart with the CUE example's exact inputs and checks the
#      output matches the CUE renderer's golden output (testdata/render);
#   3. checks Helm rejects bad values through the generated schema.
# Runs against every helm binary in $HELM (default: helm).
set -u
here="$(cd "$(dirname "$0")" && pwd)"
repo="$here/.."
chart="$repo/examples/helm/gateway"
spec="$repo/spec/cue"
CUE="${CUE:-cue}"
read -r -a helms <<<"${HELM:-helm}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

CUE="$CUE" "$chart/generate.sh" >/dev/null
# The CUE example's own inputs, in the chart's values layout.
# Rendered from a copy of the chart whose values.yaml is replaced, not
# overlaid: Helm merges overlays into defaults, so it could not replace the
# default's structured routes with the example's string.
cp -r "$chart" "$tmp/parity-chart"
{ echo "image: example.invalid/gateway:test"; echo "replicas: 1"
  (cd "$spec" && "$CUE" export examples/*.cue testdata/gen/*.cue \
    -e '{docuconf: {values: gatewayValues, files: gatewayFiles}}' --out yaml)
} >"$tmp/parity-chart/values.yaml"

# Each case: a values overlay, and text the failure must contain.
cases=(
  'docuconf: {values: {LOG_LEVEL: verbose}}|LOG_LEVEL'
  'docuconf: {values: {GOMEMLIMIT: {fieldRef: {fieldPath: metadata.name}}}}|GOMEMLIMIT'
  'docuconf: {values: {PARTNER_KEYSTORE_PASSWORD: hunter2}}|PARTNER_KEYSTORE_PASSWORD'
  'docuconf: {values: {LOG_LEVL: warn}}|LOG_LEVL'
  'docuconf: {values: {RATE_LIMITS: {perMinute: 0}}}|RATE_LIMITS'
  'docuconf: {files: {serving-tls: {inline: "-----BEGIN CERTIFICATE-----"}}}|serving-tls'
  'docuconf: {files: {routes: {inline: {routes: [{match: /a}]}}}}|routes'
  'docuconf: {files: {license: {inline: not-a-licence}}}|license'
  'docuconf: {files: {upstream-ca: {configMap: {key: null}}}}|upstream-ca'
  'docuconf: {files: {upstream-ca: {secret: {name: ca, key: ca.crt}}}}|upstream-ca'
)

for helm in "${helms[@]}"; do
  version=$("$helm" version --short 2>/dev/null)
  (cd "$chart" && "$helm" dependency build . >/dev/null 2>&1) || { echo "FAIL $version dependency build"; fail=1; continue; }
  rm -rf "$tmp/parity-chart/charts" && cp -r "$chart/charts" "$tmp/parity-chart/charts"

  if out=$("$helm" lint "$chart" 2>&1); then echo "PASS $version lint"; else echo "FAIL $version lint"; echo "$out"; fail=1; fi

  if "$helm" template gateway "$tmp/parity-chart" >"$tmp/rendered.yaml" 2>"$tmp/err" \
    && python3 "$here/testdata/compare.py" "$tmp/rendered.yaml" "$spec/testdata/render/gatewayOut.yaml" >"$tmp/cmp"; then
    echo "PASS $version $(cat "$tmp/cmp")"
  else
    echo "FAIL $version parity with the CUE renderer"; cat "$tmp/err" "$tmp/cmp" 2>/dev/null; fail=1
  fi

  for c in "${cases[@]}"; do
    overlay="${c%%|*}"; want="${c##*|}"
    printf '%s\n' "$overlay" >"$tmp/bad.yaml"
    if out=$("$helm" template gateway "$chart" -f "$tmp/bad.yaml" 2>&1); then
      echo "FAIL $version accepted: $overlay"; fail=1
    elif grep -qF -- "$want" <<<"$out"; then
      echo "PASS $version rejects $overlay"
    else
      echo "FAIL $version rejected for another reason: $overlay"; echo "$out" | head -5; fail=1
    fi
  done
done
exit $fail
