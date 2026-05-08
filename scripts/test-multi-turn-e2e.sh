#!/usr/bin/env bash
# test-multi-turn-e2e.sh
#
# End-to-end smoke test for multi-turn conversation continuity.
#
# Sends two messages that share the same conversationId and verifies that the
# agent's response to the second message demonstrates awareness of information
# disclosed only in the first message — proof that ConversationSnapshot was
# written after Turn 1 and injected as prior context for Turn 2.
#
# Prerequisites:
#   ./bootstrap.sh          — full stack running (includes aegis-agents)
#   ANTHROPIC_API_KEY       — set in .env or in the environment
#
# Usage:
#   ./scripts/test-multi-turn-e2e.sh
#   IO_URL=http://localhost:3001 ./scripts/test-multi-turn-e2e.sh

set -euo pipefail

IO_URL="${IO_URL:-http://localhost:3001}"
PASS=0; FAIL=0

# ── helpers ───────────────────────────────────────────────────────────────────

log()  { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
ok()   { printf '  ✓ %s\n' "$*"; PASS=$((PASS+1)); }
fail() { printf '  ✗ %s\n' "$*"; FAIL=$((FAIL+1)); }

# wait_http URL [max_seconds]
# Polls URL until it returns 2xx or timeout.
wait_http() {
  local url=$1 max=${2:-60} i=0
  while ! curl -sf "$url" -o /dev/null 2>/dev/null; do
    i=$((i+1))
    [ $i -ge $max ] && { log "TIMEOUT waiting for $url"; return 1; }
    sleep 2
  done
}

# stream_chat IO_URL TASK_ID CONTENT [CONV_ID]
# POSTs to /api/chat and returns the last accumulated chunk from the SSE stream.
# Blocks until the server sends {"done":true} or the connection closes.
stream_chat() {
  local url=$1 task_id=$2 content=$3 conv_id=${4:-}
  local body
  if [ -n "$conv_id" ]; then
    body=$(printf '{"taskId":"%s","content":"%s","conversationId":"%s"}' \
      "$task_id" "$content" "$conv_id")
  else
    body=$(printf '{"taskId":"%s","content":"%s"}' "$task_id" "$content")
  fi

  # Read SSE stream; capture last non-empty chunk value.
  local last_chunk=""
  while IFS= read -r line; do
    if [[ "$line" == data:* ]]; then
      local data="${line#data: }"
      local chunk
      chunk=$(printf '%s' "$data" | python3 -c \
        "import sys,json; d=json.load(sys.stdin); print(d.get('chunk',''),end='')" 2>/dev/null || true)
      [ -n "$chunk" ] && last_chunk="$chunk"
      # Stop as soon as done:true arrives
      printf '%s' "$data" | python3 -c \
        "import sys,json; d=json.load(sys.stdin); exit(0 if d.get('done') else 1)" 2>/dev/null && break
    fi
  done < <(curl -s -N -X POST "$url/api/chat" \
    -H 'Content-Type: application/json' \
    --max-time 180 \
    -d "$body" 2>/dev/null)

  printf '%s' "$last_chunk"
}

# ── pre-flight checks ─────────────────────────────────────────────────────────

log "=== Multi-Turn Conversation E2E Test ==="
echo

log "Step 1 — checking service health"

if wait_http "$IO_URL/api/health" 5; then
  ok "IO API is reachable ($IO_URL)"
else
  fail "IO API not reachable at $IO_URL — is the stack running? (./bootstrap.sh)"
fi

ORCH_URL="${ORCHESTRATOR_URL:-http://localhost:8080}"
if wait_http "$ORCH_URL/health" 5; then
  ok "Orchestrator is reachable ($ORCH_URL)"
else
  fail "Orchestrator not reachable at $ORCH_URL"
fi

echo

# ── conversation IDs ──────────────────────────────────────────────────────────

CONV_ID=$(python3 -c "import uuid; print(str(uuid.uuid4()))")
# taskId is reused for both turns — this is how the web surface works
# (same chat window = same taskId = natural conversation key)
TASK_ID=$(python3 -c "import uuid; print(str(uuid.uuid4()))")
SENTINEL="Aldebaran-7"   # unusual enough that coincidence is ruled out

log "Step 2 — Turn 1: plant a sentinel fact"
log "  conversation_id : $CONV_ID"
log "  task_id         : $TASK_ID"
log "  content         : My secret code word is $SENTINEL."
echo

TURN1_REPLY=$(stream_chat "$IO_URL" "$TASK_ID" "My secret code word is $SENTINEL." "$CONV_ID")

if [ -z "$TURN1_REPLY" ]; then
  fail "Turn 1 returned empty response — agents may not be processing tasks"
  log "  Check: docker logs <aegis-agents container>"
  log "  Check: ANTHROPIC_API_KEY is set in .env"
  echo
else
  ok "Turn 1 received a response"
  log "  Reply (truncated): ${TURN1_REPLY:0:120}…"
  echo
fi

# ── Turn 2: probe for the sentinel ───────────────────────────────────────────

log "Step 3 — Turn 2: probe for the sentinel fact"
log "  Same conversation_id, same task_id, new content"
log "  content: What is my secret code word?"
echo

TURN2_REPLY=$(stream_chat "$IO_URL" "$TASK_ID" "What is my secret code word?" "$CONV_ID")

if [ -z "$TURN2_REPLY" ]; then
  fail "Turn 2 returned empty response"
  echo
else
  ok "Turn 2 received a response"
  log "  Reply (truncated): ${TURN2_REPLY:0:120}…"
  echo
fi

# ── verdict ───────────────────────────────────────────────────────────────────

log "Step 4 — Checking if Turn 2 references the sentinel ('$SENTINEL')"
echo

if printf '%s' "$TURN2_REPLY" | grep -qi "$SENTINEL"; then
  ok "PASS — agent recalled '$SENTINEL' from Turn 1 context"
  echo
  log "Multi-turn continuity is working end-to-end:"
  log "  Turn 1 → agent ran → ConversationSnapshot written to Memory"
  log "  Turn 2 → factory fetched snapshot → prior turns injected into SpawnContext"
  log "  Agent answered with knowledge from Turn 1 without it being in the current message"
else
  fail "FAIL — agent did not mention '$SENTINEL' in Turn 2 reply"
  echo
  log "Diagnosis checklist:"
  log "  1. Was Turn 1 even processed? (check Turn 1 reply above)"
  log "     If empty: agents container may be down or ANTHROPIC_API_KEY missing"
  log "  2. Was the snapshot written?"
  log "     docker exec <memory-db> psql -U user -d memory_db -c \\"
  log "       \"SELECT agent_id,payload->>'conversation_id',payload->>'total_tokens' FROM memory_records WHERE agent_id LIKE 'conversation:%' LIMIT 5;\""
  log "  3. Was conversation_id on the NATS wire?"
  log "     nats sub 'aegis.agents.task.inbound' --server nats://localhost:4222"
  log "  4. Did the factory fetch prior turns?"
  log "     docker logs <aegis-agents> 2>&1 | grep 'prior turns'"
fi

# ── summary ───────────────────────────────────────────────────────────────────

echo
log "=== Results: $PASS passed, $FAIL failed ==="

[ $FAIL -eq 0 ]
