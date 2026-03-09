#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$THIS_DIR/common.sh"

require_basics
ensure_api_up

bold "Demo 05: System events + Vault"

info "1) Create a system event"
event_payload='{"serviceName":"demo-suite","severity":"INFO","message":"demo event created","metadata":{"phase":"demo"}}'
curl -sS -X POST "$BASE_URL/system/events" \
  -H "Content-Type: application/json" \
  -d "$event_payload" | jq .

info "2) Query system events by serviceName"
curl -sS "$BASE_URL/system/events?serviceName=demo-suite&limit=5" | jq .

info "3) Vault access without key (expected unauthorized)"
vault_body='{"key_name":"DEMO_TOKEN","value":"abc123"}'
code="$(curl -sS -o /tmp/vault_unauthorized.json -w "%{http_code}" \
  -X POST "$BASE_URL/vault/$DEMO_USER_ID/secrets" \
  -H "Content-Type: application/json" \
  -d "$vault_body")"
cat /tmp/vault_unauthorized.json | jq .
info "HTTP status: $code"

info "4) Vault save with API key"
curl -sS -X POST "$BASE_URL/vault/$DEMO_USER_ID/secrets" \
  -H "Content-Type: application/json" \
  -H "X-API-KEY: $VAULT_API_KEY" \
  -d "$vault_body" >/dev/null
ok "Vault save succeeded."

info "5) Vault read with API key"
curl -sS -X GET "$BASE_URL/vault/$DEMO_USER_ID/secrets?key_name=DEMO_TOKEN" \
  -H "X-API-KEY: $VAULT_API_KEY" | jq .

ok "System + Vault demo complete."
