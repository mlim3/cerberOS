#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-cerberos}"
IO_SERVICE="${IO_SERVICE:-io}"
IO_LOCAL_PORT="${IO_LOCAL_PORT:-13001}"
AGENTS_DEPLOYMENT="${AGENTS_DEPLOYMENT:-aegis-agents}"
EMBEDDING_DEPLOYMENT="${EMBEDDING_DEPLOYMENT:-embedding-api}"
ORCHESTRATOR_DEPLOYMENT="${ORCHESTRATOR_DEPLOYMENT:-orchestrator}"
CHAT_TIMEOUT_SECONDS="${CHAT_TIMEOUT_SECONDS:-180}"
LOG_SINCE="${LOG_SINCE:-5m}"
TEST_USER_ID="${TEST_USER_ID:-00000000-0000-0000-0000-000000000001}"
TEST_URL="${TEST_URL:-https://example.com}"

bold() {
  printf '\033[1m%s\033[0m\n' "$*"
}

section() {
  echo ""
  bold "$*"
}

info() {
  printf '==> %s\n' "$*"
}

ok() {
  printf '✔ %s\n' "$*"
}

fail() {
  printf '✖ %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

jsonpath_env() {
  local deployment="$1"
  local env_name="$2"
  kubectl get deployment "$deployment" -n "$NAMESPACE" -o jsonpath="{.spec.template.spec.containers[0].env[?(@.name==\"${env_name}\")].value}"
}

print_pod_table() {
  kubectl get pods -n "$NAMESPACE" -o wide | awk '
    NR==1 || /aegis-agents|embedding-api|io-|orchestrator/ {print}
  '
}

cleanup() {
  if [[ -n "${PORT_FORWARD_PID:-}" ]] && kill -0 "${PORT_FORWARD_PID}" >/dev/null 2>&1; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
  fi
  if [[ -n "${EVENT_STREAM_PID:-}" ]] && kill -0 "${EVENT_STREAM_PID}" >/dev/null 2>&1; then
    kill "${EVENT_STREAM_PID}" >/dev/null 2>&1 || true
    wait "${EVENT_STREAM_PID}" 2>/dev/null || true
  fi
  if [[ -n "${CHAT_STREAM_PID:-}" ]] && kill -0 "${CHAT_STREAM_PID}" >/dev/null 2>&1; then
    kill "${CHAT_STREAM_PID}" >/dev/null 2>&1 || true
    wait "${CHAT_STREAM_PID}" 2>/dev/null || true
  fi
}

assert_eq() {
  local got="$1"
  local want="$2"
  local msg="$3"
  if [[ "$got" != "$want" ]]; then
    fail "${msg}: got '${got}', want '${want}'"
  fi
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local msg="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    fail "${msg}: expected to find '${needle}'"
  fi
}

start_port_forward() {
  kubectl port-forward -n "$NAMESPACE" "svc/${IO_SERVICE}" "${IO_LOCAL_PORT}:3001" >/tmp/cerberos-io-port-forward.log 2>&1 &
  PORT_FORWARD_PID=$!

  for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:${IO_LOCAL_PORT}/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  if kill -0 "${PORT_FORWARD_PID}" >/dev/null 2>&1; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
  fi
  fail "io port-forward did not become ready"
}

parse_sse_chunks() {
  local stream_file="$1"
  awk '
    /^data: / {
      sub(/^data: /, "", $0)
      print
    }
  ' "$stream_file" | jq -r 'select(.chunk != null) | .chunk'
}

parse_sse_json() {
  local stream_file="$1"
  awk '
    /^data: / {
      sub(/^data: /, "", $0)
      print
    }
  ' "$stream_file"
}

latest_agents_logs() {
  kubectl logs -n "$NAMESPACE" "deployment/${AGENTS_DEPLOYMENT}" --since="$LOG_SINCE"
}

deployment_ready() {
  local deployment="$1"
  kubectl get deployment "$deployment" -n "$NAMESPACE" -o jsonpath="{.status.conditions[?(@.type==\"Available\")].status}"
}

require_cmd kubectl
require_cmd curl
require_cmd jq
require_cmd uuidgen

trap cleanup EXIT

section "E2E: Agent Skill Search Delegation"
echo "Namespace:          ${NAMESPACE}"
echo "IO service:         ${IO_SERVICE}"
echo "IO local port:      ${IO_LOCAL_PORT}"
echo "Agents deployment:  ${AGENTS_DEPLOYMENT}"
echo "Embedding deploy:   ${EMBEDDING_DEPLOYMENT}"
echo "Orchestrator deploy:${ORCHESTRATOR_DEPLOYMENT}"
echo "Test user id:       ${TEST_USER_ID}"
echo "Target URL:         ${TEST_URL}"
echo "Log window:         ${LOG_SINCE}"

section "Pod Status"
print_pod_table

section "Dependency Readiness"
embedding_ready="$(deployment_ready "${EMBEDDING_DEPLOYMENT}")"
assert_eq "${embedding_ready}" "True" "${EMBEDDING_DEPLOYMENT} deployment is not ready"
ok "${EMBEDDING_DEPLOYMENT} deployment is ready"

section "Deployment Configuration"
info "Reading configured embedding settings from running deployments"
agents_embedding_url="$(jsonpath_env "${AGENTS_DEPLOYMENT}" AEGIS_EMBEDDING_API_URL)"
agents_embedding_model="$(jsonpath_env "${AGENTS_DEPLOYMENT}" AEGIS_EMBEDDING_MODEL)"
agents_embedding_dim="$(jsonpath_env "${AGENTS_DEPLOYMENT}" AEGIS_EMBEDDING_DIM)"
agents_prompt_style="$(jsonpath_env "${AGENTS_DEPLOYMENT}" AEGIS_EMBEDDING_PROMPT_STYLE)"
embedding_model="$(jsonpath_env "${EMBEDDING_DEPLOYMENT}" MODEL_ID)"
embedding_dim="$(jsonpath_env "${EMBEDDING_DEPLOYMENT}" EMBEDDING_DIM)"

[[ -n "${agents_embedding_url}" ]] || fail "aegis-agents AEGIS_EMBEDDING_API_URL is empty"
[[ -n "${agents_embedding_model}" ]] || fail "aegis-agents AEGIS_EMBEDDING_MODEL is empty"
[[ -n "${agents_embedding_dim}" ]] || fail "aegis-agents AEGIS_EMBEDDING_DIM is empty"
[[ -n "${agents_prompt_style}" ]] || fail "aegis-agents AEGIS_EMBEDDING_PROMPT_STYLE is empty"
[[ -n "${embedding_model}" ]] || fail "embedding-api MODEL_ID is empty"
[[ -n "${embedding_dim}" ]] || fail "embedding-api EMBEDDING_DIM is empty"

assert_eq "${agents_embedding_model}" "${embedding_model}" "aegis-agents and embedding-api model mismatch"
assert_eq "${agents_embedding_dim}" "${embedding_dim}" "aegis-agents and embedding-api dimension mismatch"
assert_contains "${agents_embedding_url}" "embedding-api" "aegis-agents embedding URL should target embedding-api"

ok "Deployments agree on model=${agents_embedding_model} dim=${agents_embedding_dim} prompt_style=${agents_prompt_style}"
echo "aegis-agents:"
echo "  AEGIS_EMBEDDING_API_URL:     ${agents_embedding_url}"
echo "  AEGIS_EMBEDDING_MODEL:       ${agents_embedding_model}"
echo "  AEGIS_EMBEDDING_DIM:         ${agents_embedding_dim}"
echo "  AEGIS_EMBEDDING_PROMPT_STYLE:${agents_prompt_style}"
echo "embedding-api:"
echo "  MODEL_ID:                    ${embedding_model}"
echo "  EMBEDDING_DIM:               ${embedding_dim}"

section "Startup Log Check"
info "Checking that aegis-agents announced the shared embedding API on startup"
startup_logs="$(kubectl logs -n "$NAMESPACE" "deployment/${AGENTS_DEPLOYMENT}" --tail=200)"
assert_contains "${startup_logs}" "embedding: using shared embedding API" "shared embedding API startup log missing"
echo "${startup_logs}" | rg "embedding: using shared embedding API|aegis-agents ready" || true
ok "aegis-agents startup logs show the shared embedding API"

section "IO Chat Request"
info "Starting temporary port-forward to io"
start_port_forward
base_url="http://127.0.0.1:${IO_LOCAL_PORT}"
ok "io is reachable at ${base_url}"

task_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
conversation_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
request_query="fetch the public URL ${TEST_URL} and return the page title"
chat_prompt="$(
  cat <<EOF
Follow these steps exactly:
1. Call skills_search with query "${request_query}" and top_k 3.
2. If skills_search recommends spawn_agent, call spawn_agent with the suggested domain and instructions.
3. If skills_search does not recommend spawn_agent, use the best matching tool yourself.
4. Return the final result and then call task_complete.
EOF
)"

