#!/usr/bin/env bash
# cerberOS IO component integration tests
# Usage: IO_URL=http://localhost:3001 ./integration.sh

set -euo pipefail

IO_URL="${IO_URL:-http://localhost:3001}"
PASS=0
FAIL=0

test_it() {
  local description="$1"
  local expected_code="$2"
  local expected_pattern="$3"
  shift 3
  local curl_args=("$@")

  local response
  local status_code

  # Capture status code from curl
  set +e
  response=$(curl -sS "${curl_args[@]}" -w "\n__STATUS__: %{http_code}" "$IO_URL${curl_args[-1]}" 2>&1)
  set -e

  status_code=$(echo "$response" | grep -o '__STATUS__: [0-9]*' | awk '{print $2}')
  body=$(echo "$response" | sed '/__STATUS__:/d')

  if [[ "$status_code" == "$expected_code" ]] && echo "$body" | grep -q "$expected_pattern"; then
    echo "  PASS  $description"
    ((PASS++))
  else
    echo "  FAIL  $description"
    echo "        Expected: $expected_code + '$expected_pattern'"
    echo "        Got status: $status_code"
    echo "        Body: ${body:0:200}"
    ((FAIL++))
  fi
}

echo "=== cerberOS IO Integration Tests ==="
echo "Target: $IO_URL"
echo ""

# 1. Health checks
test_it "GET /health returns 200" "200" "ok" -X GET "/health"
test_it "GET /api/health returns 200" "200" "ok" -X GET "/api/health"

# 2. Status endpoint
test_it "GET /api/status returns 200 with io_api ok" "200" '"io_api":"ok"' -X GET "/api/status"

# 3. Task list
test_it "GET /api/tasks returns 200" "200" '"tasks"' -X GET "/api/tasks"

# 4. Create a task
TASK_RESPONSE=$(curl -sS -X POST "$IO_URL/api/tasks" \
  -H 'Content-Type: application/json' \
  -d '{"content":"test integration task","userId":"test-user-001"}')
TASK_ID=$(echo "$TASK_RESPONSE" | grep -o '"taskId":"[^"]*"' | head -1 | cut -d'"' -f4)
if [[ -n "$TASK_ID" ]]; then
  test_it "GET /api/tasks/:taskId returns created task" "200" "$TASK_ID" -X GET "/api/tasks/$TASK_ID"
else
  echo "  FAIL  POST /api/tasks — could not extract taskId from response: $TASK_RESPONSE"
  ((FAIL++))
  TASK_ID="dummy-task-id"
fi

# 5. Chat streaming (demo mode)
test_it "POST /api/chat streams a response" "200" "" \
  -X POST "/api/chat" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d "{\"taskId\":\"$TASK_ID\",\"content\":\"hello\",\"conversationHistory\":[]}"

# 6. Log retrieval
test_it "GET /api/logs/:taskId returns 200" "200" '"logs"' -X GET "/api/logs/$TASK_ID"

# 7. Orchestrator HTTP bridge
test_it "POST /api/orchestrator/stream-events accepts valid status event" "200" '"ok":true' \
  -X POST "/api/orchestrator/stream-events" \
  -H 'Content-Type: application/json' \
  -d "{\"type\":\"status\",\"payload\":{\"taskId\":\"$TASK_ID\",\"status\":\"working\",\"lastUpdate\":\"test\",\"expectedNextInputMinutes\":1,\"timestamp\":$(date +%s)000}}"

# 8. Web dashboard HTML
test_it "GET / returns cerberOS dashboard HTML" "200" "cerberOS" -X GET "/"

# 9. SSE stream delivers injected events
# Open an SSE connection in background, inject a status event, verify it arrives
SSE_TASK_ID="${TASK_ID:-dummy-sse-task}"
SSE_OUT=$(mktemp)
timeout 3 curl -sS -N "$IO_URL/api/events/$SSE_TASK_ID" > "$SSE_OUT" 2>/dev/null &
SSE_PID=$!
sleep 0.5
# Inject a status event via HTTP bridge
curl -sS -X POST "$IO_URL/api/orchestrator/stream-events" \
  -H 'Content-Type: application/json' \
  -d "{\"type\":\"status\",\"payload\":{\"taskId\":\"$SSE_TASK_ID\",\"status\":\"working\",\"lastUpdate\":\"SSE test\",\"expectedNextInputMinutes\":1,\"timestamp\":$(date +%s)000}}" > /dev/null 2>&1
sleep 1
kill "$SSE_PID" 2>/dev/null || true
wait "$SSE_PID" 2>/dev/null || true
if grep -q "SSE test" "$SSE_OUT"; then
  echo "  PASS  SSE stream receives injected status event"
  ((PASS++))
else
  echo "  FAIL  SSE stream receives injected status event"
  echo "        SSE output: $(cat "$SSE_OUT" | head -5)"
  ((FAIL++))
fi
rm -f "$SSE_OUT"

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
