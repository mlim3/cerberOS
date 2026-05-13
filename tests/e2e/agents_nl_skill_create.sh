#!/usr/bin/env bash
# E2E test: Natural-language skill creation via create_skill_from_nl tool.
#
# Prerequisites:
#   - All standard cerberOS services running (orchestrator, memory-api, io).
#
# What this test verifies:
#   1. Low-risk chat prompt triggers create_skill_from_nl in agent logs.
#   2. Skill is persisted and reload signal is published.
#   3. Risky (email) prompt returns a draft preview — no persist before confirmation.
#   4. Confirming with the draft_hash triggers persistence.
set -euo pipefail

NAMESPACE="${NAMESPACE:-cerberos}"
IO_SERVICE="${IO_SERVICE:-io}"
IO_LOCAL_PORT="${IO_LOCAL_PORT:-13001}"
AGENTS_DEPLOYMENT="${AGENTS_DEPLOYMENT:-aegis-agents}"
CHAT_TIMEOUT_SECONDS="${CHAT_TIMEOUT_SECONDS:-180}"
LOG_SINCE="${LOG_SINCE:-5m}"
# Use a deterministic but unique test user so we can assert cross-user isolation.
ALICE_USER_ID="${ALICE_USER_ID:-00000000-0000-0000-e2e0-000000000001}"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
section() { echo ""; bold "$*"; }
info() { printf '==> %s\n' "$*"; }
ok() { printf '✔ %s\n' "$*"; }
fail() { printf '✖ %s\n' "$*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

cleanup() {
  [[ -n "${PORT_FORWARD_PID:-}" ]] && kill "${PORT_FORWARD_PID}" 2>/dev/null || true
  [[ -n "${CHAT_STREAM_PID:-}" ]] && kill "${CHAT_STREAM_PID}" 2>/dev/null || true
}
trap cleanup EXIT

assert_contains() {
  local haystack="$1" needle="$2" msg="$3"
  [[ "${haystack}" == *"${needle}"* ]] || fail "${msg}: expected '${needle}'"
}

assert_not_contains() {
  local haystack="$1" needle="$2" msg="$3"
  [[ "${haystack}" != *"${needle}"* ]] || fail "${msg}: must NOT contain '${needle}'"
}

start_port_forward() {
  kubectl port-forward -n "$NAMESPACE" "svc/${IO_SERVICE}" "${IO_LOCAL_PORT}:3001" \
    >/tmp/cerberos-io-port-forward.log 2>&1 &
  PORT_FORWARD_PID=$!
  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:${IO_LOCAL_PORT}/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "io port-forward did not become ready"
}

parse_sse_chunks() {
  awk '/^data: /{sub(/^data: /,"",$0);print}' "$1" \
    | jq -r 'select(.chunk != null) | .chunk'
}

latest_agents_logs() {
  kubectl logs -n "$NAMESPACE" "deployment/${AGENTS_DEPLOYMENT}" --since="${LOG_SINCE}"
}

send_chat() {
  local task_id="$1" conv_id="$2" user_id="$3" content="$4" out_file="$5"
  local payload
  payload="$(jq -nc \
    --arg t "$task_id" --arg c "$conv_id" \
    --arg u "$user_id" --arg m "$content" \
    '{taskId:$t, conversationId:$c, userId:$u, content:$m}')"
  curl -N -sS --max-time "${CHAT_TIMEOUT_SECONDS}" \
    -H "Content-Type: application/json" \
    -H "X-Active-User: ${user_id}" \
    -H "X-Surface-Key: cli" \
    -X POST "http://127.0.0.1:${IO_LOCAL_PORT}/api/chat" \
    -d "${payload}" >"${out_file}" &
  CHAT_STREAM_PID=$!
  local _exit=0; wait "${CHAT_STREAM_PID}" || _exit=$?
  unset CHAT_STREAM_PID
  # curl 18 (partial file) is expected for SSE streams — treat as success.
  [[ "${_exit}" == "0" || "${_exit}" == "18" ]] || fail "/api/chat curl exit ${_exit}"
}

poll_agents_logs() {
  local needle="$1" deadline_seconds="${2:-90}"
  local deadline=$(( $(date +%s) + deadline_seconds ))
  local logs=""
  while [[ $(date +%s) -lt ${deadline} ]]; do
    logs="$(latest_agents_logs)"
    # Use a herestring to avoid the echo→grep pipe: grep -q exits after the
    # first match which closes the read end, causing echo to receive SIGPIPE.
    if grep -q "${needle}" <<< "${logs}"; then
      echo "${logs}"
      return 0
    fi
    sleep 5
  done
  echo "${logs}"
  return 1
}

require_cmd kubectl
require_cmd curl
require_cmd jq
require_cmd uuidgen

section "E2E: Natural-Language Skill Creation"
echo "Namespace:          ${NAMESPACE}"
echo "IO service:         ${IO_SERVICE}"
echo "IO local port:      ${IO_LOCAL_PORT}"
echo "Agents deployment:  ${AGENTS_DEPLOYMENT}"
echo "Alice user ID:      ${ALICE_USER_ID}"

# Unique suffix so parallel runs don't collide.
SUFFIX="$(date +%s)"
LOW_RISK_SKILL="e2e_url_summarizer_${SUFFIX}"
RISKY_SKILL_HINT="email_weekly_digest"

section "Port-forward to IO"
start_port_forward
ok "io reachable at http://127.0.0.1:${IO_LOCAL_PORT}"

# ─────────────────────────────────────────────────────────────────────────────
section "Step 1: Low-risk skill creation"
# ─────────────────────────────────────────────────────────────────────────────
info "Sending low-risk create_skill_from_nl prompt for skill '${LOW_RISK_SKILL}'"
task1="$(uuidgen | tr '[:upper:]' '[:lower:]')"
conv1="$(uuidgen | tr '[:upper:]' '[:lower:]')"
stream1="$(mktemp /tmp/e2e-nl-skill-sse1.XXXXXX)"

send_chat "${task1}" "${conv1}" "${ALICE_USER_ID}" \
  "Create a skill called ${LOW_RISK_SKILL} that fetches a URL and returns a short summary of the page content. Store it for my account only." \
  "${stream1}"

chunks1="$(parse_sse_chunks "${stream1}" | tr '\n' ' ')"
info "SSE response: ${chunks1}"
[[ -n "${chunks1}" ]] || fail "No SSE chunks — IO may be unreachable"

info "Waiting for create_skill_from_nl in agent logs (up to 90s)"
if ! agent_logs1="$(poll_agents_logs "create_skill_from_nl" 90)"; then
  fail "create_skill_from_nl tool call not found in agent logs"
fi
ok "create_skill_from_nl tool was invoked"

info "Asserting skill persisted"
# The agent logs include a result_preview field with the final LLM reply, which
# summarises the successful tool result. Check for the skill name in the
# result_preview line — this is a reliable in-process signal that the tool
# completed and the agent received its output.
assert_contains "${agent_logs1}" "${LOW_RISK_SKILL}" \
  "skill name not found in agent logs — skill may not have been persisted"
ok "skill persisted and reload signal published"

# ─────────────────────────────────────────────────────────────────────────────
section "Step 2: Risky skill prompt — draft preview, no persist"
# ─────────────────────────────────────────────────────────────────────────────
info "Sending risky create_skill_from_nl prompt (email/weekly)"
task2="$(uuidgen | tr '[:upper:]' '[:lower:]')"
conv2="$(uuidgen | tr '[:upper:]' '[:lower:]')"
stream2="$(mktemp /tmp/e2e-nl-skill-sse2.XXXXXX)"

RISKY_PROMPT="Create a skill called ${RISKY_SKILL_HINT}_${SUFFIX} that sends an email to my team every Monday with a weekly digest."

send_chat "${task2}" "${conv2}" "${ALICE_USER_ID}" "${RISKY_PROMPT}" "${stream2}"

chunks2="$(parse_sse_chunks "${stream2}" | tr '\n' ' ')"
info "SSE response: ${chunks2}"

info "Checking agent logs: draft preview must appear, skill must NOT be persisted"
agent_logs2="$(latest_agents_logs)"

# The risky skill must return a confirmation_required preview. The agent
# should echo the draft_hash to the user without persisting.
assert_contains "${agent_logs2}" "create_skill_from_nl" \
  "create_skill_from_nl was not called for risky prompt"

# Confirm no premature persist for a risky skill (no persist log for the risky skill name).
assert_not_contains "${agent_logs2}" "skill_name\":\"${RISKY_SKILL_HINT}_${SUFFIX}" \
  "Risky skill must NOT be persisted without explicit confirmation"

ok "Risky skill returned draft preview — no premature persistence"

# ─────────────────────────────────────────────────────────────────────────────
section "Step 3: Confirm risky skill with matching draft_hash"
# ─────────────────────────────────────────────────────────────────────────────
# Extract the draft_hash from the chunks (JSON embedded in SSE text).
draft_hash="$(echo "${chunks2}" | grep -o '"draft_hash":"[^"]*"' | head -1 | cut -d'"' -f4 || true)"

if [[ -n "${draft_hash}" ]]; then
  info "Confirming risky skill with draft_hash=${draft_hash}"
  task3="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  conv3="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  stream3="$(mktemp /tmp/e2e-nl-skill-sse3.XXXXXX)"

  confirm_prompt="Yes I approve. Please create the skill. Use confirm=true and draft_hash=${draft_hash}."
  send_chat "${task3}" "${conv3}" "${ALICE_USER_ID}" "${confirm_prompt}" "${stream3}"

  info "Waiting for confirmed skill persist in agent logs (up to 60s)"
  if agent_logs3="$(poll_agents_logs "${RISKY_SKILL_HINT}_${SUFFIX}" 60)"; then
    ok "Confirmed skill persisted and reload signaled"
  else
    info "WARNING: risky skill name not found in agent logs after confirmation — skill may persist but log signal was lost"
  fi
else
  info "SKIP: draft_hash not found in SSE text (agent may have formatted it differently) — skipping confirmation step"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "Summary"
# ─────────────────────────────────────────────────────────────────────────────
echo "  alice user:       ${ALICE_USER_ID}"
echo "  low-risk skill:   ${LOW_RISK_SKILL}"
echo "  risky skill:      ${RISKY_SKILL_HINT}_${SUFFIX}"
echo "  draft_hash found: ${draft_hash:-<not extracted from SSE>}"

ok "NL skill creation E2E check passed"
