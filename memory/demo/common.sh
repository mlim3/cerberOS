#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080/api/v1}"
VAULT_API_KEY="${VAULT_API_KEY:-test-vault-key}"

DEMO_USER_ID="${DEMO_USER_ID:-11111111-1111-1111-1111-111111111111}"
OTHER_USER_ID="${OTHER_USER_ID:-22222222-2222-2222-2222-222222222222}"
DEMO_SESSION_ID="${DEMO_SESSION_ID:-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa}"
DEMO_TASK_ID="${DEMO_TASK_ID:-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb}"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
info() { printf "\033[36m%s\033[0m\n" "$*"; }
ok() { printf "\033[32m%s\033[0m\n" "$*"; }
warn() { printf "\033[33m%s\033[0m\n" "$*"; }
err() { printf "\033[31m%s\033[0m\n" "$*"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    err "Missing required command: $1"
    exit 1
  }
}

require_basics() {
  require_cmd curl
  require_cmd jq
  require_cmd uuidgen
}

ensure_api_up() {
  if ! curl -sS -m 2 "$BASE_URL/healthz" >/dev/null 2>&1; then
    err "Memory API is not reachable at $BASE_URL"
    warn "Start it first, for example:"
    warn "  cd memory"
    warn "  VAULT_MASTER_KEY=0123456789abcdef0123456789abcdef INTERNAL_VAULT_API_KEY=$VAULT_API_KEY go run ./cmd/server"
    exit 1
  fi
}