chat_payload="$(
  jq -nc \
    --arg taskId "${task_id}" \
    --arg conversationId "${conversation_id}" \
    --arg userId "${TEST_USER_ID}" \
    --arg content "${chat_prompt}" \
    '{taskId:$taskId, conversationId:$conversationId, userId:$userId, content:$content, required_skill_domains:["general"]}'
)"

stream_file="$(mktemp /tmp/cerberos-agents-sse.XXXXXX)"
event_stream_file="$(mktemp /tmp/cerberos-agent-events.XXXXXX)"
plan_auto_approved="false"
plan_preview_seen="false"
info "Sending /api/chat request with required_skill_domains=[general]"
info "Subscribing to /api/events/${task_id} for plan previews and status updates"
curl -N -sS "${base_url}/api/events/${task_id}" >"${event_stream_file}" 2>/dev/null &
EVENT_STREAM_PID=$!

curl -N -sS --max-time "${CHAT_TIMEOUT_SECONDS}" \
  -H "Content-Type: application/json" \
  -H "X-Active-User: ${TEST_USER_ID}" \
  -H "X-Surface-Key: cli" \
  -X POST "${base_url}/api/chat" \
  -d "${chat_payload}" >"${stream_file}" &
CHAT_STREAM_PID=$!

for _ in $(seq 1 "${CHAT_TIMEOUT_SECONDS}"); do
  if [[ "${plan_auto_approved}" != "true" ]]; then
    orchestrator_task_ref="$(
      parse_sse_json "${event_stream_file}" | jq -r 'select(.type=="plan_preview") | .payload.orchestratorTaskRef' | tail -n 1
    )"
    if [[ -n "${orchestrator_task_ref}" && "${orchestrator_task_ref}" != "null" ]]; then
      plan_preview_seen="true"
      info "Plan preview detected; auto-approving orchestratorTaskRef=${orchestrator_task_ref}"
      approve_payload="$(
        jq -nc \
          --arg taskId "${task_id}" \
          --arg orchestratorTaskRef "${orchestrator_task_ref}" \
          '{taskId:$taskId, orchestratorTaskRef:$orchestratorTaskRef, approved:true}'
      )"
      approve_resp="$(curl -fsS -H "Content-Type: application/json" -H "X-Active-User: ${TEST_USER_ID}" -H "X-Surface-Key: cli" -X POST "${base_url}/api/orchestrator/plan-decision" -d "${approve_payload}")"
      echo "plan approval response:"
      echo "${approve_resp}" | jq .
      plan_auto_approved="true"
    fi
  fi

  if ! kill -0 "${CHAT_STREAM_PID}" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! wait "${CHAT_STREAM_PID}"; then
  fail "/api/chat request failed"
