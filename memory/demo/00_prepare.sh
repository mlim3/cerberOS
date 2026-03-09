#!/usr/bin/env bash
set -euo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$THIS_DIR/.." && pwd)"
source "$THIS_DIR/common.sh"

require_basics
require_cmd docker

bold "Preparing demo environment"
info "Using root: $ROOT_DIR"

(
  cd "$ROOT_DIR"
  docker compose up -d
)

info "Waiting for database health..."
for _ in {1..40}; do
  status="$(cd "$ROOT_DIR" && docker compose ps --format json 2>/dev/null | jq -r '.[0].Health // "unknown"' || true)"
  if [[ "$status" == "healthy" ]]; then
    break
  fi
  sleep 1
done

bold "Seeding demo users into identity_schema.users"
(
  cd "$ROOT_DIR"
  docker compose exec -T db psql -U user -d memory_db <<SQL
INSERT INTO identity_schema.users (id, email, created_at) VALUES
  ('$DEMO_USER_ID', 'demo-user@example.com', NOW()),
  ('$OTHER_USER_ID', 'other-user@example.com', NOW())
ON CONFLICT (id) DO NOTHING;
SQL
)

ok "Environment ready."
info "Next: start API server and run ./demo/run_all.sh"
