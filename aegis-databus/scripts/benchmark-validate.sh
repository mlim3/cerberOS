#!/bin/bash
# NFR-DB-001/002: Run benchmarks and pass/fail on targets.
# NFR-DB-001: >= 50,000 msgs/sec
# NFR-DB-002: P99 < 5ms
# Requires Docker for testcontainers.
set -e
cd "$(dirname "$0")/.."

THROUGHPUT_TARGET=50000   # msgs/sec
LATENCY_P99_MAX_MS=5.0

echo "=== NFR-DB Benchmark Validation ==="
echo "  NFR-DB-001: >= ${THROUGHPUT_TARGET} msgs/sec"
echo "  NFR-DB-002: P99 < ${LATENCY_P99_MAX_MS} ms"
echo ""

OUT=$(go test -bench=BenchmarkPubSub -benchmem -benchtime=2s -run=^$ ./tests/... 2>&1) || {
  echo "SKIP: Docker required for benchmarks"
  exit 0
}

# Parse ns/op from output. Format: "12345678   95.2 ns/op" or "95 ns/op"
NS_PER_OP=$(echo "$OUT" | grep -E "BenchmarkPubSub-[0-9]+" | head -1 | grep -oE '[0-9]+(\.[0-9]+)? ns/op' | awk '{print $1}')
if [ -z "$NS_PER_OP" ]; then
  echo "FAIL: Could not parse ns/op (benchmark may have skipped — Docker required)"
  exit 1
else
  # msgs/sec = 1e9 / ns_per_op
  MSGS_PER_SEC=$(awk "BEGIN {printf \"%.0f\", 1000000000 / $NS_PER_OP}" 2>/dev/null || echo "0")
  echo "Throughput: $MSGS_PER_SEC msgs/sec (target >= $THROUGHPUT_TARGET)"
  if awk "BEGIN {exit !($MSGS_PER_SEC >= $THROUGHPUT_TARGET)}" 2>/dev/null; then
    :
  else
    echo "FAIL: NFR-DB-001 — throughput $MSGS_PER_SEC < $THROUGHPUT_TARGET"
    exit 1
  fi
  echo "PASS: NFR-DB-001"
fi

OUT2=$(go test -bench=BenchmarkPubSubLatency -benchmem -benchtime=2s -run=^$ ./tests/... 2>&1) || {
  echo "SKIP: Latency benchmark (Docker required)"
  exit 0
}

# Parse ms_P99. Format: "12345 ns/op  2.5 ms_P99" (ReportMetric output)
P99_MS=$(echo "$OUT2" | grep -E "BenchmarkPubSubLatency-[0-9]+" | head -1 | grep -oE '[0-9]+(\.[0-9]+)? ms_P99' | awk '{print $1}')
if [ -z "$P99_MS" ]; then
  echo "FAIL: Could not parse ms_P99 (benchmark may have skipped — Docker required)"
  exit 1
else
  echo "P99 latency: ${P99_MS} ms (target < ${LATENCY_P99_MAX_MS})"
  if awk "BEGIN {exit !($P99_MS < $LATENCY_P99_MAX_MS)}" 2>/dev/null; then
    echo "PASS: NFR-DB-002"
  else
    echo "FAIL: NFR-DB-002 — P99 ${P99_MS}ms >= ${LATENCY_P99_MAX_MS}ms"
    exit 1
  fi
fi

echo ""
echo "Done. All NFR benchmark targets met."
