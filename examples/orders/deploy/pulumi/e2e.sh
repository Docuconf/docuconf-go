#!/usr/bin/env bash
# End-to-end test: deploys the orders service to a kind cluster with the
# Pulumi program in this directory, then checks that the running service
# received what the component rendered:
#
#   1. A value the contract forbids fails at preview, before the cluster
#      sees it.
#   2. pulumi up rolls out.
#   3. The service serves HTTPS with the cert-manager certificate, and its
#      /config shows the values set in index.ts, with the secrets redacted.
#      /discounts serves the inline file, and a webhook signed with either
#      key of the rotating key set is accepted.
#   4. A changed file input, applied through Pulumi, rolls out and is
#      served.
#   5. A renewed certificate is served without a restart.
#
# Needs docker, kind, kubectl, pulumi, npm, curl and openssl. The cluster is
# as for ../terraform/e2e/e2e.sh: $KIND_CLUSTER (default orders-e2e),
# created and deleted unless it exists, KEEP_CLUSTER=1 to keep it. The
# stack's state is local and temporary. The shared steps are in
# ../e2e/lib.sh.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=SCRIPTDIR/../e2e/lib.sh
. "$here/../e2e/lib.sh"
need pulumi npm

export PULUMI_CONFIG_PASSPHRASE="" PULUMI_SKIP_UPDATE_CHECK=1 PULUMI_BACKEND_URL="file://$tmp/state"
teardown() { (cd "$tmp/pulumi" && pulumi destroy --yes --non-interactive); }

setup

# Work in a copy of the program, so the test leaves no stack files in the
# repository.
step "program"
mkdir "$tmp/state" "$tmp/pulumi"
cp -r "$here"/index.ts "$here"/package.json "$here"/package-lock.json "$here"/tsconfig.json "$here"/Pulumi.yaml "$here"/orders-config "$tmp/pulumi/"
cd "$tmp/pulumi"
npm ci --no-audit --no-fund >/dev/null
pulumi stack init e2e >/dev/null
pulumi config set image "$image"

step "1. a value the contract forbids fails at preview"
pulumi config set workerCount 100
if pulumi preview --non-interactive >"$tmp/preview.txt" 2>&1; then
  fail "the preview accepted WORKER_COUNT = 100"
fi
grep -q "WORKER_COUNT: must be at most 64 (got 100)" "$tmp/preview.txt" ||
  { cat "$tmp/preview.txt" >&2; fail "the preview failed, but not on WORKER_COUNT"; }
echo "refused: WORKER_COUNT: must be at most 64 (got 100)"
pulumi config set workerCount 8

deploy() { # pulumi up, and wait for the rollout
  pulumi up --yes --non-interactive >"$tmp/up.txt" 2>&1 ||
    { cat "$tmp/up.txt" >&2; fail "pulumi up failed"; }
  rolled_out
}

step "2. pulumi up"
deploy

step "3. the service received what the component rendered"
check_service

step "4. a changed file input rolls out"
pulumi config set --path discountCodes.WELCOME10 10
pulumi config set --path discountCodes.SPRING25 25
pulumi config set --path discountCodes.E2E50 50
deploy
port_forward
check_discounts '{"E2E50":50,"SPRING25":25,"WELCOME10":10}'

step "5. a renewed certificate is served without a restart"
check_renewal

echo
echo "e2e: ok"
