# IO Component Docker & Integration Plan

## Context

The IO component needs a single Docker container that runs the full IO surface (web dashboard + API server), replaces mock/demo responses with real "not connected" behavior, and exposes well-documented ports for the orchestrator and memory teams to integrate against.

### Key Architecture Insight: NATS, Not HTTP

The orchestrator communicates via **NATS JetStream**, not HTTP. From `orchestrator/internal/gateway/gateway.go`:

```
IO Dashboard (browser)
    ↕ HTTP / SSE
IO API Server (Bun/Hono BFF)
    ↕ NATS JetStream
Orchestrator (Go)
    ↕ NATS JetStream
Agents Component
```

**NATS Topics (from orchestrator EDD §11.8):**

| Direction | Topic | Payload |
|-----------|-------|---------|
| IO → Orchestrator | `aegis.orchestrator.task.inbound` | `MessageEnvelope` wrapping `UserTask` |
| Orchestrator → IO | `aegis.orchestrator.task.result` | `MessageEnvelope` wrapping `TaskResult` |
| Orchestrator → IO | `aegis.orchestrator.agent.status` | `MessageEnvelope` wrapping agent status |
| Orchestrator → IO | `aegis.orchestrator.credential.request` | `MessageEnvelope` wrapping `CredentialRequest` |
| Orchestrator → IO | `aegis.orchestrator.error` | `MessageEnvelope` wrapping error detail |

**MessageEnvelope** (all messages must use this):
```json
{
  "message_id": "uuid",
  "message_type": "user_task",
  "source_component": "io",
  "correlation_id": "task-uuid",
  "timestamp": "2026-04-05T...",
  "schema_version": "1.0",
  "payload": { ... }
}
```

**UserTask** (IO → Orchestrator inbound):
```json
{
  "task_id": "uuid",
  "user_id": "uuid",
  "required_skill_domains": [],
  "priority": 5,
  "timeout_seconds": 300,
  "payload": { "raw_input": "user's message text" },
  "callback_topic": "aegis.orchestrator.tasks.results.<task_id>",
  "user_context_id": "session-uuid"
}
```

The IO API server already has `POST /api/orchestrator/stream-events` as an HTTP injection endpoint. This can serve as a **bridge** for testing: the orchestrator team (or a test harness) can POST status/result events into this endpoint without NATS being wired up yet.

### Current State

- **Web dashboard**: Complete React app with task sidebar, chat, credentials, activity log, SSE streaming
- **IO API**: Hono server on port 3001 — chat, logs, credentials, SSE hub, voice transcription
- **Mock data**: `generateMockResponse()` in API, `mockTasks[]` in App.tsx, `DEMO_TASK_13_CREDENTIAL` hardcoded
- **Docker**: API-only Dockerfile exists + docker-compose, but does NOT include the web dashboard
- **No NATS client**: IO API currently has no NATS dependency

---

## Plan

### Step 1: Unified Dockerfile (IO API + Web Dashboard)

**Goal**: Single container serves the built dashboard as static files and runs the API server.

**Approach**: Multi-stage Docker build:
1. **Stage 1 (build-web)**: Install deps, run `vite build` for the web dashboard → produces `dist/`
2. **Stage 2 (runtime)**: Bun base image, copy API source + built dashboard static files, configure the API server to serve static files for non-`/api` routes

**Files to create/modify**:
- `io/Dockerfile` — New unified Dockerfile (replaces `io/api/Dockerfile`)
- `io/api/src/index.ts` — Add static file serving for production mode

**Static file serving approach** — Add to the Hono API server:
```typescript
// In production, serve the built web dashboard
if (process.env.NODE_ENV === 'production') {
  // Serve static files from /app/web-dist
  // Fall through to index.html for SPA client-side routing
}
```

**Port**: Single port `3001` serves both the API (`/api/*`) and the dashboard (`/*`).

**Why single container**: The IO component is one logical unit. Splitting API and dashboard into separate containers adds unnecessary networking complexity for what is fundamentally a BFF + its frontend. The orchestrator, memory, and agents teams don't need to know about internal IO service topology.

### Step 2: Remove Mock Responses, Add Orchestrator Connection Status + Demo Mode

