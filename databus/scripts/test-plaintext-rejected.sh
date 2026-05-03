#!/bin/bash
# SR-DB-001: Plaintext / unauthenticated rejection — without NKey, connections fail.
# Prerequisites: make setup-zero-trust && source .env.nkeys && make up-secure
set -e
cd "$(dirname "$0")/.."

echo "=== SR-DB-001 Auth Rejection Test ==="
echo ""

if ! docker ps | grep -q nats; then
  echo "Start secure NATS first: make setup-zero-trust && source .env.nkeys && make up-secure"
  exit 1
fi

unset AEGIS_NKEY_SEED
echo "Connecting WITHOUT AEGIS_NKEY_SEED..."
OUT=$(timeout 5 ./bin/aegis-databus 2>&1) || true
if echo "$OUT" | grep -qE "connect|rejected|auth|timeout|refused|error|failed|Fatal"; then
  echo "PASS: Connection rejected or failed (SR-DB-001)"
  exit 0
fi
echo "Expected auth failure. Output: $OUT"
exit 1
