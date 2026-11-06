# shellcheck shell=bash
# The end-to-end tests' shared steps, sourced by terraform/e2e/e2e.sh and
# pulumi/e2e.sh: a kind cluster with the orders image, cert-manager and
# the platform's fixtures (fixtures.yaml), and the checks on the running
# service. Each test deploys the service its own way, then calls them.
#
# The caller sets $here, may define teardown (run at exit on a cluster the
# test did not create, to leave it as it was), and calls setup.

e2e="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
orders="$(cd "$e2e/../.." && pwd)"
repo="$(cd "$orders/../.." && pwd)"
cluster="${KIND_CLUSTER:-orders-e2e}"
image="orders-api:e2e"
cert_manager="${CERT_MANAGER_VERSION:-v1.19.1}"

tmp="$(mktemp -d)"
created=""
pf=""
cleanup() {
  [ -n "$pf" ] && kill "$pf" 2>/dev/null
  if [ -n "$created" ]; then
    [ -z "${KEEP_CLUSTER:-}" ] && kind delete cluster --name "$cluster" >/dev/null 2>&1
  elif declare -F teardown >/dev/null; then
    teardown >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
step() { echo; echo "== $*"; }
fail() { echo "e2e: $*" >&2; exit 1; }
need() {
  for tool in "$@"; do
    command -v "$tool" >/dev/null || fail "needs $tool"
  done
}

# setup creates the cluster (unless $KIND_CLUSTER exists), loads the image,
# installs cert-manager and applies the fixtures.
setup() {
  need docker kind kubectl curl openssl
  step "cluster $cluster"
  if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
    kind create cluster --name "$cluster" --wait 120s
    created=1
  fi
  export KUBECONFIG="$tmp/kubeconfig"
  kind get kubeconfig --name "$cluster" >"$KUBECONFIG"

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
    kubectl apply -f "$e2e/fixtures.yaml" >"$tmp/apply.txt" 2>&1 && break
    [ "$i" = 30 ] && { cat "$tmp/apply.txt" >&2; fail "fixtures were not applied"; }
    sleep 2
  done
  kubectl -n orders wait certificate/orders-tls --for=condition=Ready --timeout=120s
  kubectl -n orders get secret e2e-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >"$tmp/ca.crt"
}

# rolled_out waits for the Deployment, showing why when it fails.
rolled_out() {
  kubectl -n orders rollout status deploy/orders-api --timeout=180s ||
    { kubectl -n orders describe pods >&2; kubectl -n orders logs deploy/orders-api >&2 || true; fail "rollout failed"; }
}

# The newest pod that is not shutting down: after a rollout, the old ones
# can still be terminating.
current_pod() {
  kubectl -n orders get pods -l app=orders-api --sort-by=.metadata.creationTimestamp \
    -o go-template='{{range .items}}{{if not .metadata.deletionTimestamp}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}' | tail -1
}

# port_forward forwards a local $port to the current pod, as $pod.
port_forward() {
  # kubectl can take a while to start forwarding to a pod that has just
  # rolled out, and exits if the pod goes away first: start it again until
  # the service answers. It listens on 127.0.0.1 only, which is where get
  # connects, so that a port it cannot bind there is an error.
  for attempt in 1 2 3; do
    [ -n "$pf" ] && { kill "$pf" 2>/dev/null; wait "$pf" 2>/dev/null || true; }
    pod="$(current_pod)"
    port=$((20000 + RANDOM % 20000))
    kubectl -n orders port-forward --address 127.0.0.1 "pod/$pod" "$port:8080" >"$tmp/pf.txt" 2>&1 &
    pf=$!
    for _ in $(seq 75); do
      get /healthz >/dev/null 2>&1 && return 0
      kill -0 "$pf" 2>/dev/null || break
      sleep 0.2
    done
    echo "port-forward to $pod, attempt $attempt:" >&2
    cat "$tmp/pf.txt" >&2
  done
  # The state now, before the exit trap tears the deployment down.
  kubectl -n orders get pods -o wide >&2 || true
  kubectl -n orders logs "pod/$pod" --tail=50 >&2 || true
  fail "no answer from the service"
}

# get PATH: an HTTPS request to the service, verified against the e2e CA
# and the contract's DNS name.
get() {
  curl -fsS --cacert "$tmp/ca.crt" --resolve "orders.example.com:$port:127.0.0.1" "https://orders.example.com:$port$1"
}

# check_service checks that the running service received the inputs both
# programs set: HTTPS with the issued certificate, the values in /config
# with the secrets redacted, the inline discounts, and both rotating
# webhook keys.
check_service() {
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
  echo "/config: the values the program set, secrets redacted"

  check_discounts '{"SPRING25":25,"WELCOME10":10}'

  body='{"order":"42","status":"paid"}'
  for key in old-webhook-key-0123456789abcdef0123 new-webhook-key-0123456789abcdef0123 other-webhook-key-0123456789abcdef; do
    sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$key" | sed 's/.*= //')
    code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$tmp/ca.crt" --resolve "orders.example.com:$port:127.0.0.1" \
      -X POST -H "X-Signature: $sig" -d "$body" "https://orders.example.com:$port/webhooks/payments")
    want=204; [ "${key#other}" != "$key" ] && want=401
    [ "$code" = "$want" ] || fail "webhook signed with the ${key%%-*} key: got $code, want $want"
  done
  echo "webhooks: the old and new keys are accepted, any other is rejected"
}

# check_discounts JSON: /discounts serves exactly these codes.
check_discounts() {
  [ "$(get /discounts)" = "$1" ] || fail "GET /discounts: $(get /discounts), want $1"
  echo "/discounts: $1"
}

# check_renewal: a renewed certificate is served without a restart.
check_renewal() {
  restarts() { kubectl -n orders get pod "$pod" -o jsonpath='{.status.containerStatuses[0].restartCount}'; }
  serial() {
    openssl s_client -connect "127.0.0.1:$port" -servername orders.example.com </dev/null 2>/dev/null |
      openssl x509 -noout -serial
  }
  local before old_serial
  before="$(restarts)"
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
}