**Goal**: When the orchestrator is not connected, the system clearly communicates this instead of faking responses. A `DEMO_MODE` toggle preserves the full mock experience for presentations.

**2.0 Demo Mode Toggle**

Controlled by `DEMO_MODE` env var (API) and `VITE_DEMO_MODE` (dashboard build-time).

| `DEMO_MODE` | Behavior |
|-------------|----------|
| `true` | Mock tasks pre-loaded, `generateMockResponse()` active, credential demo for task 13, mock heartbeats — identical to current midterm branch |
| `false` (default) | Empty task list, "orchestrator not connected" responses, no demo credentials, real SSE only |

**API Server** — wrap existing mock logic behind a flag:
```typescript
const DEMO_MODE = process.env.DEMO_MODE === 'true'

// In POST /api/chat:
if (DEMO_MODE) {
  // existing generateMockResponse() path — unchanged
} else if (natsClient?.connected) {
  // forward to orchestrator via NATS (Step 5)
} else {
  // yield "Orchestrator is not connected" response
}

// In GET /api/events/:taskId:
if (DEMO_MODE && taskId === '13') {
  // push demo credential request — unchanged
}
```

**Dashboard** — wrap mock data behind build-time flag:
```typescript
const DEMO_MODE = import.meta.env.VITE_DEMO_MODE === 'true'

const [tasks, setTasks] = useState<Task[]>(DEMO_MODE ? mockTasks : [])
const [selectedTaskId, setSelectedTaskId] = useState<string | null>(
  DEMO_MODE ? mockTasks[0].id : null
)
```

This means `mockTasks[]`, `generateMockResponse()`, and `DEMO_TASK_13_CREDENTIAL` stay in the codebase but are **gated** — not deleted. No code is lost, and `DEMO_MODE=true docker compose up` gives the exact midterm demo experience.

**2a. API Server (`io/api/src/index.ts`)** — changes for non-demo mode:

- **Gate** `generateMockResponse()` behind `DEMO_MODE` check
- **Gate** `DEMO_TASK_13_CREDENTIAL` and the task-13 SSE push behind `DEMO_MODE`
- **Add** the "orchestrator not connected" fallback for when `DEMO_MODE=false` and no NATS:

```typescript
async function* orchestratorNotConnected(): AsyncGenerator<string> {
  yield JSON.stringify({
    chunk: '[IO] Orchestrator is not connected. The message was logged but cannot be processed.\n\n' +
           'To connect the orchestrator, configure NATS_URL or start the orchestrator service.'
  })
  yield JSON.stringify({ done: true })
}
```

- **Add** `GET /api/status` endpoint that reports component connectivity:
```json
{
  "io_api": "ok",
  "demo_mode": false,
  "orchestrator": "disconnected",
  "memory": "disconnected",
  "nats": "disconnected",
  "web_dashboard": "serving"
}
```

**2b. Web Dashboard (`io/surfaces/web/src/App.tsx`)** — changes for non-demo mode:

- **Gate** `mockTasks[]` initialization behind `VITE_DEMO_MODE`
- **Add** a "Create Task" UI flow so the user can submit a prompt without pre-loaded mock data
- **Gate** `FALLBACK_TASK_13_CREDENTIAL` and the mock heartbeat timer behind `VITE_DEMO_MODE`
- **When not in demo mode and no tasks exist**, show an empty state: "No active tasks. Submit a prompt to create one."
- **Keep** the mock heartbeat _only_ as a fallback when SSE is unavailable (existing `useMockHeartbeat` pattern), but only with real task data (not mock tasks)

### Step 3: Task Creation Endpoint

**Goal**: The dashboard needs a way to create new tasks (currently tasks are hardcoded).

**Add** `POST /api/tasks` to the API server:
```typescript
// Creates a new task and returns its ID
// When orchestrator is connected: publishes UserTask to NATS
// When orchestrator is disconnected: creates local task in "pending" state
app.post('/api/tasks', async (c) => {
  const { content, userId } = await c.req.json()
  const taskId = crypto.randomUUID()

  // Create local task state
  const task: StatusUpdate = {
    taskId,
    status: 'awaiting_feedback',
    lastUpdate: 'Task created — awaiting orchestrator',
    expectedNextInputMinutes: null,
    timestamp: Date.now(),
  }
  tasks.set(taskId, task)
  broadcastStatus(taskId, task)

  // TODO (Step 5): If NATS connected, publish UserTask envelope

  return c.json({ taskId, status: 'created' })
})
```

