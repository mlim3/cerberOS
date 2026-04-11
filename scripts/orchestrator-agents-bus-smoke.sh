#!/usr/bin/env bash
# Manual end-to-end traffic on shared subjects (core NATS publish).
# Requires: `nats` CLI, `python3`, running NATS (:4222), real orchestrator + agents connected.
#
# 1) Publishes to aegis.heartbeat.<id> — agents subscribe to aegis.heartbeat.* (expect out_msgs ↑ on aegis-aegis-agents; connz counts server→client deliveries).
# 2) Publishes a valid §13.5 envelope to aegis.orchestrator.agent.status — orchestrator gateway handles it (expect out_msgs ↑ on aegis-orchestrator-* for that delivery).
#
# Usage:
#   ./scripts/orchestrator-agents-bus-smoke.sh
#   NATS_URL=nats://127.0.0.1:4222 ./scripts/orchestrator-agents-bus-smoke.sh
set -euo pipefail

NATS_URL="${NATS_URL:-nats://127.0.0.1:4222}"

if ! command -v nats >/dev/null 2>&1; then
  echo "FAIL: install NATS CLI: brew install nats-io/nats-tools/nats"
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "FAIL: python3 required to build envelope JSON"
  exit 1
fi

echo "=== [1/2] Heartbeat subject (agents listen on aegis.heartbeat.*) ==="
HB_SUBJ="aegis.heartbeat.bus-smoke-$(date +%s)"
nats --server "${NATS_URL}" pub "${HB_SUBJ}" '{"source":"orchestrator-agents-bus-smoke","ping":true}'
echo "OK: published to ${HB_SUBJ}"

echo ""
echo "=== [2/2] Agent status → orchestrator (aegis.orchestrator.agent.status) ==="
STATUS_JSON="$(python3 <<'PY'
import json, uuid
from datetime import datetime, timezone
correlation_id = str(uuid.uuid4())
envelope = {
    "message_id": str(uuid.uuid4()),
    "message_type": "agent.status",
    "source_component": "agents",
    "correlation_id": correlation_id,
    "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z",
    "schema_version": "1.0",
    "payload": {
        "agent_id": "bus-smoke-agent",
        "task_id": "bus-smoke-task",
        "state": "ACTIVE",
        "message": "orchestrator-agents-bus-smoke",
    },
}
print(json.dumps(envelope))
PY
)"
nats --server "${NATS_URL}" pub "aegis.orchestrator.agent.status" "${STATUS_JSON}"
echo "OK: published agent.status envelope (correlation_id in payload is task ref for monitor)"

echo ""
echo "Verify with monitoring (/connz is server-centric):"
echo "  in_msgs  = messages this client SENT to the server (publish)"
echo "  out_msgs = messages the server DELIVERED to this client (subscriptions)"
echo "For this smoke test, expect out_msgs to bump for both (heartbeat + agent.status):"
echo "  curl -sS 'http://127.0.0.1:8222/connz?subs=1' | jq '.connections[] | select(.name|test(\"orchestrator|aegis-agents\")) | {name,in_msgs,out_msgs}'"
echo "Orchestrator logs may show [gateway] lines when agent status is processed."
echo "orchestrator-agents-bus-smoke: done."
