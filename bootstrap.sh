#!/usr/bin/env bash
# cerberOS bootstrap — bring the full stack up or tear it down.
#
# Usage:
#   ./bootstrap.sh                       # wipe volumes, build, start, init + unseal OpenBao
#   ./bootstrap.sh --keep-volumes        # build, start, init + unseal OpenBao without wiping volumes
#   ./bootstrap.sh down                  # stop stack, clean up OpenBao state
#   ./bootstrap.sh down --keep-db        # stop but keep openbao database
#   ./bootstrap.sh down --delete-volumes # stop and remove Docker volumes
#
# By default `up` wipes the Docker volumes before bringing the stack up, so every
# teammate ends a bootstrap run on the same schema and seed state regardless of
# prior volume history. Use --keep-volumes when you want to preserve local chat,
# vault secrets, or other data across a bootstrap.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

log() { printf '%s\n' "$*"; }

die() { log "error: $*"; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

upsert_env_var() {
  local file="$1" key="$2" val="$3"
  if [[ ! -f "$file" ]]; then
    printf '%s=%s\n' "$key" "$val" > "$file"
    return
  fi
  local tmp
  tmp="$(mktemp)"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    grep -v "^${key}=" "$file" > "$tmp" || true
  else
    cp "$file" "$tmp"
  fi
  printf '%s=%s\n' "$key" "$val" >> "$tmp"
  mv "$tmp" "$file"
}

BAO_ADDR="http://127.0.0.1:8200"

# =============================================================================
# DOWN
# =============================================================================
cmd_down() {
  local keep_db=false delete_volumes=false
  for arg in "$@"; do
    case "$arg" in
      --keep-db) keep_db=true ;;
      --delete-volumes) delete_volumes=true ;;
      *) die "unknown option: $arg" ;;
    esac
  done

  # Drop the openbao database before stopping Postgres
  if [[ "$keep_db" == false ]]; then
    if docker compose ps memory-db --status running -q 2>/dev/null | grep -q .; then
      log "Terminating openbao database connections..."
      docker compose exec memory-db \
        psql -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" \
        -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'openbao' AND pid <> pg_backend_pid();" 2>/dev/null || true
      log "Dropping openbao database..."
      docker compose exec memory-db \
        psql -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" \
        -c "DROP DATABASE IF EXISTS openbao" 2>/dev/null || true
    else
      log "memory-db not running — skipping database cleanup."
    fi
  else
    log "Keeping openbao database (--keep-db)."
  fi

  # Stop the stack
  if [[ "$delete_volumes" == true ]]; then
    log "Stopping stack and removing volumes..."
    docker compose down -v
  else
    log "Stopping stack..."
    docker compose down
  fi

  # Clean up OpenBao init credentials
  for f in vault/.openbao-init.json; do
    if [[ -f "$f" ]]; then
      log "Removing $f..."
      rm -f "$f"
    fi
  done

  # Remove BAO_TOKEN from .env (but keep the file)
  if [[ -f "$ROOT/.env" ]] && grep -q "^BAO_TOKEN=" "$ROOT/.env" 2>/dev/null; then
    log "Clearing BAO_TOKEN from .env..."
    upsert_env_var "$ROOT/.env" "BAO_TOKEN" ""
  fi

  log "Done."
}

