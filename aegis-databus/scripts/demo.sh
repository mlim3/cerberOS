#!/bin/bash
# EDD Demo Script - Run all 6 components and demonstrate requirements
set -e
cd "$(dirname "$0")/.."

echo "=== Aegis DataBus EDD Demo ==="
echo ""

# Check docker
if ! docker ps &>/dev/null; then
  echo "Docker not running. Start Docker and retry."
  exit 1
fi

# Start cluster
echo "[1/4] Starting NATS 3-node cluster..."
docker compose up -d 2>/dev/null || true
sleep 5

# Build
echo "[2/4] Building..."
make build

# Start DataBus (streams + heartbeat)
echo "[3/4] Starting Data Bus..."
./bin/aegis-databus 2>/dev/null &
DATABUS_PID=$!
sleep 3

# Run demo (6 components)
echo "[4/4] Starting 6 components (I/O, Orchestrator, Memory, Vault, Agent, Monitoring)..."
./bin/aegis-demo &
DEMO_PID=$!

echo ""
echo "Demo running. Press Ctrl+C to stop."
echo "Watch for: FR-DB-001 (pub/sub), FR-DB-004 (queue group), FR-DB-005 (wildcard)"
echo ""
trap "kill $DEMO_PID $DATABUS_PID 2>/dev/null; exit 0" INT TERM
wait $DEMO_PID 2>/dev/null || true
kill $DATABUS_PID 2>/dev/null || true
