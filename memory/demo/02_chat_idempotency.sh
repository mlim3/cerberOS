#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$THIS_DIR/common.sh"

require_basics
ensure_api_up

bold "Demo 02: Chat + idempotency"

idempotency_key="$(uuidgen | tr '[:upper:]' '[:lower:]')"
payload_1="$(jq -nc \
  --arg userId "$DEMO_USER_ID" \
  --arg role "user" \
  --arg content "What did I say about Postgres last week?" \
  --arg key "$idempotency_key" \
  '{userId:$userId, role:$role, content:$content, idempotencyKey:$key}')"

info "1) Create message"
resp_1="$(curl -sS -X POST "$BASE_URL/chat/$DEMO_SESSION_ID/messages" \
  -H "Content-Type: application/json" \
  -d "$payload_1")"
echo "$resp_1" | jq .

info "2) Replay same idempotency key + same payload (should reuse prior message)"
resp_2="$(curl -sS -X POST "$BASE_URL/chat/$DEMO_SESSION_ID/messages" \
  -H "Content-Type: application/json" \
  -d "$payload_1")"
echo "$resp_2" | jq .

info "3) Replay same idempotency key + different payload (should conflict)"
payload_3="$(jq -nc \
  --arg userId "$DEMO_USER_ID" \
  --arg role "assistant" \
  --arg content "Different payload for same key" \
  --arg key "$idempotency_key" \
  '{userId:$userId, role:$role, content:$content, idempotencyKey:$key}')"

code="$(curl -sS -o /tmp/chat_conflict.json -w "%{http_code}" -X POST "$BASE_URL/chat/$DEMO_SESSION_ID/messages" \
  -H "Content-Type: application/json" \
  -d "$payload_3")"
cat /tmp/chat_conflict.json | jq .
info "HTTP status: $code"

info "4) List messages in session"
curl -sS "$BASE_URL/chat/$DEMO_SESSION_ID/messages?limit=20" | jq .

ok "Chat demo complete."
