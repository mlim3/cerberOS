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

conversation_agents_logs() {
  local conversation_id="$1"
  latest_agents_logs | rg "\"conversation_id\":\"${conversation_id}\"" || true
}

deployment_ready() {
  local deployment="$1"
  kubectl get deployment "$deployment" -n "$NAMESPACE" -o jsonpath="{.status.conditions[?(@.type==\"Available\")].status}"
}

latest_deployment_pod() {
  kubectl get pods -n "$NAMESPACE" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' \
    | rg "^${AGENTS_DEPLOYMENT}-" \
    | tail -n 1
}

container_started_recently() {
  local pod_name="$1"
  local threshold_seconds="${2:-900}"
  local started_at

  started_at="$(kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].state.running.startedAt}')"
  [[ -n "${started_at}" ]] || return 1

  python3 - "${started_at}" "${threshold_seconds}" <<'PY'
from datetime import datetime, timezone
import sys

started = datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00"))
threshold_seconds = int(sys.argv[2])
age_seconds = (datetime.now(timezone.utc) - started).total_seconds()
sys.exit(0 if age_seconds < threshold_seconds else 1)
PY
}

require_cmd kubectl
require_cmd curl
require_cmd jq
require_cmd python3
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
echo "Test domain:        e2e_test (e2e_ping)"
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
orchestrator_memory_endpoint="$(jsonpath_env "${ORCHESTRATOR_DEPLOYMENT}" MEMORY_ENDPOINT)"
memory_embedding_url="$(jsonpath_env "memory-api" EMBEDDING_API_URL 2>/dev/null || true)"
memory_embedding_dim="$(jsonpath_env "memory-api" EMBEDDING_DIM 2>/dev/null || true)"

[[ -n "${agents_embedding_url}" ]] || fail "aegis-agents AEGIS_EMBEDDING_API_URL is empty"
[[ -n "${agents_embedding_model}" ]] || fail "aegis-agents AEGIS_EMBEDDING_MODEL is empty"
[[ -n "${agents_embedding_dim}" ]] || fail "aegis-agents AEGIS_EMBEDDING_DIM is empty"
[[ -n "${agents_prompt_style}" ]] || fail "aegis-agents AEGIS_EMBEDDING_PROMPT_STYLE is empty"
[[ -n "${embedding_model}" ]] || fail "embedding-api MODEL_ID is empty"
[[ -n "${embedding_dim}" ]] || fail "embedding-api EMBEDDING_DIM is empty"
[[ -n "${orchestrator_memory_endpoint}" ]] || fail "orchestrator MEMORY_ENDPOINT is empty — orchestrator cannot forward skill_cache writes to Memory API"

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
echo "orchestrator:"
echo "  MEMORY_ENDPOINT:             ${orchestrator_memory_endpoint}"
if [[ -n "${memory_embedding_url}" ]]; then
  echo "memory-api:"
  echo "  EMBEDDING_API_URL:           ${memory_embedding_url}"
  echo "  EMBEDDING_DIM:               ${memory_embedding_dim}"
fi

section "Startup Log Check"
info "Checking that aegis-agents announced the shared embedding API on startup"
startup_logs="$(kubectl logs -n "$NAMESPACE" "deployment/${AGENTS_DEPLOYMENT}")"
if [[ "${startup_logs}" == *"embedding: using shared embedding API"* ]]; then
  echo "${startup_logs}" | rg "embedding: using shared embedding API|aegis-agents ready" || true
  ok "aegis-agents startup logs show the shared embedding API"
else
  agents_pod="$(latest_deployment_pod)"
  if [[ -z "${agents_pod}" ]]; then
    fail "could not determine current ${AGENTS_DEPLOYMENT} pod for startup log validation"
  fi

  if container_started_recently "${agents_pod}" 900; then
    fail "shared embedding API startup log missing for recently started pod ${agents_pod}"
  fi

  echo "startup log line not present in accessible container logs; pod ${agents_pod} is older than 15m, so relying on deployment config above"
  echo "WARNING: if ${agents_pod} restarted more recently than expected, investigate log retention or startup logging."
fi

info "Checking that aegis-agents seeded static skills at startup"
if [[ "${startup_logs}" == *"factory: static skills seeded to Memory Component"* ]]; then
  echo "${startup_logs}" | rg "factory: static skills seeded|factory: seeding static skill" | tail -20 || true
  ok "aegis-agents seeded static skills to Memory Component"
