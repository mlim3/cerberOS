#!/usr/bin/env bash
set -euo pipefail

# Bootstrap OpenBao using memory service's Postgres as the storage backend.
# Requires: memory's docker compose stack running first (provides the 'db' container).

COMPOSE_DIR="${COMPOSE_DIR:-$(cd "$(dirname "$0")" && pwd)}"
MEMORY_DIR="${MEMORY_DIR:-$(cd "$(dirname "$0")/../memory" && pwd)}"
BAO_ADDR="http://127.0.0.1:8200"

cd "$COMPOSE_DIR"

# Ensure memory's Postgres is running
echo "Checking memory Postgres is up..."
if ! docker compose -f "$MEMORY_DIR/docker-compose.yml" ps db --status running -q 2>/dev/null | grep -q .; then
  echo "Starting memory Postgres..."
  docker compose -f "$MEMORY_DIR/docker-compose.yml" up -d db
  echo "Waiting for Postgres to be healthy..."
  for i in $(seq 1 30); do
    if docker compose -f "$MEMORY_DIR/docker-compose.yml" exec db pg_isready -U user -d memory_db > /dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi

# Create the openbao database if it doesn't exist
echo "Ensuring openbao database exists..."
docker compose -f "$MEMORY_DIR/docker-compose.yml" exec db \
  psql -U user -d memory_db -tc "SELECT 1 FROM pg_database WHERE datname = 'openbao'" | grep -q 1 || \
  docker compose -f "$MEMORY_DIR/docker-compose.yml" exec db \
  psql -U user -d memory_db -c "CREATE DATABASE openbao OWNER \"user\""

# Start OpenBao (joins memory's network to reach 'db')
echo "Starting Vault..."
docker compose up -d

echo "Waiting for OpenBao to be reachable..."
for i in $(seq 1 30); do
  if curl -sf "$BAO_ADDR/v1/sys/health" -o /dev/null 2>/dev/null || \
     curl -sf "$BAO_ADDR/v1/sys/seal-status" -o /dev/null 2>/dev/null; then
    break
  fi
  sleep 1
done

# Check if already initialized
INIT_STATUS=$(curl -sf "$BAO_ADDR/v1/sys/init" | grep -o '"initialized":[a-z]*')
if echo "$INIT_STATUS" | grep -q "true"; then
  echo "OpenBao already initialized."
else
  echo "Initializing OpenBao (1 key share for dev)..."
  INIT_OUT=$(docker compose exec -e BAO_ADDR="$BAO_ADDR" openbao \
    bao operator init -key-shares=1 -key-threshold=1 -format=json)

  UNSEAL_KEY=$(echo "$INIT_OUT" | jq -r '.unseal_keys_b64[0]')
  ROOT_TOKEN=$(echo "$INIT_OUT" | jq -r '.root_token')

  echo "$INIT_OUT" > .openbao-init.json
  echo "Init output saved to .openbao-init.json"
fi

# Load keys from saved init if not set
if [ -z "${UNSEAL_KEY:-}" ]; then
  if [ ! -f .openbao-init.json ]; then
    echo "error: no init output found — delete the openbao database and re-run"
    exit 1
  fi
  UNSEAL_KEY=$(jq -r '.unseal_keys_b64[0]' .openbao-init.json)
  ROOT_TOKEN=$(jq -r '.root_token' .openbao-init.json)
fi

# Unseal if sealed
SEALED=$(curl -sf "$BAO_ADDR/v1/sys/seal-status" | jq -r '.sealed')
if [ "$SEALED" = "true" ]; then
  echo "Unsealing..."
  docker compose exec -e BAO_ADDR="$BAO_ADDR" openbao \
    bao operator unseal "$UNSEAL_KEY" > /dev/null
fi

