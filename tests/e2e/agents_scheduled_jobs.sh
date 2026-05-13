#!/usr/bin/env bash
# agents_scheduled_jobs.sh — e2e checks for agent-driven scheduled job creation
# and listing. Confirms chat requests route through create_scheduled_job /
# list_scheduled_jobs instead of any hardcoded scheduling wizard, and that the
# created job persists in the user-crons API.
set -euo pipefail

NAMESPACE="${NAMESPACE:-cerberos}"
IO_SERVICE="${IO_SERVICE:-io}"
IO_LOCAL_PORT="${IO_LOCAL_PORT:-13001}"
AGENTS_DEPLOYMENT="${AGENTS_DEPLOYMENT:-aegis-agents}"
CHAT_TIMEOUT_SECONDS="${CHAT_TIMEOUT_SECONDS:-180}"
LOG_SINCE="${LOG_SINCE:-5m}"
TEST_USER_ID="${TEST_USER_ID:-00000000-0000-0000-0000-000000000001}"

bold()    { printf '\033[1m%s\033[0m\n' "$*"; }
section() { echo ""; bold "$*"; }
info()    { printf '==> %s\n' "$*"; }
ok()      { printf '✔ %s\n' "$*"; }
fail()    { printf '✖ %s\n' "$*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

assert_contains() {
  local haystack="$1" needle="$2" label="$3"
  if ! rg -q "${needle}" <<< "${haystack}"; then
    fail "${label}: expected to find '${needle}'"
  fi
  ok "${label}"
}

cleanup() {
  if [[ -n "${PF_PID:-}" ]]; then
    kill "${PF_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
  fi
  if [[ -n "${EVENT_PID:-}" ]]; then
    kill "${EVENT_PID}" 2>/dev/null || true
    wait "${EVENT_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SCHED_JOB_ID:-}" ]]; then
    curl -fsS \
      -H "X-Active-User: ${TEST_USER_ID}" \
      -H "X-Surface-Key: cli" \
      -X DELETE "http://localhost:${IO_LOCAL_PORT}/api/user-crons/${SCHED_JOB_ID}" \
      >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

submit_and_wait() {
  local message="$1"
  local timeout="${2:-${CHAT_TIMEOUT_SECONDS}}"

  local task_id conversation_id
  task_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  conversation_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"

  local payload
  payload="$(jq -nc \
    --arg taskId "${task_id}" \
    --arg conversationId "${conversation_id}" \
    --arg userId "${TEST_USER_ID}" \
    --arg content "${message}" \
    '{taskId:$taskId, conversationId:$conversationId, userId:$userId, content:$content}')"

  local event_file stream_file
  event_file="$(mktemp /tmp/cerberos-sched-events.XXXXXX)"
  stream_file="$(mktemp /tmp/cerberos-sched-stream.XXXXXX)"

  curl -N -sS \
    -H "X-Active-User: ${TEST_USER_ID}" \
    "http://localhost:${IO_LOCAL_PORT}/api/events/${task_id}" \
    >"${event_file}" 2>/dev/null &
  EVENT_PID=$!

  curl -N -sS --max-time "${timeout}" \
    -H "Content-Type: application/json" \
    -H "X-Active-User: ${TEST_USER_ID}" \
    -H "X-Surface-Key: cli" \
    -X POST "http://localhost:${IO_LOCAL_PORT}/api/chat" \
    -d "${payload}" >"${stream_file}" &
  local chat_pid=$!

  local deadline=$(( $(date +%s) + 30 ))
  while [[ $(date +%s) -lt ${deadline} ]]; do
    local orch_ref
    orch_ref="$(
      awk '/^data: / { sub(/^data: /, "", $0); print }' "${event_file}" \
        | jq -r 'select(.type=="plan_preview") | .payload.orchestratorTaskRef' \
        | tail -n 1
    )"
    if [[ -n "${orch_ref}" && "${orch_ref}" != "null" ]]; then
      local approve_payload
      approve_payload="$(jq -nc --arg taskId "${task_id}" --arg ref "${orch_ref}" \
        '{taskId:$taskId, orchestratorTaskRef:$ref, approved:true}')"
      curl -fsS \
        -H "Content-Type: application/json" \
        -H "X-Active-User: ${TEST_USER_ID}" \
        -H "X-Surface-Key: cli" \
        -X POST "http://localhost:${IO_LOCAL_PORT}/api/orchestrator/plan-decision" \
        -d "${approve_payload}" >/dev/null 2>&1 || true
      break
    fi
    sleep 2
  done

  local chat_exit=0
  wait "${chat_pid}" || chat_exit=$?
  if [[ "${chat_exit}" != "0" && "${chat_exit}" != "18" ]]; then
    fail "/api/chat request failed (curl exit ${chat_exit})"
  fi

  kill "${EVENT_PID}" 2>/dev/null || true
  wait "${EVENT_PID}" 2>/dev/null || true

  if [[ -s "${event_file}" ]] && ! grep -q '^data:' "${event_file}" 2>/dev/null; then
    info "event stream returned a non-SSE response; continuing because scheduled-job assertions rely on agent logs: $(cat "${event_file}")"
  fi

  local chunks
  chunks="$(awk '/^data: / { sub(/^data: /, "", $0); print }' "${stream_file}" \
    | jq -r 'select(.chunk != null) | .chunk' | tr '\n' ' ')"
  [[ -n "${chunks}" ]] || fail "chat stream returned no chunks"

  rm -f "${event_file}" "${stream_file}"
  echo "${chunks}"
}

