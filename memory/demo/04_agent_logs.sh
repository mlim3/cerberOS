#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$THIS_DIR/common.sh"

require_basics
ensure_api_up

bold "Demo 04: Agent execution logs"

agent_id="memory-agent-demo"

post_step() {
  local action_type="$1"
  local status="$2"
  local payload="$3"

  local body
  body="$(jq -nc \
    --arg agentId "$agent_id" \
    --arg actionType "$action_type" \
    --arg status "$status" \
    --argjson payload "$payload" \
    '{agentId:$agentId, actionType:$actionType, payload:$payload, status:$status}')"

  curl -sS -X POST "$BASE_URL/agent/$DEMO_TASK_ID/executions" \
    -H "Content-Type: application/json" \
    -d "$body" | jq .
}

info "1) Insert 3 execution steps"
post_step "reasoning_step" "pending" '{"step":"analyze user request"}'
post_step "tool_call" "success" '{"tool":"query_db","rows":2}'
post_step "final_answer" "success" '{"summary":"completed"}'

info "2) Fetch timeline (limited)"
curl -sS "$BASE_URL/agent/$DEMO_TASK_ID/executions?limit=10" | jq .

ok "Agent logs demo complete."
