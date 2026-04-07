#!/usr/bin/env bash
# End-to-end smoke: health → IO status → POST /api/chat (NATS → orchestrator).
# Defaults match docker-compose.full.yml (orchestrator on host port 18080).
#
# Usage:
#   ./scripts/e2e-docker-stack-check.sh
#   ORCHESTRATOR_URL=http://127.0.0.1:8080 IO_URL=http://127.0.0.1:3001 ./scripts/e2e-docker-stack-check.sh

set -euo pipefail

ORCHESTRATOR_URL="${ORCHESTRATOR_URL:-http://127.0.0.1:18080}"
IO_URL="${IO_URL:-http://127.0.0.1:3001}"
GRAFANA_URL="${GRAFANA_URL:-http://127.0.0.1:3003}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "OK: $*"; }

echo "=== [1/4] Orchestrator health ==="
H="$(curl -sfS "${ORCHESTRATOR_URL}/" -o /dev/null -w "%{http_code}" 2>/dev/null || true)"
if [[ "$H" != "200" ]]; then
  H="$(curl -sfS "${ORCHESTRATOR_URL}/health" -o /dev/null -w "%{http_code}" 2>/dev/null || echo "000")"
fi
[[ "$H" == "200" ]] || fail "orchestrator not reachable at ${ORCHESTRATOR_URL} (HTTP ${H}) — try / and /health"
curl -sfS "${ORCHESTRATOR_URL}/" 2>/dev/null | head -c 400 || curl -sfS "${ORCHESTRATOR_URL}/health" | head -c 400
echo ""
ok "orchestrator responded"

echo ""
echo "=== [2/4] IO API status ==="
curl -sfS "${IO_URL}/api/status" | head -c 500 || fail "IO not reachable at ${IO_URL}"
echo ""
ok "IO /api/status OK"

echo ""
echo "=== [3/4] IO → NATS → orchestrator (POST /api/chat) ==="
TASK_ID="$(uuidgen 2>/dev/null || python3 -c 'import uuid; print(uuid.uuid4())')"
# traceparent optional — IO will create one if absent
BODY="$(python3 -c 'import json,sys; print(json.dumps({"taskId":sys.argv[1],"content":"e2e-docker-stack-check ping"}))' "${TASK_ID}")"
CHAT_OUT="$(mktemp)"
set +e
curl -sS -N --max-time 15 -X POST "${IO_URL}/api/chat" \
  -H 'Content-Type: application/json' \
  -d "${BODY}" >"${CHAT_OUT}" 2>&1
RC=$?
set -e
# 0 = success; 28 = --max-time; 18 = partial body (older Bun/proxies closing mid-SSE)
if [[ $RC -ne 0 ]] && [[ $RC -ne 28 ]] && [[ $RC -ne 18 ]]; then
  fail "POST /api/chat failed (curl exit $RC): $(head -c 300 "${CHAT_OUT}")"
fi
if grep -q 'Orchestrator is not connected\|not connected' "${CHAT_OUT}" 2>/dev/null; then
  fail "IO reports NATS/orchestrator not connected — check IO container NATS_URL and nats service"
fi
ok "POST /api/chat completed (task_id=${TASK_ID}); first bytes:"
head -c 400 "${CHAT_OUT}" || true
echo ""
rm -f "${CHAT_OUT}"

echo ""
echo "=== [4/4] Grafana (optional) ==="
if curl -sfS -o /dev/null -w "%{http_code}" "${GRAFANA_URL}/login" | grep -q 200; then
  ok "Grafana reachable at ${GRAFANA_URL} — Explore → Loki → {compose_service=\"io\"} or \"orchestrator\"}"
else
  echo "SKIP: Grafana not reachable at ${GRAFANA_URL}"
fi

echo ""
echo "Done. If chat hung or Loki shows decomposition timeout, start agents on the same NATS:"
echo "  export ANTHROPIC_API_KEY=... && docker compose -f docker-compose.full.yml -f docker-compose.agents.yml up -d --build"
echo "Trace in logs: send traceparent on requests or read JSON trace_id from IO lines in Loki."
