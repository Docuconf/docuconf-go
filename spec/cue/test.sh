#!/usr/bin/env bash
# Validates the contract meta-schema.
#
# 1. Compiles the JSON Schemas in example contracts to CUE, as the
#    docuconf toolchain does before validation (testdata/gen).
# 2. The examples must pass.
# 3. Rendered output must match testdata/render byte for byte.
# 4. Every file in testdata/invalid must be rejected with an error
#    containing the substring on its "// want:" line.
# 5. Every generated docs model (testdata/docs/*/docs.json and the
#    orders example's) must pass #DocsModel, and a broken one must not.
set -u
cd "$(dirname "$0")"
CUE="${CUE:-cue}"
fail=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gen() { # cue-expression definition-name output-file
  "$CUE" export examples/gateway_contract.cue -e "$1" --out json >"$tmp/schema.json" &&
    "$CUE" import -f -p examples -l "$2:" jsonschema: "$tmp/schema.json" -o "testdata/gen/$3"
}
gen gateway.files.routes.schema '#RoutesSchema' routes_schema.cue || fail=1
gen gateway.vars.RATE_LIMITS.schema '#RateLimitsSchema' rate_limits_schema.cue || fail=1

pkg=(examples/*.cue testdata/gen/*.cue)
contracts=(examples/*_contract.cue testdata/gen/*.cue)

if "$CUE" vet -c ./contract && "$CUE" vet -c ./docs && "$CUE" vet -c "${pkg[@]}"; then
  echo "PASS examples"
else
  echo "FAIL examples"; fail=1
fi

for g in testdata/render/*.yaml; do
  expr=$(basename "$g" .yaml)
  if diff -u "$g" <("$CUE" export "${pkg[@]}" -e "$expr" --out yaml); then
    echo "PASS render $expr"
  else
    echo "FAIL render $expr"; fail=1
  fi
done

for f in testdata/invalid/*.cue; do
  want=$(sed -n 's|^// want: ||p' "$f")
  if [ -z "$want" ]; then
    echo "FAIL $f has no // want: line"; fail=1; continue
  fi
  if out=$("$CUE" vet -c "${contracts[@]}" "$f" 2>&1); then
    echo "FAIL $f was accepted"; fail=1
  elif grep -qF -- "$want" <<<"$out"; then
    echo "PASS $f"
  else
    echo "FAIL $f rejected for the wrong reason (want \"$want\"):"
    head -3 <<<"$out"; fail=1
  fi
done
for m in testdata/docs/*/docs.json ../../examples/orders/docs.json; do
  if "$CUE" vet -c -d '#DocsModel' ./docs json: - <"$m"; then
    echo "PASS docs model $m"
  else
    echo "FAIL docs model $m"; fail=1
  fi
done
# A secret with a default must be rejected, so the check above can fail.
if sed 's/"secret": false/"secret": true/' testdata/docs/orders/docs.json |
  "$CUE" vet -c -d '#DocsModel' ./docs json: - >/dev/null 2>&1; then
  echo "FAIL a docs model with a secret's default was accepted"; fail=1
else
  echo "PASS docs model rejects a secret's default"
fi
exit $fail
