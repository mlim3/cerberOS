#!/usr/bin/env bash

set -e

# Change to the memory directory (where this script's parent directory is located)
cd "$(dirname "$0")/.."

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

until docker exec "$DB_CONTAINER" pg_isready -U user -d memory_db; do
  echo "Waiting for database connection..."
  sleep 2
done

echo "Database is ready!"

echo "Running go mod tidy..."
go mod tidy

echo "Bootstrap complete!"
