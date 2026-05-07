#!/bin/bash
# NFR-DB-010: Compare throughput 1-node vs 3-node cluster.
# Asserts cluster >= 1.5x single-node (near-linear scaling).
set -e
cd "$(dirname "$0")/.."

MIN_SCALING_FACTOR=${NFR_SCALING_MIN_FACTOR:-1.5}

echo "=== NFR-DB-010 Scaling Test ==="
echo "  Assert: cluster throughput >= ${MIN_SCALING_FACTOR}x single-node"
echo ""

echo "1. Single-node throughput (testcontainers)..."
ONE=$(go test -bench=BenchmarkPubSubSingleNode -benchtime=2s -run=^$ ./tests/... 2>&1 | grep "msgs_per_sec" | awk '{print $3}' || echo "0")
echo "   Single-node: $ONE msgs/sec"
echo ""

echo "2. Cluster throughput (requires: make up)..."
if ! docker ps | grep -q nats; then
  echo "   Skip: run 'make up' first for cluster benchmark"
  exit 0
fi
THREE=$(AEGIS_TEST_CLUSTER=1 go test -bench=BenchmarkPubSubCluster -benchtime=2s -run=^$ ./tests/... 2>&1 | grep "msgs_per_sec" | awk '{print $3}' || echo "0")
echo "   3-node cluster: $THREE msgs/sec"
echo ""

if [ "$ONE" != "0" ] && [ "$THREE" != "0" ]; then
  RATIO=$(awk "BEGIN {printf \"%.2f\", $THREE / $ONE}" 2>/dev/null || echo "0")
  echo "Scaling factor: $RATIO (cluster/single)"
  if awk "BEGIN {exit !($THREE >= $MIN_SCALING_FACTOR * $ONE)}" 2>/dev/null; then
    echo "PASS: NFR-DB-010 — cluster >= ${MIN_SCALING_FACTOR}x single-node"
    exit 0
  else
    echo "FAIL: NFR-DB-010 — cluster $THREE < ${MIN_SCALING_FACTOR}x single $ONE (need >= $(awk "BEGIN {printf \"%.0f\", $MIN_SCALING_FACTOR * $ONE}" 2>/dev/null))"
    exit 1
  fi
fi
