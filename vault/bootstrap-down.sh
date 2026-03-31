#!/usr/bin/env bash
set -euo pipefail

# Tear down OpenBao and clean up everything bootstrap-up.sh created.
# Flags:
#   --keep-db       Preserve the openbao database in Postgres.
#   --stop-memory   Also stop memory's Postgres container.

COMPOSE_DIR="${COMPOSE_DIR:-$(cd "$(dirname "$0")" && pwd)}"
MEMORY_DIR="${MEMORY_DIR:-$(cd "$(dirname "$0")/../memory" && pwd)}"
KEEP_DB=false
STOP_MEMORY=false

for arg in "$@"; do
  case "$arg" in
    --keep-db) KEEP_DB=true ;;
    --stop-memory) STOP_MEMORY=true ;;
    *) echo "Unknown option: $arg"; exit 1 ;;
  esac
done

cd "$COMPOSE_DIR"

# Stop and remove vault compose services (openbao, vault, swagger)
echo "Stopping vault services..."
docker compose down

# Remove saved init credentials
if [ -f .openbao-init.json ]; then
  echo "Removing .openbao-init.json..."
  rm -f .openbao-init.json
fi

# Drop the openbao database from memory's Postgres
if [ "$KEEP_DB" = false ]; then
  if docker compose -f "$MEMORY_DIR/docker-compose.yml" ps db --status running -q 2>/dev/null | grep -q .; then
    echo "Dropping openbao database..."
    docker compose -f "$MEMORY_DIR/docker-compose.yml" exec db \
      psql -U user -d memory_db -c "DROP DATABASE IF EXISTS openbao" 2>/dev/null || true
  else
    echo "Memory Postgres is not running — skipping database cleanup."
  fi
else
  echo "Keeping openbao database (--keep-db)."
fi

# Optionally stop memory's Postgres
if [ "$STOP_MEMORY" = true ]; then
  echo "Stopping memory Postgres..."
  docker compose -f "$MEMORY_DIR/docker-compose.yml" down
fi

echo "Done."
