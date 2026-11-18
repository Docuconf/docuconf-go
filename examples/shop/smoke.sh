#!/usr/bin/env bash
# Smoke test: builds the shop and runs the real process as the web server,
# as the worker, and as a worker with JSON logs but no log schema, which
# must refuse to start. Checks stdout and the exit code of each, and that
# the secret DATABASE_URL is never printed. Needs go.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cd "$here"
go build -o "$tmp/shop" .

# run NAME WANT_CODE WANT_OUTPUT VAR=value...: runs the shop with only the
# base env and the given variables, and an empty file root.
run() {
  local name="$1" want_code="$2" want="$3"
  shift 3
  mkdir -p "$tmp/root-$name"
  set +e
  env -i PATH="$PATH" DATABASE_URL='postgres://shop@db:5432/shop' DOCUCONF_FILE_ROOT="$tmp/root-$name" "$@" \
    "$tmp/shop" >"$tmp/out.txt" 2>&1
  local code=$?
  set -e
  printf '%s\n' "$want" >"$tmp/want.txt"
  if [ "$code" != "$want_code" ] || ! diff -u "$tmp/want.txt" "$tmp/out.txt"; then
    echo "$name: want exit $want_code, got $code:" >&2; cat "$tmp/out.txt" >&2; exit 1
  fi
  if grep -q 'shop@db' "$tmp/out.txt"; then
    echo "$name: DATABASE_URL was printed" >&2; exit 1
  fi
  echo "$name: exit $code: $(head -1 "$tmp/out.txt")"
}

# 1. The web server.
run web 0 'shop web: port 8080, origins https://shop.example' \
  DOCUCONF_ROLE=web ALLOWED_ORIGINS=https://shop.example

# 2. The worker needs no ALLOWED_ORIGINS.
run worker 0 'shop worker: queues default,mail, webhooks off' \
  DOCUCONF_ROLE=worker QUEUES=default,mail

# 6. JSON logs need LOG_SCHEMA_URL.
run json-logs 1 'docuconf: 1 configuration problem:
  LOG_SCHEMA_URL: is required when LOG_FORMAT is "json" but not set (missing_required)' \
  DOCUCONF_ROLE=worker QUEUES=default LOG_FORMAT=json

echo "smoke: ok"