**Update** the web dashboard to call this endpoint when the user submits a new task prompt.

### Step 4: Update docker-compose.yml

**Goal**: Single-service compose file with documented env vars for all teams.

```yaml
services:
  io:
    build:
      context: "."
      dockerfile: Dockerfile
    ports:
      - "3001:3001"     # IO API + Web Dashboard
    environment:
      - NODE_ENV=production

      # ── Demo Mode ──
      # Set to 'true' for presentation mode (mock tasks, fake responses, credential demo)
      # Default 'false' = integration mode (empty state, real orchestrator communication)
      - DEMO_MODE=false

      # ── Orchestrator Integration ──
      # NATS connection (required for real orchestrator communication)
      # The orchestrator team should provide NATS_URL and NATS_CREDS_PATH
      - NATS_URL=                              # e.g. nats://nats:4222
      # - NATS_CREDS_PATH=                     # Path to mTLS credentials

      # HTTP bridge (alternative to NATS for testing)
      # Orchestrator can POST events to http://<io-host>:3001/api/orchestrator/stream-events
      # No config needed — always available

      # ── Memory Integration ──
      - MEMORY_API_BASE=                       # e.g. http://memory-api:8080
      - MEMORY_API_KEY=
      - MEMORY_VAULT_URL=                      # e.g. http://memory-api:8080/api/v1/vault
      - MEMORY_VAULT_API_KEY=

      # ── Voice (optional) ──
      - STT_PROVIDER=local                     # 'local' uses faster-whisper, 'none' disables
    networks:
      - cerberos

networks:
  cerberos:
    name: cerberos
    external: true   # Other teams create this network in their own compose files
```

### Step 5: NATS Client in IO API (Future — Stub Now)

**Goal**: Prepare the NATS integration point without requiring NATS to be running.

**Create** `io/api/src/nats/client.ts`:
```typescript
// NATS client for IO ↔ Orchestrator communication
// Publishes UserTask messages, subscribes for results/status/errors

export interface NatsConfig {
  url: string          // NATS_URL env var
  credsPath?: string   // NATS_CREDS_PATH env var
}

export interface IONatsClient {
  connected: boolean
  publishUserTask(task: UserTask): Promise<void>
  subscribeTaskResults(taskId: string, handler: (result: TaskResult) => void): () => void
  subscribeStatusEvents(handler: (status: StatusResponse) => void): () => void
  close(): void
}

// Returns null when NATS_URL is not configured
export function createNatsClient(config: NatsConfig): IONatsClient | null {
  if (!config.url) return null
  // TODO: Implement with nats.ws or nats (Bun-compatible NATS client)
  // For now, return null — all orchestrator communication falls back to
  // the HTTP bridge (POST /api/orchestrator/stream-events)
  return null
}
```

**Wire into API server startup**:
```typescript
const natsClient = createNatsClient({
  url: process.env.NATS_URL ?? '',
  credsPath: process.env.NATS_CREDS_PATH,
})
console.log(`[IO] Orchestrator transport: ${natsClient ? 'NATS' : 'HTTP bridge (POST /api/orchestrator/stream-events)'}`)
```

**NATS Topic Mapping** (for when the client is implemented):

| IO Action | NATS Topic | Message Type |
|-----------|-----------|--------------|
| User submits task | `aegis.orchestrator.task.inbound` | `UserTask` in `MessageEnvelope` |
| Listen for results | `aegis.orchestrator.task.result` | `TaskResult` in `MessageEnvelope` |
| Listen for status | `aegis.orchestrator.agent.status` | Agent status in `MessageEnvelope` |
| Listen for creds | `aegis.orchestrator.credential.request` | `CredentialRequest` in `MessageEnvelope` |
| Listen for errors | `aegis.orchestrator.error` | Error detail in `MessageEnvelope` |

### Step 6: Integration Test Script

**Goal**: Verify the Docker container works and sends/receives correct data shapes.

**Create** `io/test/integration.sh`:

