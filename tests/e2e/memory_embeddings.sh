#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-cerberos}"
MEMORY_SERVICE="${MEMORY_SERVICE:-memory-api}"
MEMORY_LOCAL_PORT="${MEMORY_LOCAL_PORT:-18081}"
DB_RESOURCE="${DB_RESOURCE:-statefulset/memory-db}"
DB_USER="${DB_USER:-user}"
DB_NAME="${DB_NAME:-memory_db}"
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
    NR==1 || /embedding-api|memory-api|memory-db/ {print}
  '
}

start_port_forward() {
  kubectl port-forward -n "$NAMESPACE" "svc/${MEMORY_SERVICE}" "${MEMORY_LOCAL_PORT}:8081" >/tmp/cerberos-memory-port-forward.log 2>&1 &
  PORT_FORWARD_PID=$!

  for _ in $(seq 1 30); do
    if curl -fsS "http://127.0.0.1:${MEMORY_LOCAL_PORT}/api/v1/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  if kill -0 "${PORT_FORWARD_PID}" >/dev/null 2>&1; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
  fi
  fail "memory-api port-forward did not become ready"
}

cleanup() {
  if [[ -n "${PORT_FORWARD_PID:-}" ]] && kill -0 "${PORT_FORWARD_PID}" >/dev/null 2>&1; then
    kill "${PORT_FORWARD_PID}" >/dev/null 2>&1 || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
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

require_cmd kubectl
require_cmd curl
require_cmd jq
require_cmd uuidgen

trap cleanup EXIT

section "E2E: Memory Embeddings"
echo "Namespace:     ${NAMESPACE}"
echo "Memory svc:    ${MEMORY_SERVICE}"
echo "Memory port:   ${MEMORY_LOCAL_PORT}"
echo "DB resource:   ${DB_RESOURCE}"
echo "Test user id:  ${TEST_USER_ID}"

section "Pod Status"
print_pod_table

section "Deployment Configuration"
info "Reading configured embedding settings from running deployments"
memory_model="$(jsonpath_env memory-api EMBEDDING_MODEL)"
memory_dim="$(jsonpath_env memory-api EMBEDDING_DIM)"
memory_prompt_style="$(jsonpath_env memory-api EMBEDDING_PROMPT_STYLE)"
embedding_model="$(jsonpath_env embedding-api MODEL_ID)"
embedding_dim="$(jsonpath_env embedding-api EMBEDDING_DIM)"

[[ -n "${memory_model}" ]] || fail "memory-api EMBEDDING_MODEL is empty"
[[ -n "${memory_dim}" ]] || fail "memory-api EMBEDDING_DIM is empty"
[[ -n "${memory_prompt_style}" ]] || fail "memory-api EMBEDDING_PROMPT_STYLE is empty"
[[ -n "${embedding_model}" ]] || fail "embedding-api MODEL_ID is empty"
[[ -n "${embedding_dim}" ]] || fail "embedding-api EMBEDDING_DIM is empty"

assert_eq "${memory_model}" "${embedding_model}" "memory-api and embedding-api model mismatch"
assert_eq "${memory_dim}" "${embedding_dim}" "memory-api and embedding-api dimension mismatch"

ok "Deployments agree on model=${memory_model} dim=${memory_dim} prompt_style=${memory_prompt_style}"
echo "memory-api:"
echo "  EMBEDDING_MODEL:        ${memory_model}"
echo "  EMBEDDING_DIM:          ${memory_dim}"
echo "  EMBEDDING_PROMPT_STYLE: ${memory_prompt_style}"
echo "embedding-api:"
echo "  MODEL_ID:               ${embedding_model}"
echo "  EMBEDDING_DIM:          ${embedding_dim}"

section "Database Schema"
info "Checking database vector column type"
db_vector_type="$(
  kubectl exec -n "$NAMESPACE" "$DB_RESOURCE" -- \
    psql -U "$DB_USER" -d "$DB_NAME" -Atc \
    "SELECT pg_catalog.format_type(a.atttypid, a.atttypmod)
     FROM pg_attribute a
     JOIN pg_class c ON a.attrelid = c.oid
     JOIN pg_namespace n ON c.relnamespace = n.oid
     WHERE n.nspname = 'personal_info_schema'
       AND c.relname = 'personal_info_chunks'
       AND a.attname = 'embedding';" | tr -d '\r'
)"

assert_eq "${db_vector_type}" "vector(${memory_dim})" "database embedding column type mismatch"
ok "Database column uses ${db_vector_type}"
echo "personal_info_schema.personal_info_chunks.embedding: ${db_vector_type}"

section "Memory API Health"
info "Starting temporary port-forward to memory-api"
start_port_forward
base_url="http://127.0.0.1:${MEMORY_LOCAL_PORT}/api/v1"
ok "memory-api is reachable at ${base_url}"
health_resp="$(curl -fsS "${base_url}/healthz")"
echo "${health_resp}" | jq .

source_id_1="$(uuidgen | tr '[:upper:]' '[:lower:]')"
source_id_2="$(uuidgen | tr '[:upper:]' '[:lower:]')"

