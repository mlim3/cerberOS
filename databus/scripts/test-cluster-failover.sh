#!/bin/bash
# FR-DB-007: Cluster failover — 3-node cluster, kill node, verify recovery.
# Automated assertions: leader election < 5s, no message loss.
# Prerequisites: make up (3-node NATS cluster)
set -e
cd "$(dirname "$0")/.."

echo "=== FR-DB-007 Cluster Failover Test ==="
echo ""

if ! docker ps | grep -q nats; then
  echo "Start NATS cluster first: make up"
  exit 1
fi

# Run automated test (publishes 100 msgs, kills nats-1, measures reconnect, asserts no loss)
echo "Running cluster failover test (AEGIS_TEST_CLUSTER=1)..."
if AEGIS_TEST_CLUSTER=1 go test -v -run TestFRDB007_ClusterFailover ./tests/... 2>&1; then
  echo ""
  echo "PASS: FR-DB-007 — leader election < 5s, zero message loss"
  exit 0
else
  echo ""
  echo "FAIL: FR-DB-007 — see above"
  exit 1
fi
