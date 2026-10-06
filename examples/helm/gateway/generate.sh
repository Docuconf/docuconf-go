#!/usr/bin/env bash
# Regenerates the chart's files/docuconf/contract.json and values.schema.json
# from the gateway's docuconf contract. Run it after the contract changes.
#
# Outside this repository the same step is:
#   docuconf helm -contract contract.cue -chart .
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../../../cmd/docuconf"
go run . helm -contract ../../spec/cue/examples/gateway_contract.cue -chart "$here"