payload_1="$(
  jq -nc \
    --arg content "Colby prefers PostgreSQL with pgvector for memory service work." \
    --arg sourceType "chat" \
    --arg sourceId "$source_id_1" \
    '{content:$content, sourceType:$sourceType, sourceId:$sourceId, extractFacts:true}'
)"

payload_2="$(
  jq -nc \
    --arg content "Colby uses Grafana to inspect cerberOS dashboards during debugging." \
    --arg sourceType "chat" \
    --arg sourceId "$source_id_2" \
    '{content:$content, sourceType:$sourceType, sourceId:$sourceId, extractFacts:true}'
)"

section "Save Memories"
info "Saving two memories with distinct semantic content"
save_resp_1="$(curl -fsS -X POST "${base_url}/personal_info/${TEST_USER_ID}/save" -H "Content-Type: application/json" -d "${payload_1}")"
save_resp_2="$(curl -fsS -X POST "${base_url}/personal_info/${TEST_USER_ID}/save" -H "Content-Type: application/json" -d "${payload_2}")"

chunk_count_1="$(echo "${save_resp_1}" | jq -r '.data.chunkIds | length')"
chunk_count_2="$(echo "${save_resp_2}" | jq -r '.data.chunkIds | length')"
if [[ "${chunk_count_1}" -lt 1 || "${chunk_count_2}" -lt 1 ]]; then
  fail "save did not return chunkIds"
fi
ok "Memory save returned chunk IDs"
echo "save #1 response:"
echo "${save_resp_1}" | jq .
echo "save #2 response:"
echo "${save_resp_2}" | jq .

section "Database Chunk Inspection"
latest_chunks="$(
  kubectl exec -n "$NAMESPACE" "$DB_RESOURCE" -- \
    psql -U "$DB_USER" -d "$DB_NAME" -AtF '|' -c \
    "SELECT model_version, vector_dims(embedding), left(raw_text, 100)
     FROM personal_info_schema.personal_info_chunks
     WHERE user_id = '${TEST_USER_ID}'
     ORDER BY created_at DESC
     LIMIT 5;" | tr -d '\r'
)"

if [[ -z "${latest_chunks}" ]]; then
  fail "no chunks found in database after save"
fi
echo "Latest chunk rows (model_version | vector_dims | raw_text preview):"
echo "${latest_chunks}" | sed 's/^/  /'
latest_model_version="$(echo "${latest_chunks}" | head -n 1 | cut -d'|' -f1)"
latest_vector_dim="$(echo "${latest_chunks}" | head -n 1 | cut -d'|' -f2)"
assert_eq "${latest_model_version}" "${memory_model}" "latest chunk model_version mismatch"
assert_eq "${latest_vector_dim}" "${memory_dim}" "latest chunk vector_dims mismatch"
ok "Stored chunks use model_version=${latest_model_version} and vector_dims=${latest_vector_dim}"

query_payload='{"query":"What database does Colby prefer for the memory service?","topK":3}'

section "Semantic Query"
info "Running semantic query"
query_resp="$(curl -fsS -X POST "${base_url}/personal_info/${TEST_USER_ID}/query" -H "Content-Type: application/json" -d "${query_payload}")"
top_text="$(echo "${query_resp}" | jq -r '.data.results[0].text // empty')"
top_score="$(echo "${query_resp}" | jq -r '.data.results[0].similarityScore // empty')"
result_count="$(echo "${query_resp}" | jq -r '.data.results | length')"
second_text="$(echo "${query_resp}" | jq -r '.data.results[1].text // empty')"
postgres_rank="$(echo "${query_resp}" | jq -r '
  (.data.results | to_entries[] | select(.value.text | contains("PostgreSQL with pgvector")) | .key + 1) // 0
')"
grafana_rank="$(echo "${query_resp}" | jq -r '
  (.data.results | to_entries[] | select(.value.text | contains("Grafana to inspect cerberOS dashboards")) | .key + 1) // 0
')"

[[ "${result_count}" -ge 1 ]] || fail "semantic query returned no results"
assert_contains "${top_text}" "PostgreSQL with pgvector" "top semantic result mismatch"
assert_eq "${postgres_rank}" "1" "PostgreSQL memory should rank first"
if [[ "${grafana_rank}" != "0" && "${grafana_rank}" -le "${postgres_rank}" ]]; then
  fail "Grafana distractor ranked too high: postgres_rank=${postgres_rank}, grafana_rank=${grafana_rank}"
fi
ok "Semantic search returned the expected top result (score=${top_score})"
echo "query response:"
echo "${query_resp}" | jq .

section "Summary"
echo "  model:        ${memory_model}"
echo "  dimensions:   ${memory_dim}"
echo "  prompt style: ${memory_prompt_style}"
echo "  db column:    ${db_vector_type}"
echo "  model_version:${latest_model_version}"
echo "  vector_dims:  ${latest_vector_dim}"
echo "  top result:   ${top_text}"
echo "  second result:${second_text}"
echo "  postgres rank:${postgres_rank}"
echo "  grafana rank: ${grafana_rank}"

ok "Memory embeddings E2E check passed"
