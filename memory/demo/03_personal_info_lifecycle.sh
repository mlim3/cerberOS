#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$THIS_DIR/common.sh"

require_basics
ensure_api_up

bold "Demo 03: Personal info lifecycle"

source_id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
save_payload="$(jq -nc \
  --arg content "Colby prefers PostgreSQL with pgvector for memory service work." \
  --arg sourceType "chat" \
  --arg sourceId "$source_id" \
  '{content:$content, sourceType:$sourceType, sourceId:$sourceId, extractFacts:true}')"

info "1) Save memory content"
save_resp="$(curl -sS -X POST "$BASE_URL/personal_info/$DEMO_USER_ID/save" \
  -H "Content-Type: application/json" \
  -d "$save_payload")"
echo "$save_resp" | jq .

info "2) Semantic query"
query_payload='{"query":"What database does Colby prefer?","topK":3}'
curl -sS -X POST "$BASE_URL/personal_info/$DEMO_USER_ID/query" \
  -H "Content-Type: application/json" \
  -d "$query_payload" | jq .

info "3) Export all facts and chunks"
all_resp="$(curl -sS "$BASE_URL/personal_info/$DEMO_USER_ID/all")"
echo "$all_resp" | jq .

fact_id="$(echo "$all_resp" | jq -r '.data.facts[0].factId // empty')"
if [[ -z "$fact_id" ]]; then
  err "No fact found to update/delete; stopping here."
  exit 1
fi

version="$(echo "$all_resp" | jq -r '.data.facts[0].version')"

info "4) Update a fact with optimistic concurrency"
update_payload="$(jq -nc \
  --arg category "Preferences" \
  --arg factKey "memory_database_choice" \
  --argjson factValue '"PostgreSQL with pgvector"' \
  --argjson confidence 0.97 \
  --argjson version "$version" \
  '{category:$category,factKey:$factKey,factValue:$factValue,confidence:$confidence,version:$version}')"
curl -sS -X PUT "$BASE_URL/personal_info/$DEMO_USER_ID/facts/$fact_id" \
  -H "Content-Type: application/json" \
  -d "$update_payload" | jq .

info "5) Delete the same fact"
curl -sS -X DELETE "$BASE_URL/personal_info/$DEMO_USER_ID/facts/$fact_id" | jq .

ok "Personal info demo complete."