```bash
#!/usr/bin/env bash
# IO Component Integration Test
# Tests that the Docker container:
# 1. Starts and serves the dashboard
# 2. Health endpoint responds
# 3. Chat endpoint handles "orchestrator not connected" gracefully
# 4. SSE subscription works
# 5. Status endpoint reports component connectivity
# 6. Request/response shapes match io-interfaces.md contracts

set -euo pipefail
IO_URL="${IO_URL:-http://localhost:3001}"
PASS=0; FAIL=0

test_it() { ... }

# Test 1: Health check
test_it "Health endpoint" \
  curl -sf "$IO_URL/api/health" | jq -e '.status == "ok"'

# Test 2: Status endpoint
test_it "Status endpoint" \
  curl -sf "$IO_URL/api/status" | jq -e '.io_api == "ok"'

# Test 3: Dashboard serves HTML
test_it "Dashboard serves index.html" \
  curl -sf "$IO_URL/" | grep -q 'cerberOS'

# Test 4: Create task
TASK_ID=$(curl -sf -X POST "$IO_URL/api/tasks" \
  -H 'Content-Type: application/json' \
  -d '{"content":"Test task","userId":"test-user"}' | jq -r '.taskId')
test_it "Task creation returns UUID" [ -n "$TASK_ID" ]

# Test 5: Chat with orchestrator disconnected
test_it "Chat returns not-connected message" \
  curl -sf -X POST "$IO_URL/api/chat" \
  -H 'Content-Type: application/json' \
  -d "{\"taskId\":\"$TASK_ID\",\"content\":\"hello\",\"conversationHistory\":[]}" \
  | grep -q 'not connected'

# Test 6: SSE subscription opens
test_it "SSE endpoint responds" \
  timeout 2 curl -sf -N "$IO_URL/api/events/$TASK_ID" | head -1 | grep -q 'data:'

# Test 7: HTTP bridge injection (simulates orchestrator pushing an event)
test_it "HTTP bridge accepts orchestrator events" \
  curl -sf -X POST "$IO_URL/api/orchestrator/stream-events" \
  -H 'Content-Type: application/json' \
  -d "{\"type\":\"status\",\"payload\":{\"taskId\":\"$TASK_ID\",\"status\":\"working\",\"lastUpdate\":\"Integration test\",\"expectedNextInputMinutes\":1,\"timestamp\":$(date +%s000)}}" \
  | jq -e '.ok == true'

# Test 8: Logs endpoint returns array
test_it "Logs endpoint returns array" \
  curl -sf "$IO_URL/api/logs/$TASK_ID" | jq -e '.logs | type == "array"'

echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
```

### Step 6b: Structured Request/Response Logging

**Goal**: Make the data flowing through the IO API visible in Docker logs so you (and other teams) can see exact JSON payloads during manual testing.

**Add** a logging middleware/utility to the API server that logs every inbound and outbound message with its full shape:

```
[IO:REQ]  POST /api/chat ← { taskId: "abc-123", content: "deploy the app", conversationHistory: [] }
[IO:ORCH] Orchestrator not connected — returning fallback response
[IO:REQ]  POST /api/orchestrator/stream-events ← { type: "status", payload: { taskId: "abc-123", status: "working", ... } }
[IO:SSE]  Broadcast taskId=abc-123 → StatusUpdate { status: "working", lastUpdate: "Running deploy script" }
[IO:REQ]  POST /api/credential ← { taskId: "abc-123", requestId: "cred-1", userId: "...", keyName: "prod_db_password" } [value REDACTED]
[IO:MEM]  appendLogEntry → { sessionId: "...", role: "user", content: "deploy the app" }
```

**Rules**:
- Log the full JSON body for all API requests (except credential `value` — always redact)
- Log SSE broadcasts with the event type and taskId
- Log memory-client calls with the operation and key fields
- Use prefixes (`IO:REQ`, `IO:SSE`, `IO:MEM`, `IO:ORCH`) so you can grep Docker logs by subsystem
- Gated behind `LOG_LEVEL=debug` env var (default: `info` which only logs one-line summaries)

This means running `docker compose up` and interacting with the dashboard gives you a live view of every data exchange in the terminal — you can verify the shapes match `io-interfaces.md` by reading the logs.

### Step 7: Integration Documentation

**Goal**: Tell other teams exactly how to integrate with the IO container.

**Create** `io/INTEGRATION.md`:

