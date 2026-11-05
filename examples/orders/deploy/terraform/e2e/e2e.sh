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
# KEEP_CLUSTER=1 to keep a cluster it created.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
orders="$(cd "$root/../.." && pwd)"
repo="$(cd "$orders/../.." && pwd)"
cluster="${KIND_CLUSTER:-orders-e2e}"
image="orders-api:e2e"
cert_manager="${CERT_MANAGER_VERSION:-v1.19.1}"
TF="${TF:-terraform}"

for tool in docker kind kubectl "$TF" docuconf curl openssl; do
  command -v "$tool" >/dev/null || { echo "e2e: needs $tool" >&2; exit 1; }
done

tmp="$(mktemp -d)"
created=""
pf=""
cleanup() {
  [ -n "$pf" ] && kill "$pf" 2>/dev/null
  if [ -n "$created" ] && [ -z "${KEEP_CLUSTER:-}" ]; then
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
step() { echo; echo "== $*"; }
fail() { echo "e2e: $*" >&2; exit 1; }

step "cluster $cluster"
if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  kind create cluster --name "$cluster" --wait 120s
  created=1
fi
export KUBECONFIG="$tmp/kubeconfig"
kind get kubeconfig --name "$cluster" >"$KUBECONFIG"
export KUBE_CONFIG_PATH="$KUBECONFIG" # for the kubernetes provider

step "image $image"
docker build -q -f "$orders/Dockerfile" -t "$image" "$repo"
kind load docker-image "$image" --name "$cluster"

step "cert-manager $cert_manager"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/$cert_manager/cert-manager.yaml" >/dev/null
for d in cert-manager cert-manager-cainjector cert-manager-webhook; do
  kubectl -n cert-manager rollout status "deploy/$d" --timeout=180s
done

step "platform fixtures: namespace, Secrets, Certificate"
# The webhook can take a few seconds after its rollout to answer.
for i in $(seq 30); do
  kubectl apply -f "$here/fixtures.yaml" >"$tmp/apply.txt" 2>&1 && break
  [ "$i" = 30 ] && { cat "$tmp/apply.txt" >&2; fail "fixtures were not applied"; }
  sleep 2
done
kubectl -n orders wait certificate/orders-tls --for=condition=Ready --timeout=120s

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
  kubectl -n orders rollout status deploy/orders-api --timeout=180s ||
    { kubectl -n orders describe pods >&2; kubectl -n orders logs deploy/orders-api >&2 || true; fail "rollout failed"; }
}

# The newest pod that is not shutting down: after a rollout, the old ones
# can still be terminating.
current_pod() {
  kubectl -n orders get pods -l app=orders-api --sort-by=.metadata.creationTimestamp \
    -o go-template='{{range .items}}{{if not .metadata.deletionTimestamp}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}' | tail -1
}

port_forward() {
  [ -n "$pf" ] && { kill "$pf" 2>/dev/null; wait "$pf" 2>/dev/null || true; }
  pod="$(current_pod)"
  port=$((20000 + RANDOM % 20000))
  kubectl -n orders port-forward "pod/$pod" "$port:8080" >"$tmp/pf.txt" 2>&1 &
  pf=$!
  for _ in $(seq 50); do
    get /healthz >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  cat "$tmp/pf.txt" >&2
  fail "no answer from the service"
}

# get PATH: an HTTPS request to the service, verified against the e2e CA
# and the contract's DNS name.
get() {
  curl -fsS --cacert "$tmp/ca.crt" --resolve "orders.example.com:$port:127.0.0.1" "https://orders.example.com:$port$1"
}

step "2. plan, lint and apply"
deploy

step "3. the service received what the module rendered"
kubectl -n orders get secret e2e-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$tmp/ca.crt"
port_forward
[ "$(get /healthz)" = ok ] || fail "GET /healthz did not return ok"
echo "HTTPS: the certificate for orders.example.com verifies against the e2e CA"

get /config >"$tmp/config.json"
for want in '"LOG_LEVEL":"warn"' '"WORKER_COUNT":8' '"https://shop.example.com"' '"DATABASE_URL":"***"' '"WEBHOOK_KEYS":"***"'; do
  grep -qF "$want" "$tmp/config.json" || fail "GET /config has no $want: $(cat "$tmp/config.json")"
done
if grep -q -e 'e2e-db-password' -e 'webhook-key' "$tmp/config.json"; then
  fail "a secret leaked into /config"
fi
kubectl -n orders logs "$pod" >"$tmp/log.txt"
if grep -q -e 'e2e-db-password' -e 'webhook-key' "$tmp/log.txt"; then
  fail "a secret leaked into the log"
fi
echo "/config: the values from main.tf, secrets redacted"

[ "$(get /discounts)" = '{"SPRING25":25,"WELCOME10":10}' ] || fail "GET /discounts: $(get /discounts)"
echo "/discounts: the inline file"

body='{"order":"42","status":"paid"}'
for key in old-webhook-key-0123456789abcdef0123 new-webhook-key-0123456789abcdef0123 other-webhook-key-0123456789abcdef; do
  sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$key" | sed 's/.*= //')
  code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$tmp/ca.crt" --resolve "orders.example.com:$port:127.0.0.1" \
    -X POST -H "X-Signature: $sig" -d "$body" "https://orders.example.com:$port/webhooks/payments")
  want=204; [ "${key#other}" != "$key" ] && want=401
  [ "$code" = "$want" ] || fail "webhook signed with the ${key%%-*} key: got $code, want $want"
done
echo "webhooks: the old and new keys are accepted, any other is rejected"

step "4. a changed file input rolls out"
cp "$here/overrides/new-discount.tf" e2e_override.tf
deploy
rm e2e_override.tf
port_forward
[ "$(get /discounts)" = '{"E2E50":50,"SPRING25":25,"WELCOME10":10}' ] || fail "GET /discounts after the change: $(get /discounts)"
echo "/discounts: the new code is served"

step "5. a renewed certificate is served without a restart"
restarts() { kubectl -n orders get pod "$pod" -o jsonpath='{.status.containerStatuses[0].restartCount}'; }
before="$(restarts)"
serial() {
  openssl s_client -connect "127.0.0.1:$port" -servername orders.example.com </dev/null 2>/dev/null |
    openssl x509 -noout -serial
}
old_serial="$(serial)"
# Deleting the Secret makes cert-manager issue a new certificate, with a
# new key (rotationPolicy: Always); the kubelet then updates the mounted
# files, which can take a minute or two.
kubectl -n orders delete secret orders-tls >/dev/null
for i in $(seq 90); do
  get /reloadz 2>/dev/null | grep -q '"generation":[2-9]' && break
  [ "$i" = 90 ] && fail "the renewed certificate was not reloaded: $(get /reloadz)"
  sleep 2
done
[ "$(serial)" != "$old_serial" ] || fail "the service still serves the old certificate"
[ "$(restarts)" = "$before" ] || fail "the container restarted"
echo "reloaded: $(get /reloadz)"

echo
echo "e2e: ok"
