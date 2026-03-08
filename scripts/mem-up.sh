#!/usr/bin/env bash

set -e

# Function to clean up background processes on exit
cleanup() {
    echo -e "\nStopping all servers..."
    # Kill all background jobs started by this script
    jobs -p | xargs -r kill
}
trap cleanup EXIT INT TERM

# Change to the memory directory
cd "$(dirname "$0")/memory"

# Parse arguments
SEED_DB=false
for arg in "$@"; do
    if [ "$arg" == "--seed" ]; then
        SEED_DB=true
    fi
done

echo "Starting Docker containers..."
docker-compose up -d

echo "Waiting for PostgreSQL to be ready..."
# Wait for the database to accept connections
# We can use pg_isready inside the container
DB_CONTAINER=$(docker-compose ps -q db)

if [ -z "$DB_CONTAINER" ]; then
    echo "Error: DB container not found. Did docker-compose start correctly?"
    exit 1
fi

until docker exec "$DB_CONTAINER" pg_isready -U user -d memory_db >/dev/null 2>&1; do
  echo "Waiting for database connection..."
  sleep 2
done

echo "Database is ready, wait for all the components to come up!"

if [ "$SEED_DB" = true ]; then
    echo "Running seed script..."
    docker exec -i "$DB_CONTAINER" psql -U user -d memory_db < scripts/seed.sql
fi

echo "Running go mod tidy..."
go mod tidy

echo "Starting Memory Component..."
go run cmd/server/main.go &

echo ""
echo "================================================="
echo "All systems go! 🚀"
echo "Press Ctrl+C to stop all services."
echo "================================================="
echo ""

# Wait for background processes
wait