fi

echo "Raw SSE stream:"
sed 's/^/  /' "${stream_file}"
if [[ -s "${event_stream_file}" ]]; then
  echo "Task event stream:"
  sed 's/^/  /' "${event_stream_file}"
fi

stream_chunks="$(parse_sse_chunks "${stream_file}" | tr '\n' ' ')"
stream_done="$(awk '/^data: / {sub(/^data: /,"",$0); print}' "${stream_file}" | jq -r 'select(.done == true) | .done' | tail -n 1)"
assert_eq "${stream_done}" "true" "chat stream did not finish cleanly"
[[ -n "${stream_chunks}" ]] || fail "chat stream returned no chunks"
ok "chat stream completed"
echo "Combined streamed response:"
echo "  ${stream_chunks}"
if [[ "${plan_preview_seen}" == "true" ]]; then
  ok "Observed plan preview during execution"
fi
if [[ "${plan_auto_approved}" == "true" ]]; then
  ok "Auto-approved the execution plan so subtasks could run"
fi

sleep 5

section "Agents Log Inspection"
info "Reading recent aegis-agents logs and checking for skill search delegation flow"
agent_logs="$(latest_agents_logs)"
filtered_logs="$(echo "${agent_logs}" | rg 'skills_search|spawn_agent|web_fetch|observe phase: tool result|agent spawner ready|example.com' || true)"
echo "${filtered_logs}" | sed 's/^/  /'

assert_contains "${agent_logs}" '"tool":"skills_search"' "skills_search tool result log missing"
assert_contains "${agent_logs}" "${TEST_URL}" "expected delegated query/URL not present in recent agent logs"
assert_contains "${agent_logs}" '"tool":"spawn_agent"' "spawn_agent tool dispatch/result log missing"
if [[ "${agent_logs}" != *'"tool":"web_fetch"'* && "${agent_logs}" != *'"tool":"web_extract"'* ]]; then
  fail "expected delegated web tool execution log (web_fetch or web_extract)"
fi

section "Summary"
echo "  model:         ${agents_embedding_model}"
echo "  dimensions:    ${agents_embedding_dim}"
echo "  prompt style:  ${agents_prompt_style}"
echo "  task id:       ${task_id}"
echo "  conversation:  ${conversation_id}"
echo "  query:         ${request_query}"
echo "  plan preview:  ${plan_preview_seen}"
echo "  auto-approved: ${plan_auto_approved}"
echo "  response:      ${stream_chunks}"

ok "Agent semantic skill search delegation E2E check passed"