Sections:
1. **Quick Start** — `docker compose up` and open `http://localhost:3001`
2. **Architecture** — Diagram showing IO container internals (BFF + dashboard)
3. **For the Orchestrator Team** — NATS topics, UserTask schema, HTTP bridge for testing, how status events flow to the dashboard
4. **For the Memory Team** — API endpoints the IO server calls, env vars to configure
5. **For the Agents Team** — No direct integration (agents talk to orchestrator, not IO)
6. **Testing Without Other Components** — How IO behaves standalone, what the integration test covers
7. **Environment Variables Reference** — Complete table of all env vars with defaults
8. **API Endpoint Reference** — All HTTP endpoints with request/response shapes
9. **SSE Event Format** — The enveloped event format per io-interfaces.md §1.0

---

## Execution Order

```
Step 1: Unified Dockerfile ─────────────────────┐
Step 2: Demo mode toggle + connection status ────┤ Can parallelize
Step 3: Task creation endpoint ──────────────────┘
                │
                ▼
Step 4: Update docker-compose.yml
                │
                ▼
Step 5: NATS client stub
                │
                ▼
Step 6: Integration test script
                │
                ▼
Step 6b: Structured request/response logging
                │
                ▼
Step 7: Integration documentation
                │
                ▼
        Docker build + test run
```

**Steps 1, 2, 3** are independent and can be done in parallel.
**Steps 4-7** are sequential but lightweight (mostly config and docs).

---

## Files Changed / Created

| File | Action | Step |
|------|--------|------|
| `io/Dockerfile` | **Create** (unified, replaces api/Dockerfile) | 1 |
| `io/api/Dockerfile` | **Delete** | 1 |
| `io/api/src/index.ts` | **Modify** (static serving, demo mode gates, /api/status, /api/tasks, structured logging) | 1, 2, 3, 6b |
| `io/surfaces/web/src/App.tsx` | **Modify** (demo mode gate on mockTasks, add empty state + task creation) | 2 |
| `io/docker-compose.yml` | **Modify** (single service, documented env vars) | 4 |
| `io/api/src/nats/client.ts` | **Create** (stub) | 5 |
| `io/test/integration.sh` | **Create** | 6 |
| `io/INTEGRATION.md` | **Create** | 7 |

---

## Design Decisions

### Why not separate containers for API and dashboard?
The IO API is a BFF — it exists to serve the dashboard. Splitting them adds a reverse proxy, CORS config, and networking complexity for no benefit. Other teams interact with one endpoint: `http://io:3001`.

### Why HTTP bridge AND NATS?
The HTTP bridge (`POST /api/orchestrator/stream-events`) is already built and works. It lets the orchestrator team test against IO without NATS infrastructure. When NATS is wired up, the same internal event flow is used — the bridge just becomes another ingress path.

### Why not implement NATS now?
The orchestrator's NATS is not running yet (main.go is all TODOs). Building a full NATS client now means testing against nothing. The stub + HTTP bridge is the pragmatic path. When the orchestrator team has NATS running, we wire up the client.

### Why keep voice/transcription in the container?
The faster-whisper transcription is a significant image size cost (~500MB+ for Python + model). For the integration test, it can be disabled with `STT_PROVIDER=none`. A follow-up can split it into a sidecar if size becomes an issue. For now, one container keeps things simple.

### What about the CLI surface?
The CLI connects to the IO API server over HTTP. It doesn't need to be in the Docker container — it runs on the user's machine and points at `http://localhost:3001`. No changes needed for CLI.

---

## Testing Checklist

- [ ] `docker compose up` starts the container
- [ ] `http://localhost:3001` loads the dashboard
- [ ] `http://localhost:3001/api/health` returns `{"status":"ok"}`
- [ ] `http://localhost:3001/api/status` shows connectivity for all components
- [ ] Submitting a prompt in the dashboard creates a task and shows "orchestrator not connected"
- [ ] SSE subscription on `/api/events/<taskId>` stays open
- [ ] `POST /api/orchestrator/stream-events` injects events into the SSE stream
- [ ] Credential flow works (with Memory Vault down, gracefully simulates storage)
- [ ] `io/test/integration.sh` passes all checks
- [ ] No mock data or fake responses remain in the codebase
