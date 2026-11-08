#!/usr/bin/env bash
# The Crossplane demo (docs/PLAN.md, Phase 2): the example composition,
# rendered by crossplane render with function-go-templating and
# function-docuconf, against a registry holding the orders image and its
# contract. One App renders into a configured Deployment, its image pinned
# by digest; the same App with PORT 70000 is rejected, and nothing renders.
#
# Needs Docker and Go. Run it from anywhere: cmd/docuconf/fn/example/demo/demo.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
example="$here/.."
fn="$here/../.."
cli="$here/../../.."
work="$(mktemp -d)"
pids=()
cleanup() {
	for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
	docker rm -f docuconf-demo-registry >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }

step "Tools: docuconf, the functions and the crossplane CLI"
(cd "$cli" && go build -o "$work/docuconf" .)
(cd "$fn" && go build -o "$work/function-docuconf" .)
GOBIN="$work" go install github.com/crossplane-contrib/function-go-templating@v0.13.2
GOBIN="$work" go install github.com/crossplane/cli/v2/cmd/crossplane@v2.5.0

step "A registry with the orders image and its contract"
docker run -d --name docuconf-demo-registry -p 5000:5000 registry:2 >/dev/null
for _ in $(seq 1 30); do curl -fsS http://localhost:5000/v2/ >/dev/null 2>&1 && break; sleep 1; done
docker pull -q busybox:1.36 >/dev/null
docker tag busybox:1.36 localhost:5000/orders-api:2.3.0
docker push -q localhost:5000/orders-api:2.3.0 >/dev/null
"$work/docuconf" push -contract "$cli/../../examples/orders/contract.cue" localhost:5000/orders-api:2.3.0

step "The functions, in Crossplane's development runtime"
"$work/function-docuconf" --insecure --address localhost:9443 &
pids+=($!)
"$work/function-go-templating" --insecure --address localhost:9444 &
pids+=($!)
for port in 9443 9444; do
	for _ in $(seq 1 30); do (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && break; sleep 1; done
done

render() {
	"$work/crossplane" render "$1" "$example/composition.yaml" "$here/functions.yaml" \
		--required-resources "$here/secrets.yaml" --include-full-xr
}

step "The App: rendered into a configured Deployment"
render "$here/app.yaml" > "$work/good.yaml"
cat "$work/good.yaml"
grep -q 'type: ContractValid' "$work/good.yaml"
grep -q 'reason: Valid' "$work/good.yaml"
grep -q 'name: LOG_LEVEL' "$work/good.yaml"
grep -q 'image: localhost:5000/orders-api@sha256:' "$work/good.yaml"
grep -q 'enableServiceLinks: false' "$work/good.yaml"

step "The App with PORT 70000: rejected, nothing rendered"
if render "$here/app-bad-port.yaml" > "$work/bad.yaml" 2> "$work/bad.err"; then
	cat "$work/bad.yaml"
	echo "the App with PORT 70000 rendered" >&2
	exit 1
fi
cat "$work/bad.err"
grep -q 'PORT: 70000 is above max 65535' "$work/bad.err"

step "ok: the valid App deploys, the invalid one is rejected before anything is applied"