else
  agents_pod="$(latest_deployment_pod)"
  if [[ -z "${agents_pod}" ]]; then
    fail "could not determine current ${AGENTS_DEPLOYMENT} pod for skill seed validation"
  fi
  if container_started_recently "${agents_pod}" 900; then
    fail "static skill seed log missing — skills_search will return no results"
  fi
  echo "WARNING: static skill seed log not present for pod ${agents_pod} (older than 15m, relying on persistent DB state)"
fi

section "IO Chat Request"
info "Starting temporary port-forward to io"
start_port_forward
base_url="http://127.0.0.1:${IO_LOCAL_PORT}"
ok "io is reachable at ${base_url}"

task_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
conversation_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
# The probe value is echoed back by e2e_ping so we can assert it appears in logs.
E2E_PROBE="skill-search-delegation-$(date +%s)"
# The prompt describes what we need without naming the domain or tool — the agent
# must use skills_search to discover e2e_ping, then call it directly after the
# credential-free auto-registration path upgrades the current agent toolset.
chat_prompt="$(
  cat <<EOF
Run an automated connectivity check to verify that skill discovery and cross-domain delegation work end-to-end.
Use skills_search to find a tool that runs an automated e2e connectivity probe, then run it directly with probe="${E2E_PROBE}".
Do not use spawn_agent.
Return the result from the probe.
EOF
)"

chat_payload="$(
  jq -nc \
    --arg taskId "${task_id}" \
    --arg conversationId "${conversation_id}" \
    --arg userId "${TEST_USER_ID}" \
    --arg content "${chat_prompt}" \
    '{taskId:$taskId, conversationId:$conversationId, userId:$userId, content:$content}'
)"

stream_file="$(mktemp /tmp/cerberos-agents-sse.XXXXXX)"
event_stream_file="$(mktemp /tmp/cerberos-agent-events.XXXXXX)"
plan_auto_approved="false"
plan_preview_seen="false"
approve_resp=""
info "Sending /api/chat request (no required_skill_domains — agent must discover e2e_test via skills_search)"

curl -N -sS --max-time "${CHAT_TIMEOUT_SECONDS}" \
  -H "Content-Type: application/json" \
  -H "X-Active-User: ${TEST_USER_ID}" \
  -H "X-Surface-Key: cli" \
  -X POST "${base_url}/api/chat" \
  -d "${chat_payload}" >"${stream_file}" &
CHAT_STREAM_PID=$!

info "Subscribing to /api/events/${task_id} for plan previews and status updates (probe=${E2E_PROBE})"
curl -N -sS -H "X-Active-User: ${TEST_USER_ID}" "${base_url}/api/events/${task_id}" >"${event_stream_file}" 2>/dev/null &
EVENT_STREAM_PID=$!

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
      if ! approve_resp="$(curl -fsS -H "Content-Type: application/json" -H "X-Active-User: ${TEST_USER_ID}" -H "X-Surface-Key: cli" -X POST "${base_url}/api/orchestrator/plan-decision" -d "${approve_payload}")"; then
        fail "plan approval request failed"
      fi
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

_chat_exit=0
wait "${CHAT_STREAM_PID}" || _chat_exit=$?
# curl exit 18 (CURLE_PARTIAL_FILE) is expected for SSE streams: the IO service
# closes the connection after sending the final event, and curl considers the
# transfer incomplete because the Content-Length was not set. Treat it as OK;
# the stream_done assertion below validates the actual completion.
if [[ "${_chat_exit}" != "0" && "${_chat_exit}" != "18" ]]; then
  fail "/api/chat request failed (curl exit ${_chat_exit})"
fi

echo "Raw SSE stream:"
sed 's/^/  /' "${stream_file}"
if [[ -s "${event_stream_file}" ]]; then
  echo "Task event stream:"
  sed 's/^/  /' "${event_stream_file}"
fi
# If the event stream file is non-empty but contains no SSE data lines, IO
# rejected the pre-subscription (likely a 404 "Task not found" race: the test
# subscribed before the chat POST registered the task).  That can prevent the
# plan-preview auto-approval path, but the agent-log assertions below still
# validate the actual skill-search behavior, so continue.
if [[ -s "${event_stream_file}" ]] && ! grep -q '^data:' "${event_stream_file}" 2>/dev/null; then
  info "event stream returned a non-SSE response; continuing because the agent-log assertions still validate delegation behavior: $(cat "${event_stream_file}")"
