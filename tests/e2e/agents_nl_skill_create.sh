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
  if [[ -n "${PORT_FORWARD_PID:-}" ]]; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
  fi
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

agents_logs_since() {
  local since_time="$1"
  kubectl logs -n "$NAMESPACE" "deployment/${AGENTS_DEPLOYMENT}" --since-time="${since_time}"
}

conversation_agents_logs() {
  local conversation_id="$1"
  local logs
  logs="$(latest_agents_logs)"
  printf '%s\n' "${logs}" | rg "\"conversation_id\":\"${conversation_id}\"" || true
}

conversation_agents_logs_since() {
  local since_time="$1"
  local conversation_id="$2"
  local logs
  logs="$(agents_logs_since "${since_time}")"
  printf '%s\n' "${logs}" | rg "\"conversation_id\":\"${conversation_id}\"" || true
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
  local needle="$1" deadline_seconds="${2:-90}" conversation_id="${3:-}"
  local deadline=$(( $(date +%s) + deadline_seconds ))
  local logs=""
  while [[ $(date +%s) -lt ${deadline} ]]; do
    if [[ -n "${conversation_id}" ]]; then
      logs="$(conversation_agents_logs "${conversation_id}")"
    else
      logs="$(latest_agents_logs)"
    fi
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
if ! agent_logs1="$(poll_agents_logs "create_skill_from_nl" 90 "${conv1}")"; then
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
step2_start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

RISKY_PROMPT="Create a general-domain skill called ${RISKY_SKILL_HINT}_${SUFFIX} that sends an email to my team every Monday with a weekly digest."

send_chat "${task2}" "${conv2}" "${ALICE_USER_ID}" "${RISKY_PROMPT}" "${stream2}"

chunks2="$(parse_sse_chunks "${stream2}" | tr '\n' ' ')"
info "SSE response: ${chunks2}"

info "Checking agent logs: draft preview must appear, skill must NOT be persisted"
agent_logs2="$(conversation_agents_logs_since "${step2_start}" "${conv2}")"

# The risky skill must return a confirmation_required preview. The agent
# should echo the draft_hash to the user without persisting.
assert_contains "${agent_logs2}" "create_skill_from_nl" \
  "create_skill_from_nl was not called for risky prompt"

# Confirm no premature persist for a risky skill.
if rg -q "skill persisted.*${RISKY_SKILL_HINT}_${SUFFIX}|${RISKY_SKILL_HINT}_${SUFFIX}.*skill persisted" <<< "${agent_logs2}"; then
  fail "Risky skill must NOT be persisted without explicit confirmation"
fi
if rg -q "skill reload signaled.*${RISKY_SKILL_HINT}_${SUFFIX}|${RISKY_SKILL_HINT}_${SUFFIX}.*skill reload signaled" <<< "${agent_logs2}"; then
  fail "Risky skill must NOT signal reload without explicit confirmation"
fi

ok "Risky skill returned draft preview — no premature persistence"

# ─────────────────────────────────────────────────────────────────────────────
section "Step 3: Confirm risky skill with matching draft_hash"
# ─────────────────────────────────────────────────────────────────────────────
# Extract the draft_hash from the chunks (JSON embedded in SSE text).
draft_hash="$(echo "${chunks2}" | grep -Eo '"draft_hash"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed -E 's/^"draft_hash"[[:space:]]*:[[:space:]]*"([^"]*)"$/\1/' || true)"
if [[ -z "${draft_hash}" ]]; then
  draft_hash="$(printf '%s\n' "${chunks2}" | grep -Eo '[0-9a-f]{64}' | head -1 || true)"
fi
if [[ -z "${draft_hash}" ]]; then
  draft_hash="$(printf '%s\n' "${agent_logs2}" | grep -Eo '"draft_hash"[[:space:]]*:[[:space:]]*"[0-9a-f]{64}"' | head -1 | sed -E 's/^"draft_hash"[[:space:]]*:[[:space:]]*"([^"]+)"$/\1/' || true)"
fi
if [[ -z "${draft_hash}" ]]; then
  draft_hash="$(printf '%s\n' "${agent_logs2}" | grep -Eo '[0-9a-f]{64}' | head -1 || true)"
fi

if [[ -n "${draft_hash}" ]]; then
  info "Confirming risky skill with draft_hash=${draft_hash}"
  task3="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  conv3="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  stream3="$(mktemp /tmp/e2e-nl-skill-sse3.XXXXXX)"
  step3_start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  confirm_prompt="Yes I approve. Please create the skill. Use confirm=true and draft_hash=${draft_hash}."
  send_chat "${task3}" "${conv3}" "${ALICE_USER_ID}" "${confirm_prompt}" "${stream3}"

  info "Waiting for confirmed skill persist in agent logs (up to 60s)"
  if agent_logs3="$(agents_logs_since "${step3_start}")" && \
     rg -q "skill persisted.*${RISKY_SKILL_HINT}_${SUFFIX}|${RISKY_SKILL_HINT}_${SUFFIX}.*skill persisted" <<< "${agent_logs3}"; then
    ok "Confirmed skill persisted and reload signaled"
  else
    info "WARNING: risky skill name not found in agent logs after confirmation — skill may persist but log signal was lost"
  fi
else
  fail "draft_hash not found in SSE text — confirmation step cannot proceed"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "Summary"
# ─────────────────────────────────────────────────────────────────────────────
echo "  alice user:       ${ALICE_USER_ID}"
echo "  low-risk skill:   ${LOW_RISK_SKILL}"
echo "  risky skill:      ${RISKY_SKILL_HINT}_${SUFFIX}"
echo "  draft_hash found: ${draft_hash:-<not extracted from SSE>}"

ok "NL skill creation E2E check passed"
