#!/usr/bin/env bash
# Smoke test: starts the orders service with valid env and checks its
# endpoints, starts it with bad env and checks it refuses to boot, then
# runs the local-files recipe from the README (HTTPS with a dev
# certificate). Needs go and curl; the last step also needs openssl.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
pid=""
trap '[ -n "$pid" ] && kill "$pid" 2>/dev/null; rm -rf "$tmp"' EXIT
cd "$here"
go build -o "$tmp/orders" .

secret='postgres://orders:s3cr3t-pw@localhost:5432/orders'
port=$((20000 + RANDOM % 20000))

wait_for() { # url
  for _ in $(seq 50); do
    curl -fsSk "$1" >"$tmp/body" 2>/dev/null && return 0
    sleep 0.1
  done
  echo "no answer from $1" >&2; cat "$tmp/out.txt" >&2; exit 1
}

# 1. Valid env: the service serves /healthz and /config, without the secret.
PORT=$port DATABASE_URL="$secret" "$tmp/orders" >"$tmp/out.txt" 2>&1 &
pid=$!
wait_for "http://127.0.0.1:$port/healthz"
[ "$(cat "$tmp/body")" = ok ] || { echo "GET /healthz did not return ok" >&2; exit 1; }
curl -fsS "http://127.0.0.1:$port/config" >"$tmp/config.json"
if grep -q 's3cr3t-pw' "$tmp/config.json" "$tmp/out.txt"; then
  echo "the secret leaked into /config or the log" >&2; exit 1
fi
grep -q '"DATABASE_URL":"\*\*\*"' "$tmp/config.json" || { echo "GET /config did not redact DATABASE_URL" >&2; exit 1; }
echo "valid env: /healthz ok, /config $(cat "$tmp/config.json")"
kill "$pid"; wait "$pid" 2>/dev/null || true; pid=""

# 2. PORT=0 and no DATABASE_URL: the service exits 1 and names both.
set +e
env -u DATABASE_URL PORT=0 DATABSE_URL=x "$tmp/orders" >"$tmp/bad.txt" 2>&1
code=$?
set -e
[ "$code" = 1 ] || { echo "want exit 1 with PORT=0 and no DATABASE_URL, got $code" >&2; cat "$tmp/bad.txt" >&2; exit 1; }
head -1 "$tmp/bad.txt" | grep -q 'DATABSE_URL is set but not declared; did you mean DATABASE_URL?' ||
  { echo "no typo hint for DATABSE_URL:" >&2; cat "$tmp/bad.txt" >&2; exit 1; }
sed 1d "$tmp/bad.txt" >"$tmp/problems.txt"
cat >"$tmp/want.txt" <<'WANT'
docuconf: 2 configuration problems:
  PORT: 0 is below min 1 (out_of_range)
  DATABASE_URL: is required but not set (missing_required)
WANT
diff -u "$tmp/want.txt" "$tmp/problems.txt" || { echo "unexpected boot output" >&2; exit 1; }
echo "bad env: exited 1 with:"
sed 's/^/  /' "$tmp/bad.txt"

# 3. The README's local-files recipe: a dev certificate under ./dev.
if ! command -v openssl >/dev/null; then
  echo "smoke: ok (openssl not found, skipped the HTTPS recipe)"
  exit 0
fi
work="$tmp/recipe"
mkdir -p "$work" && cd "$work"
mkdir -p dev/etc/orders/tls dev/etc/orders/discounts
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 90 \
  -subj /CN=orders.example.com -addext subjectAltName=DNS:orders.example.com \
  -keyout dev/etc/orders/tls/tls.key -out dev/etc/orders/tls/tls.crt 2>/dev/null
echo 'codes: {WELCOME10: 10}' > dev/etc/orders/discounts/discounts.yaml
PORT=$port DATABASE_URL="$secret" DOCUCONF_FILE_ROOT=./dev "$tmp/orders" >"$tmp/out.txt" 2>&1 &
pid=$!
wait_for "https://127.0.0.1:$port/discounts"
[ "$(cat "$tmp/body")" = '{"WELCOME10":10}' ] || { echo "GET /discounts: $(cat "$tmp/body")" >&2; exit 1; }
echo "dev files: HTTPS up, /discounts $(cat "$tmp/body")"
echo "smoke: ok"
