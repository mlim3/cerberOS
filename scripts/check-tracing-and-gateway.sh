#!/usr/bin/env bash
# CI/local checks for tracing, gateway envelope trace_id, and IO API unit tests.
# Usage: ./scripts/check-tracing-and-gateway.sh  (from repo root)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "=== [1/2] orchestrator (go test ./...) ==="
(cd orchestrator && go test ./... -count=1)

echo ""
echo "=== [2/2] io/api (bun test) ==="
if [[ "${SKIP_IO_API_TESTS:-}" == "1" ]]; then
  echo "SKIP: io/api (SKIP_IO_API_TESTS=1)"
elif ! command -v bun >/dev/null 2>&1; then
  echo "FAIL: bun not in PATH — install https://bun.sh or set SKIP_IO_API_TESTS=1 to skip"
  exit 1
else
  (cd io/api && bun test src)
fi

echo ""
echo "OK: all checks passed."
