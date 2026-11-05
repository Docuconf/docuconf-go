#!/usr/bin/env bash
# End-to-end test: deploys the orders service to a kind cluster with the
# Terraform root in deploy/terraform, as the README describes, then checks
# that the running service received what the module rendered:
#
#   1. A value the contract forbids fails at plan, before the cluster
#      sees it.
#   2. The plan passes docuconf lint terraform; terraform apply rolls out.
#   3. The service serves HTTPS with the cert-manager certificate, and its
#      /config shows the values set in main.tf, with the secrets redacted.
#      /discounts serves the inline file, and a webhook signed with either
#      key of the rotating key set is accepted.
#   4. A changed file input, applied through Terraform, rolls out and is
#      served.
#   5. A renewed certificate is served without a restart.
#
# Needs docker, kind, kubectl, terraform (or set TF=tofu), docuconf, curl
# and openssl. It uses the cluster $KIND_CLUSTER (default orders-e2e), and
# creates it, and deletes it at the end, if it does not exist. Set
# KEEP_CLUSTER=1 to keep a cluster it created. The shared steps are in
# ../../e2e/lib.sh.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
TF="${TF:-terraform}"
# shellcheck source=SCRIPTDIR/../../e2e/lib.sh
. "$here/../../e2e/lib.sh"
need "$TF" docuconf

teardown() { (cd "$tmp/terraform" && "$TF" destroy -auto-approve -input=false -var "image=$image"); }

setup
export KUBE_CONFIG_PATH="$KUBECONFIG" # for the kubernetes provider

# Work in a copy of the root, so the test leaves no state or override
# files in the repository.
tf_dir="$tmp/terraform"
mkdir "$tf_dir"
cp "$root"/*.tf "$tf_dir/"
cp -r "$root/modules" "$tf_dir/"
cd "$tf_dir"
"$TF" init -input=false >/dev/null

step "1. a value the contract forbids fails at plan"
cp "$here/overrides/too-many-workers.tf" e2e_override.tf
if "$TF" plan -input=false -no-color -var "image=$image" >"$tmp/plan.txt" 2>&1; then
  fail "the plan accepted WORKER_COUNT = 100"
fi
grep -q "WORKER_COUNT: 100 is above max 64" "$tmp/plan.txt" ||
  { cat "$tmp/plan.txt" >&2; fail "the plan failed, but not on WORKER_COUNT"; }
echo "refused: WORKER_COUNT: 100 is above max 64"
rm e2e_override.tf

deploy() { # plan, lint, apply, and wait for the rollout
  "$TF" plan -input=false -no-color -var "image=$image" -out plan.out >"$tmp/plan.txt" ||
    { cat "$tmp/plan.txt" >&2; fail "plan failed"; }
  "$TF" show -json plan.out >plan.json
  docuconf lint terraform plan.json -contract "$orders/contract.cue"
  "$TF" apply -input=false -no-color plan.out >"$tmp/apply.txt" ||
    { cat "$tmp/apply.txt" >&2; fail "apply failed"; }
  rolled_out
}

step "2. plan, lint and apply"
deploy

step "3. the service received what the module rendered"
check_service

step "4. a changed file input rolls out"
cp "$here/overrides/new-discount.tf" e2e_override.tf
deploy
rm e2e_override.tf
port_forward
check_discounts '{"E2E50":50,"SPRING25":25,"WELCOME10":10}'

step "5. a renewed certificate is served without a restart"
check_renewal

echo
echo "e2e: ok"
