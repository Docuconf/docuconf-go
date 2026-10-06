#!/usr/bin/env bash
# Regenerates the chart's files/docuconf/contract.json and values.schema.json
# from the catalog's docuconf contract. Run it after the contract changes.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../../../cmd/docuconf"
go run . helm -contract ../../spec/cue/examples/catalog_contract.cue -chart "$here"
