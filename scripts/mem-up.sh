#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/docker-compose.yml"
SEED_DB=false
BRING_DOWN=false

print_help() {
    cat <<EOF
Usage: ./scripts/mem-up.sh [options]

Starts local memory dependencies needed for development:
  - memory-db
  - embedding-api

Options:
  --seed         Seed the memory database after startup
  -d, --down     Stop and remove memory-db and embedding-api
  -h, --help     Show this help message

Environment overrides:
  EMBEDDING_MODEL         Default: microsoft/harrier-oss-v1-270m
  EMBEDDING_DIM           Default: 640
  EMBEDDING_PROMPT_STYLE  Default: harrier
EOF
}

for arg in "$@"; do
    case "$arg" in
        --seed)
            SEED_DB=true
            ;;
        -d|--down)
            BRING_DOWN=true
            ;;
        -h|--help)
            print_help
            exit 0
            ;;
    esac
done

if [ ! -f "${COMPOSE_FILE}" ]; then
    echo "Error: docker-compose.yml not found at ${COMPOSE_FILE}" >&2
    exit 1
fi

EMBEDDING_MODEL="${EMBEDDING_MODEL:-microsoft/harrier-oss-v1-270m}"
EMBEDDING_DIM="${EMBEDDING_DIM:-640}"
EMBEDDING_PROMPT_STYLE="${EMBEDDING_PROMPT_STYLE:-harrier}"

if [ "${BRING_DOWN}" = true ]; then
    echo "Stopping memory-db and embedding-api via repo root docker compose..."
    cd "${ROOT_DIR}"
    docker compose -f "${COMPOSE_FILE}" stop memory-db embedding-api >/dev/null 2>&1 || true
    docker compose -f "${COMPOSE_FILE}" rm -f memory-db embedding-api >/dev/null 2>&1 || true
    echo "memory-db and embedding-api have been stopped."
    exit 0
fi

echo "Starting memory-db and embedding-api via repo root docker compose..."
cd "${ROOT_DIR}"
docker compose -f "${COMPOSE_FILE}" up -d memory-db embedding-api

echo "Waiting for PostgreSQL to be ready..."
DB_CONTAINER="$(docker compose -f "${COMPOSE_FILE}" ps -q memory-db)"

if [ -z "${DB_CONTAINER}" ]; then
    echo "Error: memory-db container not found. Did docker compose start correctly?" >&2
    exit 1
fi

until docker exec "${DB_CONTAINER}" pg_isready -U user -d memory_db >/dev/null 2>&1; do
    echo "Waiting for database connection..."
    sleep 2
done

echo "Waiting for embedding-api to be ready..."
until docker compose -f "${COMPOSE_FILE}" exec -T embedding-api python -c "import sys, urllib.request; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1/health').status == 200 else 1)" >/dev/null 2>&1; do
    echo "Waiting for embedding-api health check..."
    sleep 5
done

if [ "${SEED_DB}" = true ]; then
    echo "Running seed script..."
    docker exec -i "${DB_CONTAINER}" psql -U user -d memory_db < "${ROOT_DIR}/memory/scripts/seed.sql"
fi

echo "Building memory-cli..."
(
    cd "${ROOT_DIR}/memory"
    go build -o memory-cli ./cmd/cli
)

echo ""
echo "================================================="
echo "memory-db is ready on localhost:5432"
echo "embedding-api is ready on localhost:8082"
echo "memory-cli has been built at memory/memory-cli"
echo "================================================="
echo ""
echo "Run memory tests with:"
echo "  cd ${ROOT_DIR}/memory && \\"
echo "  GOCACHE=/tmp/go-build \\"
echo "  EMBEDDING_MODEL=${EMBEDDING_MODEL} \\"
echo "  EMBEDDING_DIM=${EMBEDDING_DIM} \\"
echo "  EMBEDDING_PROMPT_STYLE=${EMBEDDING_PROMPT_STYLE} \\"
echo "  DB_HOST=localhost DB_PORT=5432 DB_USER=user DB_PASSWORD=password DB_NAME=memory_db \\"
echo "  VAULT_MASTER_KEY=0123456789abcdef0123456789abcdef \\"
echo "  INTERNAL_VAULT_API_KEY=test-vault-key \\"
echo "  go test ./tests -count=1"
