#!/usr/bin/env bash
# Exercise IO → NATS → orchestrator → NATS → agents, and orchestrator → HTTP → IO.
#
# Prerequisites (typical dev):
#   - NATS (e.g. aegis stack :4222, monitoring :8222)
#   - IO API on :3001 with NATS_URL set to the same broker
#   - Real orchestrator with MEMORY + policy + planner path, and:
#       export IO_API_BASE=http://127.0.0.1:3001   # so status pushes back to IO over HTTP
#   - Agents subscribed on aegis.agents.* (optional — needed for a full agent-side receive)
#
# What this script does:
#   1) Starts a short background `nats sub` on aegis.agents.task.inbound (orchestrator → agents).
#   2) Triggers IO to publish a valid UserTask on aegis.orchestrator.tasks.inbound (POST /api/tasks),
#      or falls back to the same envelope via `nats pub` if IO is down.
#   3) Prints how to verify "back to IO": watch IO logs / POST /api/orchestrator/stream-events,
#      and optional nats sub on aegis.orchestrator.task.result.
#
# Usage:
#   ./scripts/integration-io-orchestrator-loop-smoke.sh
#   IO_URL=http://127.0.0.1:3001 NATS_URL=nats://127.0.0.1:4222 ./scripts/integration-io-orchestrator-loop-smoke.sh
set -euo pipefail

IO_URL="${IO_URL:-http://127.0.0.1:3001}"
NATS_URL="${NATS_URL:-nats://127.0.0.1:4222}"

if ! command -v nats >/dev/null 2>&1; then
  echo "FAIL: install NATS CLI: brew install nats-io/nats-tools/nats"
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 required"
  exit 1
fi

TASK_ID="$(python3 -c 'import uuid; print(uuid.uuid4())')"
USER_ID="${USER_ID:-00000000-0000-0000-0000-000000000001}"
CONTENT="${CONTENT:-integration-io-loop-smoke}"

echo "=== [1/3] Subscribe (background) — aegis.agents.task.inbound (orchestrator → agents) ==="
AG_SUB="$(mktemp)"
nats --server "${NATS_URL}" sub "aegis.agents.task.inbound" --count=1 >"${AG_SUB}" 2>&1 &
ag_pid=$!

cleanup() {
  kill "${ag_pid}" 2>/dev/null || true
  rm -f "${AG_SUB}"
}
trap cleanup EXIT

sleep 0.6

echo "=== [2/3] IO → NATS — POST ${IO_URL}/api/tasks ==="
if BODY="$(CONTENT="${CONTENT}" USER_ID="${USER_ID}" python3 -c 'import json,os; print(json.dumps({"content": os.environ["CONTENT"], "userId": os.environ["USER_ID"]}))')" && \
   curl -sf -X POST "${IO_URL}/api/tasks" -H 'Content-Type: application/json' -d "${BODY}" >/dev/null 2>&1; then
  echo "OK: IO accepted POST /api/tasks (published UserTask to aegis.orchestrator.tasks.inbound when NATS connected)"
else
  echo "WARN: IO not reachable at ${IO_URL} — publishing same UserTask via nats pub (core)"
  PAYLOAD="$(python3 <<PY
import json, uuid
from datetime import datetime, timezone
tid = "${TASK_ID}"
uid = "${USER_ID}"
user_task = {
  "task_id": tid,
  "user_id": uid,
  "priority": 5,
  "timeout_seconds": 300,
  "payload": {"raw_input": """${CONTENT}"""},
  "callback_topic": "aegis.user-io.status." + tid,
}
env = {
  "message_id": str(uuid.uuid4()),
  "message_type": "task.inbound",
  "source_component": "io",
  "correlation_id": tid,
  "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z",
  "schema_version": "1.0",
  "payload": user_task,
}
print(json.dumps(env))
PY
)"
  nats --server "${NATS_URL}" pub "aegis.orchestrator.tasks.inbound" "${PAYLOAD}"
  echo "OK: published envelope to aegis.orchestrator.tasks.inbound (task_id=${TASK_ID})"
fi

echo ""
echo "=== [3/3] Wait for optional agent-bound message (up to 12s) ==="
for _ in $(seq 1 24); do
  if grep -qE 'Received on|aegis\.agents\.task\.inbound|task' "${AG_SUB}" 2>/dev/null; then
    echo "OK: saw traffic on aegis.agents.task.inbound — orchestrator dispatched toward agents:"
    head -n 20 "${AG_SUB}" || true
    exit 0
  fi
  sleep 0.5
done
echo "WARN: no message on aegis.agents.task.inbound within timeout."
echo "  Common reasons: orchestrator not running, Memory/policy/planner blocked task, or agents stream only (use orchestrator logs)."
echo ""
echo "Back to IO (HTTP, not NATS): set on orchestrator  IO_API_BASE=${IO_URL}"
echo "  Then status updates hit POST ${IO_URL}/api/orchestrator/stream-events"
echo "Watch results on bus: nats --server ${NATS_URL} sub 'aegis.orchestrator.task.result' --count=1"
echo "integration-io-orchestrator-loop-smoke: done."
