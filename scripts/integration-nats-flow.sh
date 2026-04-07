#!/usr/bin/env bash
# End-to-end NATS checks: monitoring, DataBus health, pub/sub smoke, optional heartbeat.
# Prerequisite: aegis-databus stack (NATS on :4222, monitoring :8222, DataBus :9091).
#
# Usage:
#   ./scripts/integration-nats-flow.sh
#   NATS_URL=nats://127.0.0.1:4222 NATS_HTTP=http://127.0.0.1:8222 ./scripts/integration-nats-flow.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NATS_URL="${NATS_URL:-nats://127.0.0.1:4222}"
NATS_HTTP="${NATS_HTTP:-http://127.0.0.1:8222}"
DATABUS_HEALTH="${DATABUS_HEALTH:-http://127.0.0.1:9091/healthz}"

echo "=== [1/4] NATS monitoring (${NATS_HTTP}) ==="
if ! curl -sf "${NATS_HTTP}/healthz" >/dev/null 2>&1; then
  echo "FAIL: cannot reach ${NATS_HTTP}/healthz — start the stack (e.g. make bootstrap from repo root)."
  exit 1
fi
echo "OK: NATS monitoring up"

echo "=== [2/4] DataBus (${DATABUS_HEALTH}) ==="
code=$(curl -s -o /dev/null -w '%{http_code}' "${DATABUS_HEALTH}" 2>/dev/null || echo 000)
if [[ "${code}" == "200" ]]; then
  echo "OK: DataBus /healthz=200"
elif [[ "${code}" == "503" ]]; then
  echo "WARN: DataBus /healthz=503 (JetStream streams not ready — wait or set AEGIS_JS_STREAM_REPLICAS=1)"
else
  echo "FAIL: DataBus not reachable on ${DATABUS_HEALTH} (code=${code})"
  exit 1
fi

echo "=== [3/4] NATS CLI pub/sub smoke (requires \`nats\` on PATH) ==="
if ! command -v nats >/dev/null 2>&1; then
  echo "SKIP: install NATS CLI: brew install nats-io/nats-tools/nats"
else
  SUBJ="aegis.integration.smoke.$(date +%s)"
  MSG="ping-$RANDOM-$RANDOM"
  tmp="$(mktemp)"
  nats --server "${NATS_URL}" sub "${SUBJ}" --count=1 >"${tmp}" 2>&1 &
  sub_pid=$!
  sleep 0.8
  nats --server "${NATS_URL}" pub "${SUBJ}" "${MSG}"
  wait "${sub_pid}" 2>/dev/null || true
  if grep -qF "${MSG}" "${tmp}"; then
    echo "OK: published and received: ${MSG}"
  else
    echo "FAIL: expected payload not seen. Subscriber output:"
    cat "${tmp}"
    rm -f "${tmp}"
    exit 1
  fi
  rm -f "${tmp}"
fi

echo "=== [4/4] DataBus heartbeat on aegis.health.databus (~8s) ==="
if ! command -v nats >/dev/null 2>&1; then
  echo "SKIP (no nats CLI)"
else
  tmp2="$(mktemp)"
  nats --server "${NATS_URL}" sub "aegis.health.databus" --count=1 >"${tmp2}" 2>&1 &
  hb_pid=$!
  (sleep 15; kill "${hb_pid}" 2>/dev/null) &
  wait "${hb_pid}" 2>/dev/null || true
  if grep -q "databus" "${tmp2}" 2>/dev/null; then
    echo "OK: heartbeat JSON received from DataBus"
  else
    echo "WARN: no heartbeat — ensure aegis-databus is running (heartbeats every ~5s)"
  fi
  rm -f "${tmp2}"
fi

echo "=== [extra] connz / JetStream snapshot ==="
chmod +x "${ROOT}/scripts/check-nats-databus.sh" 2>/dev/null || true
NATS_HTTP="${NATS_HTTP}" "${ROOT}/scripts/check-nats-databus.sh" | head -n 80 || true

echo ""
echo "integration-nats-flow: done."