# Wait for post-unseal setup to complete
echo "Waiting for OpenBao to become active..."
for i in $(seq 1 30); do
  HEALTH=$(curl -s -o /dev/null -w "%{http_code}" "$BAO_ADDR/v1/sys/health")
  if [ "$HEALTH" = "200" ]; then
    break
  fi
  sleep 1
done
if [ "$HEALTH" != "200" ]; then
  echo "error: OpenBao not healthy after unseal (HTTP $HEALTH)"
  exit 1
fi

echo "Enabling KV v2 secrets engine..."
KV_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BAO_ADDR/v1/sys/mounts/kv" \
  -H "X-Vault-Token: $ROOT_TOKEN" \
  -d '{"type":"kv","options":{"version":"2"}}')
if [ "$KV_RESP" = "204" ] || [ "$KV_RESP" = "200" ]; then
  echo "  KV v2 engine enabled."
elif [ "$KV_RESP" = "400" ]; then
  echo "  KV v2 engine already mounted."
else
  echo "  warning: KV mount returned HTTP $KV_RESP"
fi

# Create a least-privilege policy for the vault service
echo "Creating vault-service policy..."
POLICY_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BAO_ADDR/v1/sys/policies/acl/vault-service" \
  -H "X-Vault-Token: $ROOT_TOKEN" \
  -d '{
    "policy": "path \"kv/data/*\" { capabilities = [\"create\",\"read\",\"update\",\"delete\",\"list\"] }\npath \"kv/metadata/*\" { capabilities = [\"list\",\"read\",\"delete\"] }"
  }')
if [ "$POLICY_RESP" = "204" ] || [ "$POLICY_RESP" = "200" ]; then
  echo "  vault-service policy created."
else
  echo "  warning: policy creation returned HTTP $POLICY_RESP"
fi

# Create a service token with the vault-service policy
echo "Creating service token..."
TOKEN_OUT=$(curl -s -X POST "$BAO_ADDR/v1/auth/token/create" \
  -H "X-Vault-Token: $ROOT_TOKEN" \
  -d '{"policies":["vault-service"],"display_name":"vault-service","no_parent":true}')
SERVICE_TOKEN=$(echo "$TOKEN_OUT" | jq -r '.auth.client_token')
if [ -z "$SERVICE_TOKEN" ] || [ "$SERVICE_TOKEN" = "null" ]; then
  echo "error: failed to create service token"
  echo "$TOKEN_OUT"
  exit 1
fi
echo "  Service token created."

# Write .env so docker compose injects BAO_TOKEN into the vault container
echo "BAO_TOKEN=$SERVICE_TOKEN" > .env
echo "  .env written with BAO_TOKEN."

# Restart vault service so it picks up the token
echo "Restarting vault service..."
docker compose up -d vault

echo "Running smoke test..."
WRITE_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BAO_ADDR/v1/kv/data/test" \
  -H "X-Vault-Token: $ROOT_TOKEN" \
  -d '{"data":{"smoke":"ok"}}')
if [ "$WRITE_RESP" != "200" ] && [ "$WRITE_RESP" != "204" ]; then
  echo "error: smoke test write failed with HTTP $WRITE_RESP"
  exit 1
fi

RESULT=$(curl -s "$BAO_ADDR/v1/kv/data/test" \
  -H "X-Vault-Token: $ROOT_TOKEN" | jq -r '.data.data.smoke')

if [ "$RESULT" = "ok" ]; then
  echo ""
  echo "OpenBao is ready."
  echo "  Address:    $BAO_ADDR"
  echo "  Root Token: $ROOT_TOKEN"
  echo ""
  echo "Postgres verification:"
  docker compose -f "$MEMORY_DIR/docker-compose.yml" exec db \
    psql -U user -d openbao -tAc \
    "SELECT COUNT(*) FROM openbao_kv_store;" | xargs -I{} echo "  Rows in openbao_kv_store: {}"
else
  echo "error: smoke test failed — got '$RESULT', expected 'ok'"
  exit 1
fi