collect_agent_logs() {
  kubectl logs \
    -n "${NAMESPACE}" \
    "deployment/${AGENTS_DEPLOYMENT}" \
    --since="${LOG_SINCE}" \
    2>/dev/null || true
}

wait_for_log() {
  local needle="$1"
  local timeout="${2:-60}"
  local deadline=$(( $(date +%s) + timeout ))
  local logs=""
  while [[ $(date +%s) -lt ${deadline} ]]; do
    logs="$(collect_agent_logs)"
    if rg -q "${needle}" <<< "${logs}"; then
      echo "${logs}"
      return 0
    fi
    sleep 3
  done
  echo "${logs}"
  return 1
}

require_cmd kubectl
require_cmd curl
require_cmd jq
require_cmd python3
require_cmd rg
require_cmd uuidgen

section "Setting up port-forward to IO service"
kubectl port-forward -n "${NAMESPACE}" "service/${IO_SERVICE}" "${IO_LOCAL_PORT}:3001" >/dev/null 2>&1 &
PF_PID=$!
sleep 2
curl -sf "http://localhost:${IO_LOCAL_PORT}/health" >/dev/null \
  || fail "IO service not reachable at localhost:${IO_LOCAL_PORT}"
ok "IO service reachable"

JOB_NAME="e2e-scheduled-job-$(date +%s)"
PROBE_VALUE="schedule-probe-$(date +%s)"
FIRST_RUN_AT="$(python3 - <<'PY'
from datetime import datetime, timedelta, timezone
print((datetime.now(timezone.utc) + timedelta(hours=2)).replace(microsecond=0).isoformat().replace("+00:00", "Z"))
PY
)"

section "Creating scheduled job through the agent"
create_prompt="$(
  cat <<EOF
Create a scheduled job named "${JOB_NAME}".
It should run every 3600 seconds.
On each run, the task should say exactly: "${PROBE_VALUE}".
Set first_run_at to "${FIRST_RUN_AT}".
Use create_scheduled_job.
EOF
)"

create_response="$(submit_and_wait "${create_prompt}")"
assert_contains "${create_response}" "accepted" "chat stream acknowledged the scheduling request"

agent_logs="$(wait_for_log '"tool":"create_scheduled_job"' 90)" \
  || fail "create_scheduled_job did not appear in agent logs"
assert_contains "${agent_logs}" '"tool":"create_scheduled_job"' "agent invoked create_scheduled_job"

section "Checking persisted scheduled job"
user_crons_json="$(curl -fsS \
  -H "X-Active-User: ${TEST_USER_ID}" \
  -H "X-Surface-Key: cli" \
  "http://localhost:${IO_LOCAL_PORT}/api/user-crons?userId=${TEST_USER_ID}")"

SCHED_JOB_ID="$(
  echo "${user_crons_json}" \
    | jq -r --arg name "${JOB_NAME}" '.data.jobs[] | select(.name == $name) | .id' \
    | head -n 1
)"
[[ -n "${SCHED_JOB_ID}" && "${SCHED_JOB_ID}" != "null" ]] \
  || fail "created scheduled job ${JOB_NAME} not found in /api/user-crons"
ok "scheduled job persisted with id ${SCHED_JOB_ID}"

section "Listing scheduled jobs through the agent"
list_prompt="List my scheduled jobs and include the entry for ${JOB_NAME}. Use list_scheduled_jobs."
submit_and_wait "${list_prompt}" >/dev/null

agent_logs="$(wait_for_log '"tool":"list_scheduled_jobs"' 60)" \
  || fail "list_scheduled_jobs did not appear in agent logs"
assert_contains "${agent_logs}" '"tool":"list_scheduled_jobs"' "agent invoked list_scheduled_jobs"

section "Summary"
echo "  job name:      ${JOB_NAME}"
echo "  job id:        ${SCHED_JOB_ID}"
echo "  first run at:  ${FIRST_RUN_AT}"
echo "  probe value:   ${PROBE_VALUE}"

ok "Agent-driven scheduled job creation/listing E2E check passed"