# =============================================================================
# UP
# =============================================================================
cmd_up() {
  local keep_volumes=false
  for arg in "$@"; do
    case "$arg" in
      --keep-volumes) keep_volumes=true ;;
      *) die "unknown option: $arg" ;;
    esac
  done

  # --- Prerequisites ---
  require_cmd docker
  docker info >/dev/null 2>&1 || die "Docker is not running or not accessible"
  require_cmd curl
  require_cmd jq
  require_cmd openssl

  if docker compose version >/dev/null 2>&1; then
    :
  elif command -v docker-compose >/dev/null 2>&1; then
    die "use Docker Compose v2 (docker compose), not docker-compose"
  else
    die "docker compose (v2) is required"
  fi

  # --- Wipe volumes (default) ---
  # Bootstrap is meant to produce a known, identical starting state on every
  # teammate's machine. `docker-entrypoint-initdb.d` scripts only run when the
  # Postgres data dir is empty, so preserving volumes silently ships stale
  # schema and seed rows to whoever bootstraps against an older volume.
  # Wiping by default eliminates that drift; --keep-volumes is the explicit
  # opt-out when a dev wants to preserve local data across a bootstrap.
  if [[ "$keep_volumes" == false ]]; then
    log "Wiping Docker volumes (opt out with --keep-volumes)..."
    docker compose down -v --remove-orphans 2>/dev/null || true
    rm -f "$ROOT/vault/.openbao-init.json"
    if [[ -f "$ROOT/.env" ]] && grep -q "^BAO_TOKEN=" "$ROOT/.env" 2>/dev/null; then
      upsert_env_var "$ROOT/.env" "BAO_TOKEN" ""
    fi
  else
    log "Keeping existing Docker volumes (--keep-volumes)."
  fi

  # --- .env ---
  if [[ ! -f "$ROOT/.env" ]]; then
    [[ -f "$ROOT/.env.example" ]] || die "missing .env.example"
    cp "$ROOT/.env.example" "$ROOT/.env"
    log "Created .env from .env.example"
  fi

  set -a
  # shellcheck source=/dev/null
  source "$ROOT/.env"
  set +a

  if [[ -z "${VAULT_MASTER_KEY:-}" ]]; then
    v="$(openssl rand -base64 24 | head -c 32)"
    upsert_env_var "$ROOT/.env" "VAULT_MASTER_KEY" "$v"
    log "Generated VAULT_MASTER_KEY in .env"
    VAULT_MASTER_KEY="$v"
  fi

  if [[ -z "${INTERNAL_VAULT_API_KEY:-}" ]]; then
    v="$(openssl rand -hex 32)"
    upsert_env_var "$ROOT/.env" "INTERNAL_VAULT_API_KEY" "$v"
    log "Generated INTERNAL_VAULT_API_KEY in .env"
    INTERNAL_VAULT_API_KEY="$v"
  fi

  if [[ "${#VAULT_MASTER_KEY}" -ne 32 ]]; then
    die "VAULT_MASTER_KEY must be exactly 32 characters (see README)"
  fi

  # --- Postgres before the rest of the stack ---
  # After `bootstrap.sh down`, DROP DATABASE openbao runs but the Postgres volume often
  # persists, so docker-entrypoint init scripts do not recreate `openbao`. OpenBao must
  # not start until that database exists again.
  log "Starting Postgres (memory-db)..."
  docker compose up --build --detach memory-db

  log "Waiting for Postgres (memory-db)..."
  for _ in $(seq 1 60); do
    if docker compose exec memory-db pg_isready -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  docker compose exec memory-db pg_isready -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" >/dev/null 2>&1 \
    || die "Postgres (memory-db) did not become ready"

  log "Ensuring openbao database exists..."
  docker compose exec memory-db \
    psql -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" \
    -tc "SELECT 1 FROM pg_database WHERE datname = 'openbao'" | grep -q 1 || \
    docker compose exec memory-db \
    psql -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" \
    -c "CREATE DATABASE openbao OWNER \"${POSTGRES_USER:-user}\""

  # On --keep-volumes runs, Postgres skips docker-entrypoint-initdb.d because
  # the data dir already exists, so new tables/indexes/seed rows added to
  # init-db.sql would not land. Re-apply the script explicitly — it is
  # idempotent (CREATE ... IF NOT EXISTS, INSERT ... ON CONFLICT DO NOTHING)
  # so this is a no-op on a genuinely up-to-date DB and cannot clobber
  # user-generated rows. Skipped on the default wipe path because
  # docker-entrypoint will have just run it fresh on the empty volume.
  #
  # Stream the file from the host via stdin rather than passing a container
  # path to `psql -f`. Git Bash / MSYS on Windows rewrites any absolute
  # argument starting with `/` into a Windows path (e.g.
  # `/docker-entrypoint-initdb.d/01-init-db.sql` becomes
  # `C:/Program Files/Git/docker-entrypoint-initdb.d/...`) which breaks `exec`
  # even though the path was meant to be interpreted inside the container.
  # stdin avoids that entirely and also uses the checked-out script directly
  # instead of relying on the docker-entrypoint bind mount being in sync.
  if [[ "$keep_volumes" == true ]]; then
    log "Re-applying memory_db schema/seed (idempotent) for --keep-volumes..."
    [[ -f "$ROOT/memory/scripts/init-db.sql" ]] \
      || die "memory/scripts/init-db.sql not found at $ROOT"
    docker compose exec -T memory-db \
      psql -U "${POSTGRES_USER:-user}" -d "${POSTGRES_DB:-memory_db}" \
      -v ON_ERROR_STOP=1 \
      < "$ROOT/memory/scripts/init-db.sql" >/dev/null \
      || die "failed to re-apply memory/scripts/init-db.sql"
  fi

  log "Starting remaining Docker services..."
  docker compose up --build --detach

  log "Waiting for OpenBao (localhost:8200)..."
  for _ in $(seq 1 60); do
    if curl -sf "$BAO_ADDR/v1/sys/health" -o /dev/null 2>/dev/null || \
       curl -sf "$BAO_ADDR/v1/sys/seal-status" -o /dev/null 2>/dev/null; then
      break
    fi
    sleep 2
  done
  curl -sf "$BAO_ADDR/v1/sys/seal-status" -o /dev/null 2>/dev/null \
    || die "OpenBao did not become reachable on :8200"

  openbao_exec() {
    docker compose exec -e BAO_ADDR="$BAO_ADDR" openbao "$@"
  }

  # Check if already initialized
  INIT_STATUS=$(curl -sf "$BAO_ADDR/v1/sys/init" | grep -o '"initialized":[a-z]*')
  if echo "$INIT_STATUS" | grep -q "true"; then
    log "OpenBao already initialized."
  else
    log "Initializing OpenBao (1 key share for dev)..."
    INIT_OUT=$(openbao_exec bao operator init -key-shares=1 -key-threshold=1 -format=json)

    UNSEAL_KEY=$(echo "$INIT_OUT" | jq -r '.unseal_keys_b64[0]')
    ROOT_TOKEN=$(echo "$INIT_OUT" | jq -r '.root_token')

    echo "$INIT_OUT" > vault/.openbao-init.json
    log "Init output saved to vault/.openbao-init.json"
  fi

  # Load keys from saved init if not set
  if [[ -z "${UNSEAL_KEY:-}" ]]; then
    if [[ ! -f vault/.openbao-init.json ]]; then
      die "no init output found — delete the openbao database and re-run"
    fi
    UNSEAL_KEY=$(jq -r '.unseal_keys_b64[0]' vault/.openbao-init.json)
    ROOT_TOKEN=$(jq -r '.root_token' vault/.openbao-init.json)
  fi

  # Unseal if sealed
  SEALED=$(curl -sf "$BAO_ADDR/v1/sys/seal-status" | jq -r '.sealed')
  if [[ "$SEALED" == "true" ]]; then
    log "Unsealing..."
    openbao_exec bao operator unseal "$UNSEAL_KEY" >/dev/null
  fi

  # Wait for post-unseal health
  log "Waiting for OpenBao to become active..."
  for _ in $(seq 1 30); do
    HEALTH=$(curl -s -o /dev/null -w "%{http_code}" "$BAO_ADDR/v1/sys/health")
    if [[ "$HEALTH" == "200" ]]; then
      break
    fi
    sleep 1
  done
  if [[ "$HEALTH" != "200" ]]; then
    die "OpenBao not healthy after unseal (HTTP $HEALTH)"
  fi

  log "Enabling KV v2 secrets engine..."
  KV_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BAO_ADDR/v1/sys/mounts/kv" \
    -H "X-Vault-Token: $ROOT_TOKEN" \
    -d '{"type":"kv","options":{"version":"2"}}')
  if [[ "$KV_RESP" == "204" ]] || [[ "$KV_RESP" == "200" ]]; then
    log "  KV v2 engine enabled."
  elif [[ "$KV_RESP" == "400" ]]; then
    log "  KV v2 engine already mounted."
  else
    log "  warning: KV mount returned HTTP $KV_RESP"
  fi

  log "Creating vault-service policy..."
  POLICY_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BAO_ADDR/v1/sys/policies/acl/vault-service" \
    -H "X-Vault-Token: $ROOT_TOKEN" \
    -d '{
      "policy": "path \"kv/data/*\" { capabilities = [\"create\",\"read\",\"update\",\"delete\",\"list\"] }\npath \"kv/metadata/*\" { capabilities = [\"list\",\"read\",\"delete\"] }"
    }')
  if [[ "$POLICY_RESP" == "204" ]] || [[ "$POLICY_RESP" == "200" ]]; then
    log "  vault-service policy created."
  else
    log "  warning: policy creation returned HTTP $POLICY_RESP"
  fi

  log "Creating service token..."
  TOKEN_OUT=$(curl -s -X POST "$BAO_ADDR/v1/auth/token/create" \
    -H "X-Vault-Token: $ROOT_TOKEN" \
    -d '{"policies":["vault-service"],"display_name":"vault-service","no_parent":true}')
  SERVICE_TOKEN=$(echo "$TOKEN_OUT" | jq -r '.auth.client_token')
  if [[ -z "$SERVICE_TOKEN" ]] || [[ "$SERVICE_TOKEN" == "null" ]]; then
    die "failed to create service token"
  fi
  log "  Service token created."

  upsert_env_var "$ROOT/.env" "BAO_TOKEN" "$SERVICE_TOKEN"
  log "  .env updated with BAO_TOKEN."

  # Seed application secrets into OpenBao.
  # TAVILY_API_KEY powers the web_search skill. If present in .env, write it
  # now so vault engine can resolve it at runtime without manual intervention.
  #
  # bootstrap.sh never sources .env (set -euo pipefail — no implicit sourcing),
  # so we read the key directly from the file when it is not already exported.
  if [[ -z "${TAVILY_API_KEY:-}" ]] && [[ -f "$ROOT/.env" ]]; then
    _tavily_val=$(grep -E "^TAVILY_API_KEY=" "$ROOT/.env" 2>/dev/null | head -1 | cut -d'=' -f2-)
    [[ -n "$_tavily_val" ]] && TAVILY_API_KEY="$_tavily_val"
    unset _tavily_val
  fi
  if [[ -n "${TAVILY_API_KEY:-}" ]]; then
    log "Seeding TAVILY_API_KEY into OpenBao..."
    SEED_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BAO_ADDR/v1/kv/data/TAVILY_API_KEY" \
      -H "X-Vault-Token: $ROOT_TOKEN" \
      -d "{\"data\":{\"TAVILY_API_KEY\":\"$TAVILY_API_KEY\"}}")
    if [[ "$SEED_RESP" == "200" ]] || [[ "$SEED_RESP" == "204" ]]; then
      log "  TAVILY_API_KEY stored."
    else
      log "  warning: TAVILY_API_KEY seed returned HTTP $SEED_RESP"
    fi
  else
    log "  TAVILY_API_KEY not set in .env — web_search skill will be unavailable."
    log "  Add TAVILY_API_KEY=<key> to .env and re-run bootstrap.sh to enable it."
  fi

  log "Restarting vault service (recreating to pick up BAO_TOKEN)..."
  docker compose up --detach vault

  # Smoke test
  log "Running smoke test..."
  WRITE_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BAO_ADDR/v1/kv/data/test" \
    -H "X-Vault-Token: $ROOT_TOKEN" \
    -d '{"data":{"smoke":"ok"}}')
  if [[ "$WRITE_RESP" != "200" ]] && [[ "$WRITE_RESP" != "204" ]]; then
    die "smoke test write failed with HTTP $WRITE_RESP"
  fi

  RESULT=$(curl -s "$BAO_ADDR/v1/kv/data/test" \
    -H "X-Vault-Token: $ROOT_TOKEN" | jq -r '.data.data.smoke')

  if [[ "$RESULT" == "ok" ]]; then
    log ""
    log "Bootstrap finished."
    log "  OpenBao:      $BAO_ADDR"
    log "  Root Token:   $ROOT_TOKEN"
    log "  Vault engine: http://127.0.0.1:8000"
    log ""
    log "Postgres verification:"
    docker compose exec memory-db \
      psql -U "${POSTGRES_USER:-user}" -d openbao -tAc \
      "SELECT COUNT(*) FROM openbao_kv_store;" | xargs -I{} echo "  Rows in openbao_kv_store: {}"
  else
    die "smoke test failed — got '$RESULT', expected 'ok'"
  fi
}

# =============================================================================
# MAIN
# =============================================================================
case "${1:-up}" in
  up)      [[ $# -gt 0 ]] && shift; cmd_up "$@" ;;
  down)    [[ $# -gt 0 ]] && shift; cmd_down "$@" ;;
  -*)      cmd_up "$@" ;;
  *)       die "usage: $0 [up|down] [options]" ;;
esac
