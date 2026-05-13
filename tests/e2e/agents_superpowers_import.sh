#!/usr/bin/env bash
# agents_superpowers_import.sh — e2e test for extracting Superpowers skill files via chat.
#
# What this test verifies:
#   1. A chat prompt with the Superpowers repo link triggers extract_skills_from_repo.
#   2. The live agent runtime picks up the imported skills.
#   3. The agent can actually use one of the imported skills in a later chat task.
#
# Prerequisites:
#   kubectl, curl, jq, rg, uuidgen
#
# This test is designed to run against the deployed cerberOS stack with
# a real IO service, orchestrator, memory, and aegis-agents deployment.
set -euo pipefail

NAMESPACE="${NAMESPACE:-cerberos}"
IO_SERVICE="${IO_SERVICE:-io}"
IO_LOCAL_PORT="${IO_LOCAL_PORT:-13001}"
AGENTS_DEPLOYMENT="${AGENTS_DEPLOYMENT:-aegis-agents}"
CHAT_TIMEOUT_SECONDS="${CHAT_TIMEOUT_SECONDS:-180}"
LOG_SINCE="${LOG_SINCE:-10m}"
TEST_USER_ID="${TEST_USER_ID:-00000000-0000-0000-0000-000000000001}"
SUPERPOWERS_REPO="${SUPERPOWERS_REPO:-github.com/obra/superpowers}"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
section() { echo ""; bold "$*"; }
info() { printf '==> %s\n' "$*"; }
ok() { printf '✔ %s\n' "$*"; }
fail() { printf '✖ %s\n' "$*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
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
  fail "io port-forward did not become ready"
}

latest_agents_logs() {
  kubectl logs -n "$NAMESPACE" "deployment/${AGENTS_DEPLOYMENT}" --since="$LOG_SINCE"
}

poll_agents_logs() {
  local needle="$1"
  local timeout_seconds="${2:-120}"
  local deadline=$(( $(date +%s) + timeout_seconds ))
  local logs=""
  while [[ $(date +%s) -lt ${deadline} ]]; do
    logs="$(latest_agents_logs)"
    if rg -q "${needle}" <<< "${logs}"; then
      echo "${logs}"
      return 0
    fi
    sleep 5
  done
  echo "${logs}"
  return 1
}

send_chat() {
  local task_id="$1"
  local conversation_id="$2"
  local content="$3"
  local out_file="$4"

  local payload
  payload="$(jq -nc \
    --arg taskId "$task_id" \
    --arg conversationId "$conversation_id" \
    --arg userId "$TEST_USER_ID" \
    --arg content "$content" \
    '{taskId:$taskId, conversationId:$conversationId, userId:$userId, content:$content}')"

  curl -N -sS --max-time "${CHAT_TIMEOUT_SECONDS}" \
    -H "Content-Type: application/json" \
    -H "X-Active-User: ${TEST_USER_ID}" \
    -H "X-Surface-Key: cli" \
    -X POST "http://127.0.0.1:${IO_LOCAL_PORT}/api/chat" \
    -d "${payload}" >"${out_file}" &
  CHAT_STREAM_PID=$!

  local wait_status=0
  wait "${CHAT_STREAM_PID}" || wait_status=$?
  unset CHAT_STREAM_PID
  if [[ "${wait_status}" != "0" && "${wait_status}" != "18" ]]; then
    fail "/api/chat curl exit ${wait_status}"
  fi
}

cleanup() {
  if [[ -n "${PORT_FORWARD_PID:-}" ]]; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
  fi
  if [[ -n "${CHAT_STREAM_PID:-}" ]]; then
    kill "${CHAT_STREAM_PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

require_cmd kubectl
require_cmd curl
require_cmd jq
require_cmd rg
require_cmd uuidgen

section "E2E: Superpowers import"
echo "Namespace:          ${NAMESPACE}"
echo "IO service:         ${IO_SERVICE}"
echo "IO local port:      ${IO_LOCAL_PORT}"
echo "Agents deployment:  ${AGENTS_DEPLOYMENT}"
echo "Test user id:       ${TEST_USER_ID}"
echo "Superpowers repo:   ${SUPERPOWERS_REPO}"
echo "Log window:         ${LOG_SINCE}"

section "Port-forward to IO"
start_port_forward
ok "io reachable at http://127.0.0.1:${IO_LOCAL_PORT}"

section "Extract Superpowers skills via chat"
task_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
conversation_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
stream_file="$(mktemp /tmp/cerberos-superpowers-extract.XXXXXX)"

send_chat "${task_id}" "${conversation_id}" \
  "Here is a public GitHub repository: ${SUPERPOWERS_REPO}. Extract the skills from it and make them available to me." \
  "${stream_file}"

chat_text="$(awk '/^data: /{sub(/^data: /,"",$0); print}' "${stream_file}" | tr '\n' ' ')"
echo "${chat_text}" | jq -r 'select(.chunk != null) | .chunk' | sed 's/^/  | /'

info "Waiting for extract_skills_from_repo in agent logs (up to 120s)"
if ! import_logs="$(poll_agents_logs "extract_skills_from_repo" 120)"; then
  fail "extract_skills_from_repo tool call not found in agent logs"
fi
ok "extract_skills_from_repo tool was invoked"

info "Waiting for persisted Superpowers skills to appear in logs (up to 120s)"
if ! import_logs="$(poll_agents_logs "writing_skills" 120)"; then
  fail "imported skill names not found in agent logs"
fi
assert_contains "${import_logs}" "using_superpowers" "agent import logs"
assert_contains "${import_logs}" "writing_skills" "agent import logs"
ok "Superpowers import persisted the expected skills"

first_skill_name="using_superpowers"
second_skill_name="writing_skills"

section "Wait for agent reload"
if ! reload_logs="$(poll_agents_logs "skill reload signal received; re-hydrating synthesized skills" 120)"; then
  fail "agent did not log a synthesized-skill reload after import"
fi
ok "agent reload observed"

assert_contains "${reload_logs}" "${first_skill_name}" "agent reload logs"

section "Use an imported skill"
skill_to_use="${second_skill_name:-${first_skill_name}}"
[[ -n "${skill_to_use}" ]] || fail "could not determine an imported skill name to use"
info "Requesting the agent to use imported skill: ${skill_to_use}"

task_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
conversation_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
stream_file="$(mktemp /tmp/cerberos-superpowers-sse.XXXXXX)"

send_chat "${task_id}" "${conversation_id}" \
  "Use the imported ${skill_to_use} skill to help me understand the Superpowers repo and then answer in one short paragraph. Make sure you actually use the ${skill_to_use} skill." \
  "${stream_file}"

chat_text="$(awk '/^data: /{sub(/^data: /,"",$0); print}' "${stream_file}" | tr '\n' ' ')"
echo "${chat_text}" | jq -r 'select(.chunk != null) | .chunk' | sed 's/^/  | /'

info "Searching agent logs for an invocation of ${skill_to_use}"
if ! invocation_logs="$(poll_agents_logs "${skill_to_use}" 120)"; then
  fail "did not find ${skill_to_use} in agent logs"
fi
assert_contains "${invocation_logs}" "${skill_to_use}" "agent invocation logs"
ok "imported skill was invoked by the agent"

section "Summary"
echo "  first_skill:     ${first_skill_name:-<none>}"
echo "  second_skill:    ${second_skill_name:-<none>}"
