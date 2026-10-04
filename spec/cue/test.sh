#!/usr/bin/env bash
# Validates the contract meta-schema. The examples must pass, and every
# file in testdata/invalid must be rejected with an error containing the
# substring on its "// want:" line.
set -u
cd "$(dirname "$0")"
CUE="${CUE:-cue}"
fail=0

if "$CUE" vet -c ./contract ./examples; then
  echo "PASS examples"
else
  echo "FAIL examples"; fail=1
fi

for f in testdata/invalid/*.cue; do
  want=$(sed -n 's|^// want: ||p' "$f")
  if [ -z "$want" ]; then
    echo "FAIL $f has no // want: line"; fail=1; continue
  fi
  if out=$("$CUE" vet -c examples/billing_contract.cue "$f" 2>&1); then
    echo "FAIL $f was accepted"; fail=1
  elif grep -qF -- "$want" <<<"$out"; then
    echo "PASS $f"
  else
    echo "FAIL $f rejected for the wrong reason (want \"$want\"):"
    head -3 <<<"$out"; fail=1
  fi
done
exit $fail