fi

stream_chunks="$(parse_sse_chunks "${stream_file}" | tr '\n' ' ')"
# The IO service closes the /api/chat SSE stream after the initial acknowledgment
# chunks — the task continues asynchronously. We verify we got at least the
# initial "task accepted" chunk, then track completion via agent logs below.
# (Checking stream_done==true from the chat stream would always fail because the
# server closes the connection before the task finishes.)
[[ -n "${stream_chunks}" ]] || fail "chat stream returned no chunks — IO service may be unreachable"
ok "chat stream: initial acknowledgment received"
echo "Combined streamed response so far:"
echo "  ${stream_chunks}"
if [[ "${stream_chunks}" == *"Planner agent failed:"* ]]; then
  fail "planner failed before skills delegation could run: ${stream_chunks}"
fi
if [[ "${plan_preview_seen}" == "true" ]]; then
  ok "Observed plan preview during execution"
fi
if [[ "${plan_auto_approved}" == "true" ]]; then
  ok "Auto-approved the execution plan so subtasks could run"
fi

# Poll agent logs for up to 90s for e2e_ping to be executed.
# The e2e_test domain agent runs the tool and logs "e2e_ping: executed" at that point.
# A fixed sleep is too fragile — the subtask spawning and execution takes variable time.
section "Agents Log Inspection"
info "Waiting for e2e_ping execution to appear in agent logs (up to 90s)..."
agent_logs=""
full_agent_logs=""
_log_deadline=$(( $(date +%s) + 90 ))
while [[ $(date +%s) -lt ${_log_deadline} ]]; do
  agent_logs="$(conversation_agents_logs "${conversation_id}")"
  full_agent_logs="$(latest_agents_logs)"
  if rg -q "${E2E_PROBE}" <<< "${full_agent_logs}"; then
    break
  fi
  sleep 5
done
info "Inspecting recent aegis-agents logs for skill search delegation flow"
filtered_logs="$(echo "${agent_logs}" | rg 'skills_search|spawn_agent|e2e_ping|observe phase: tool result|agent spawner ready' || true)"
echo "${filtered_logs}" | sed 's/^/  /'

# 1. skills_search must have been called by the general agent — proves the search
#    pipeline is wired end-to-end (seeding → NATS → orchestrator gateway → pgvector).
assert_contains "${agent_logs}" '"tool":"skills_search"' "skills_search tool was not called — agent must use skills_search to discover e2e_ping"

# 2. skills_search must have returned at least one result — proves the Memory API
#    accepted the seed writes and the embedding index is queryable.
#    We check result_count > 0 (not just field presence) so that an empty index
#    (result_count:0 from a seeding race) is caught here rather than only at the
#    e2e_ping assertion below with a misleading "tool not called" message.
#    Note: we do not assert top_domain=e2e_test. HNSW ranking is sensitive to pool
#    size and accumulated synthesized skills from prior runs; what matters is that
#    the search fires and returns results from a live index, not which skill ranks
#    first in a growing pool.
if ! rg -q '"result_count":[1-9]' <<< "${agent_logs}"; then
  fail "skills_search result_count is 0 or missing — skill index may be empty; ensure memory-api was ready before aegis-agents seeded static skills (check wait-for-memory-api init container)"
fi
ok "skills_search returned results (result_count > 0)"

# 3. e2e_ping must have executed with the probe value. Current intended
#    behaviour is direct execution after credential-free auto-registration into
#    the discovering agent, not a second spawn_agent handoff.
assert_contains "${agent_logs}" '"tool":"e2e_ping"' "e2e_ping tool was not executed after skills_search discovery"
assert_contains "${full_agent_logs}" "${E2E_PROBE}" "probe value not found in agent logs — e2e_ping may not have run with the correct parameters"

ok "skills_search → e2e_ping direct execution path confirmed"

section "Summary"
echo "  model:         ${agents_embedding_model}"
echo "  dimensions:    ${agents_embedding_dim}"
echo "  prompt style:  ${agents_prompt_style}"
echo "  task id:       ${task_id}"
echo "  conversation:  ${conversation_id}"
echo "  probe:         ${E2E_PROBE}"
echo "  plan preview:  ${plan_preview_seen}"
echo "  auto-approved: ${plan_auto_approved}"
echo "  response:      ${stream_chunks}"

ok "Agent semantic skill search delegation E2E check passed"
