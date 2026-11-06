#!/usr/bin/env bash
# Plans the orders service on ECS or Cloud Run from a Pulumi program, and
# checks it against the Terraform root of the same target, with no cloud
# account:
#
#   check-cloud-plan.sh ecs|cloudrun PROGRAM_DIR
#
# PROGRAM_DIR is pulumi-ecs, pulumi-go-ecs, pulumi-cloudrun or
# pulumi-go-cloudrun. The script previews the program against a local
# state directory, with the provider configured without credentials (a
# preview of new resources calls no cloud API), plans ../ecs or ../cloudrun
# with its CI provider override, and checks that both plan the same
# container configuration (check-cloud-plan.py), and that the preview passes
# the target's policy pack (pulumi-policy) and docuconf lint pulumi. Then it sets WORKER_COUNT to 100, which the
# contract forbids, and checks that the preview fails with the component's
# message.
#
# Needs pulumi, terraform, docuconf, python3, node and npm, and go for a Go
# program.
set -euo pipefail

target="$1"
program="$(cd "$2" && pwd)"
here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export PULUMI_CONFIG_PASSPHRASE="" PULUMI_SKIP_UPDATE_CHECK=1
export PULUMI_BACKEND_URL="file://$work/state"
mkdir -p "$work/state"

echo "--- terraform plan of $target"
cp -r "$here/$target" "$work/terraform"
(
  cd "$work/terraform"
  cp ci/provider_override.tf .
  terraform init -input=false >/dev/null
  terraform plan -refresh=false -out plan.out >/dev/null
  terraform show -json plan.out >"$work/plan.json"
)

echo "--- pulumi preview of $(basename "$program")"
cd "$program"
if [ -f package.json ]; then
  npm ci --no-audit --no-fund
  npx tsc --noEmit -p .
else
  test -z "$(gofmt -l .)"
  go vet ./...
fi
stack="check-$$"
pulumi stack init "$stack" --non-interactive
trap 'pulumi stack rm "$stack" --yes --force --non-interactive >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
case "$target" in
  ecs)
    pulumi config set aws:accessKey ci
    pulumi config set aws:secretKey ci
    pulumi config set aws:skipCredentialsValidation true
    pulumi config set aws:skipRequestingAccountId true
    pulumi config set aws:skipMetadataApiCheck true
    ;;
  cloudrun)
    pulumi config set gcp:accessToken ci-preview-without-credentials
    ;;
esac
(cd "$here/pulumi-policy/$target" && npm ci --no-audit --no-fund >/dev/null)
pulumi preview --json --non-interactive --policy-pack "$here/pulumi-policy/$target" >"$work/preview.json"
"$here/check-cloud-plan.py" "$target" "$work/plan.json" "$work/preview.json"
docuconf lint pulumi "$work/preview.json" -contract "$here/../contract.cue" -explain

echo "--- a forbidden value fails at preview"
pulumi config set workerCount 100
if pulumi preview --non-interactive >"$work/forbidden.txt" 2>&1; then
  cat "$work/forbidden.txt"
  echo "the preview accepted WORKER_COUNT = 100"
  exit 1
fi
# The TypeScript component words it as @docuconf/core does, the Go one as the Go SDK.
if ! grep -Eq 'WORKER_COUNT: (must be at most 64 \(got 100\)|100 is above max 64)' "$work/forbidden.txt"; then
  cat "$work/forbidden.txt"
  echo "the preview failed, but not on WORKER_COUNT"
  exit 1
fi
echo "the preview refused WORKER_COUNT = 100"
