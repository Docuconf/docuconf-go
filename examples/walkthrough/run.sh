#!/usr/bin/env bash
# Regenerates every output in this walkthrough from config/config.go, and
# checks the three platform paths agree:
#   docuconf CLI (render), plain CUE (platform/deployment.cue) and Helm (chart/).
# Needs go, cue (v0.17) and helm; python3 with PyYAML for the parity check.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="$here/../.."
CUE="${CUE:-cue}"
HELM="${HELM:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$here"

(cd "$repo/cmd/docuconf" && go build -o "$tmp/docuconf" .)
docuconf() { "$tmp/docuconf" "$@"; }
mkdir -p out

# 1. The app exports its contract from its config struct.
docuconf export -C "$repo" -pkg ./examples/walkthrough/config -type Config \
  -name orders-api -app-version 2.3.0 -package orders -o contract.cue

# 2. The docuconf CLI checks the platform's inputs and renders them.
docuconf vet -contract contract.cue -values platform/values.yaml -files platform/files.yaml \
  -policy platform/prod-policy.cue >out/vet.txt
docuconf vet -contract contract.cue -values platform/bad-values.yaml -files platform/bad-files.yaml \
  -policy platform/prod-policy.cue >out/vet-bad.txt && { echo "vet accepted bad values" >&2; exit 1; }
docuconf render -contract contract.cue -values platform/values.yaml -files platform/files.yaml >out/render.yaml

# 3. Plain CUE, as a Crossplane composition function would evaluate it. The
#    meta-schema module, the contract and the platform package go into one
#    CUE module (docuconf.dev).
mod="$tmp/mod"
mkdir -p "$mod/orders"
cp -r "$repo/spec/cue/cue.mod" "$repo/spec/cue/contract" "$mod/"
cp contract.cue "$mod/orders/"
cp -r platform "$mod/platform"
(cd "$mod" && "$CUE" export ./orders -e 'files."tax-rates".schema' --out json >"$tmp/tax-rates.json")
(cd "$mod" && "$CUE" import -f -p platform -l '#TaxRates:' jsonschema: "$tmp/tax-rates.json" -o platform/tax_rates_schema.cue)
cp "$mod/platform/tax_rates_schema.cue" platform/
(cd "$mod" && "$CUE" vet -c ./platform:platform)
(cd "$mod" && "$CUE" export ./platform:platform -e 'objects' --out yaml) |
  python3 -c 'import sys, yaml; sys.stdout.write(yaml.safe_dump_all(yaml.safe_load(sys.stdin), sort_keys=False))' >out/cue-objects.yaml
# The same bad inputs, through CUE.
cp platform/bad-values.yaml "$mod/platform/values.yaml"
cp platform/bad-files.yaml "$mod/platform/files.yaml"
if (cd "$mod" && "$CUE" vet -c ./platform:platform) >"$tmp/cue-bad.txt" 2>&1; then
  echo "cue vet accepted bad values" >&2; exit 1
fi
grep -v "^    " "$tmp/cue-bad.txt" >out/cue-vet-bad.txt

# 4. Helm: the chart carries the contract and a values schema made from it.
docuconf helm -contract contract.cue -chart chart >/dev/null
(cd chart && "$HELM" dependency build . >/dev/null)
# Re-dumped so Helm 3 and 4 give the same file.
"$HELM" template orders-api chart |
  python3 -c 'import sys, yaml; sys.stdout.write(yaml.safe_dump_all([d for d in yaml.safe_load_all(sys.stdin) if d], sort_keys=False))' >out/helm.yaml
printf 'docuconf:\n  values:\n    LOG_LEVEL: verbose\n    LOG_LEVL: warn\n    DATABASE_URL: mysql://orders:hunter2@db/orders\n' >"$tmp/bad.yaml"
if "$HELM" template orders-api chart -f "$tmp/bad.yaml" >/dev/null 2>"$tmp/helm-bad.txt"; then
  echo "helm accepted bad values" >&2; exit 1
fi
# From Helm's error on (Helm 3 and 4 log the merge warnings before it
# differently), with its entries sorted: Helm lists them in random order.
sed -n '/^Error:/,$p' "$tmp/helm-bad.txt" | python3 -c '
import sys
head, entries = [], []
for line in sys.stdin.read().rstrip().splitlines():
    if line.startswith("- "):
        entries.append([line])
    elif entries and line.startswith("  "):
        entries[-1].append(line)
    else:
        head.append(line)
print("\n".join(head + [l for e in sorted(entries) for l in e]))' >out/helm-bad.txt
grep -q hunter2 out/*.txt && { echo "a secret value leaked into an output" >&2; exit 1; }

# 5. The three paths agree.
python3 "$repo/helm/testdata/compare.py" out/helm.yaml out/render.yaml
python3 "$repo/helm/testdata/compare.py" out/cue-objects.yaml out/render.yaml
