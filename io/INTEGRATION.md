# IO Component Integration Guide

This document is the authoritative integration reference for teams connecting to the cerberOS **IO component** (Input/Output surface layer). The IO component provides the primary user-facing dashboard for AI agent task management, handling real-time status updates, streamed chat replies, credential requests, and activity logging.

---

## 1. Quick Start

### Prerequisites

- Bun (runtime and package manager)
- Docker and Docker Compose

### Start the full stack

```bash
git clone https://github.com/cerberOS/cerberOS
cd cerberOS/cerberOS

# Start all services
docker compose up

# Or start just the IO API server (frontend connects to it directly)
cd io/api && bun run dev
```

### Open the dashboard

```
http://localhost:3001
```

The IO web app is served by the API server at the same port. The default deployment runs with `VITE_DEMO_MODE=true`, which provides a fully functional standalone experience with mock task data and simulated responses.

### Connect to the real orchestrator

```bash
# Disable demo mode to use live orchestrator data
export VITE_DEMO_MODE=false

# Rebuild the web app
cd io/surfaces/web && bun run build

# Restart the stack
docker compose up --build
```

---

## 2. Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    IO Dashboard (browser)                        │
│         React + Vite — surfaces/web/src/App.tsx                │
└───────────────────────────────┬─────────────────────────────────┘
                                │ HTTP POST /api/chat
                                │ SSE GET /api/events/{taskId}
                                │ SSE POST /api/orchestrator/stream-events
                                ▼
┌─────────────────────────────────────────────────────────────────┐
│                IO API Server (Bun / Hono BFF)                  │
│         io/api/src/index.ts — port 3001                        │
│         Responsibilities:                                       │
│           • SSE hub for orchestrator → UI push                  │
│           • Chat streaming proxy                                │
│           • Credential → Memory Vault proxy                     │
│           • Session log storage (fallback in-memory)             │
│           • Optional transcription endpoint                      │
└───────────────┬─────────────────────────┬───────────────────────┘
                │                         │
                │ NATS JetStream          │ HTTP bridge POST
                │ (production)            │ /api/orchestrator/stream-events
                ▼                         ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Orchestrator (Go)                            │
│         Responsibilities:                                        │
│           • Task lifecycle management                            │
│           • Agent coordination                                  │
│           • Emit status + credential_request events             │
│           • Stream chat replies                                  │
└───────────────┬─────────────────────────────────────────────────┘
                │
                │ NATS JetStream
                ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Agents Component                             │
│         Responsibilities:                                        │
│           • Execute task-specific work                           │
│           • Report progress to orchestrator                      │
│           • Request credentials via orchestrator                │
└─────────────────────────────────────────────────────────────────┘
```

### Data flow summary

| Direction | Transport | Description |
|-----------|-----------|-------------|
| Orchestrator → IO | SSE (or NATS bridge) | Status heartbeats, credential requests |
| IO → Orchestrator | HTTP POST | User messages via `/api/chat` |
| Orchestrator → IO (injection) | HTTP POST | Event injection via BFF hub |
| IO → Memory | HTTP POST | Session logs, credential vault writes |

---

## 3. For the Orchestrator Team

This section covers everything the orchestrator needs to integrate with the IO component.

### 3.1 NATS topics

The orchestrator publishes events on NATS JetStream. The IO API server subscribes to these topics (via the NATS bridge) and fans out to connected browser clients via SSE.

| Direction | Topic | Payload | Delivery |
|-----------|-------|---------|----------|
| IO → Orchestrator | `aegis.orchestrator.task.inbound` | `MessageEnvelope` wrapping `UserTask` | JetStream (at-least-once) |
| Orchestrator → IO | `aegis.orchestrator.task.result` | `MessageEnvelope` wrapping `TaskResult` | JetStream (at-least-once) |
| Orchestrator → IO | `aegis.orchestrator.agent.status` | `MessageEnvelope` wrapping agent status update | JetStream (at-least-once) |
| Orchestrator → IO | `aegis.orchestrator.credential.request` | `MessageEnvelope` wrapping `CredentialRequest` | JetStream (at-least-once) |
| Orchestrator → IO | `aegis.orchestrator.error` | `MessageEnvelope` wrapping error detail | JetStream (at-least-once) |

These follow the naming convention established by the agents component (`agents-component/internal/comms/subjects.go`). All `aegis.orchestrator.>` subjects are captured by the `AEGIS_ORCHESTRATOR` JetStream stream.

The IO API BFF maps these NATS messages to the SSE hub. When the NATS bridge is enabled (`NATS_URL` env var set), the BFF subscribes to `aegis.orchestrator.task.result`, `aegis.orchestrator.agent.status`, `aegis.orchestrator.credential.request`, and `aegis.orchestrator.error`, and publishes user tasks to `aegis.orchestrator.task.inbound`. When disabled, orchestrator events are injected via the HTTP endpoint described below.

### 3.2 UserTask schema

Tasks are identified by a string `taskId`. The IO component does not manage task creation — it receives status updates and renders them. The orchestrator is the source of truth for:

- Task identity (`taskId`)
- Task status (`'working' | 'awaiting_feedback' | 'completed'`)
- Human-readable status description (`lastUpdate`)
- Next input ETA (`expectedNextInputMinutes`)

### 3.3 HTTP bridge endpoint

The orchestrator (or any internal service) can push events directly to the IO API server via HTTP POST. This is the simplest integration path when NATS is not available.

```
POST /api/orchestrator/stream-events
Content-Type: application/json
```

**Request body — status event:**

```json
{
  "type": "status",
  "payload": {
    "taskId": "abc123",
    "status": "working",
    "lastUpdate": "Compiling Rust crate...",
    "expectedNextInputMinutes": 3,
    "timestamp": 1743891200000
  }
}
```

**Request body — credential request event:**

```json
{
  "type": "credential_request",
  "payload": {
    "taskId": "abc123",
    "requestId": "cred-001",
    "userId": "00000000-0000-0000-0000-000000000001",
    "keyName": "prod_db_admin_password",
    "label": "Production database admin password",
    "description": "Required to run the migration on the production cluster."
  }
}
```

**Response:**

```json
{ "ok": true }
```

**Notes:**
- Protect this endpoint in production with network ACLs, mTLS, or a shared secret header.
- The endpoint accepts both enveloped events (with `type` + `payload`) and legacy bare `StatusUpdate` objects (for backward compatibility during migration).

### 3.4 MessageEnvelope format

All events on the orchestrator → IO push channel use the following envelope:

```typescript
type OrchestratorStreamEvent =
  | { type: 'status'; payload: StatusUpdate }
  | { type: 'credential_request'; payload: CredentialRequest }
```

The SSE stream sends each envelope as a JSON line:

```
data: {"type":"status","payload":{"taskId":"abc123","status":"working",...}}\n\n
data: {"type":"credential_request","payload":{"taskId":"abc123",...}}\n\n
```

Reference implementation: `parseOrchestratorStreamEvent()` in `io/core/src/types.ts`.

### 3.5 How status events flow to the dashboard

```
Orchestrator (Go)
    │
    │ publishes to NATS topic io.status.{taskId}
    │  OR HTTP POST /api/orchestrator/stream-events
    ▼
IO API Server (BFF) — io/api/src/index.ts
    │
    │ stores latest StatusUpdate per taskId in memory Map
    │ broadcasts via SSE hub
    ▼
Browser client (SSE GET /api/events/{taskId})
    │
    │ OrchestratorStreamEvent parsed by parseOrchestratorStreamEvent()
    ▼
React state update → UI re-render
```

The IO BFF maintains an in-memory `Map<taskId, StatusUpdate>`. When a browser connects to the SSE stream for a task (`GET /api/events/{taskId}`), the BFF sends the current stored state immediately, then streams subsequent updates.

---

## 4. For the Memory Team

The IO component uses two Memory service endpoints: session logging and credential vault storage. Both are called from the IO API server (BFF), which acts as a proxy so the browser never directly contacts Memory.

### 4.1 Session logging

Appends a user or assistant message to a session log for persistence and analytics.

```
POST /api/v1/chat/{sessionId}/messages
X-API-KEY: {MEMORY_API_KEY}
Content-Type: application/json

{
  "userId": "00000000-0000-0000-0000-000000000001",
  "role": "user",
  "content": "Deploy the new schema to the production database",
  "taskId": "abc123"
}
```

**Response:**

```json
{
  "data": {
    "message": {
      "messageId": "msg-uuid",
      "sessionId": "sess-uuid",
      "userId": "00000000-0000-0000-0000-000000000001",
      "role": "user",
      "content": "Deploy the new schema...",
      "taskId": "abc123",
      "createdAt": "2026-04-05T10:45:00.000Z"
    }
  }
}
```

Retrieve logs:

```
GET /api/v1/chat/{sessionId}/messages?limit=50
X-API-KEY: {MEMORY_API_KEY}
```

**Response:**

```json
{
  "data": {
    "messages": [
      {
        "messageId": "msg-uuid",
        "sessionId": "sess-uuid",
        "userId": "00000000-0000-0000-0000-000000000001",
        "role": "user",
        "content": "Deploy the new schema...",
        "taskId": "abc123",
        "createdAt": "2026-04-05T10:45:00.000Z"
      }
    ]
  }
}
```

The IO API server provides an equivalent wrapper at `GET /api/logs/:taskId` that maps `taskId` to `sessionId` internally.

**Note on credentials:** Credentials are NEVER passed through this logging channel. The activity log records only `"Credential submitted through secure channel (content not logged)"`.

### 4.2 Credential vault storage

When a user provides a credential, the IO API server writes it directly to the Memory vault — the orchestrator never sees the plaintext value.

```
POST /api/v1/vault/{userId}/secrets
X-API-KEY: {MEMORY_VAULT_API_KEY}
X-Trace-ID: {optional-trace-id}
Content-Type: application/json

{
  "key_name": "prod_db_admin_password",
  "value": "hunter2"
}
```

**Response (201 Created):**

```json
{
  "taskId": "abc123",
  "requestId": "cred-001",
  "keyName": "prod_db_admin_password",
  "status": "stored"
}
```

Memory must encrypt the stored value server-side (AES-256-GCM recommended). The orchestrator retrieves the credential from the vault separately using `GET /api/v1/vault/{userId}/secrets?key_name={keyName}`.

### 4.3 Environment variables for Memory integration

| Variable | Default | Description |
|---------|---------|-------------|
| `MEMORY_API_BASE` | _(empty)_ | Base URL for Memory service (e.g. `http://localhost:8080`). When unset, IO API uses in-memory storage for session logs. |
| `MEMORY_API_KEY` | _(empty)_ | API key for Memory service authentication. |
| `MEMORY_VAULT_URL` | `http://localhost:8080/api/v1/vault` | Override for the vault endpoint base URL. |
| `MEMORY_VAULT_API_KEY` | _(empty)_ | API key for vault write operations. |

---

## 5. For the Agents Team

Agents do **not** integrate directly with the IO component. Agents report progress to the **orchestrator**, which is responsible for:

- Emitting status heartbeat events toward IO
- Requesting credentials through the orchestrator's credential request flow
- Receiving streamed chat replies from the orchestrator

```
Agents → Orchestrator → IO component → Browser
```

If you are implementing an agent, refer to the orchestrator's integration documentation for the correct event formats and NATS topics. The IO component is a downstream consumer — agents communicate upstream to the orchestrator, not sideways to IO.

---

## 6. Testing Without Other Components

### 6.1 IO API server standalone

The IO API server (`io/api`) is fully self-contained for development. It includes:

- **Mock chat responses**: `POST /api/chat` returns simulated streaming replies (defined in `generateMockResponse()`).
- **In-memory session logs**: `appendLogEntry()` and `getSessionLogs()` use an in-process array when `MEMORY_API_BASE` is unset.
- **Demo credential handling**: `POST /api/credential` simulates vault storage when the Memory service is unavailable.
- **Demo task 13 credential**: The SSE endpoint (`GET /api/events/13`) automatically pushes a credential request for task 13 after 900 ms.

Start it standalone:

```bash
cd io/api
bun run dev
```

### 6.2 IO web app standalone (DEMO_MODE=true)

The web app defaults to `VITE_DEMO_MODE=true` (see `.env.production`). When demo mode is active:

- `mockTasks` array provides 13 pre-populated tasks with realistic conversation history.
- The UI starts with mock task data already loaded — no orchestrator or Memory service needed.
- Mock heartbeat intervals generate simulated "working" activity in the activity log.
- The credential modal for task 13 still appears when that task is selected.
- Users can create new tasks via the sidebar and chat normally (responses come from the mock generator in the BFF).

```bash
cd io/surfaces/web
bun run dev       # uses .env defaults (VITE_DEMO_MODE=true)
```

### 6.3 What the integration test covers

The integration test suite (if present) validates the full event pipeline:

1. **Orchestrator → IO push**: A status event sent to `POST /api/orchestrator/stream-events` is received by a browser SSE client connected to `GET /api/events/{taskId}`.
2. **IO → Orchestrator chat**: A message sent to `POST /api/chat` returns a streamed response and updates task status.
3. **Credential flow**: A credential request pushed by the orchestrator surfaces the modal; a credential submitted via `POST /api/credential` is proxied to the Memory vault.
4. **Session logging**: Chat messages are appended via the memory client and retrievable via `GET /api/logs/:taskId`.

### 6.4 Environment matrix

| Mode | VITE_DEMO_MODE | MEMORY_API_BASE | Orchestrator |
|------|---------------|-----------------|-------------|
| Full demo | `true` | _(unset)_ | _(not needed)_ |
| API standalone | `false` | _(unset)_ | _(not needed)_ |
| API + Memory | `false` | `http://localhost:8080` | _(not needed)_ |
| Full stack | `false` | `http://localhost:8080` | _(connected)_ |

---

## 7. Environment Variables Reference

### Web UI (`io/surfaces/web`)

| Variable | Default | Description |
|---------|---------|-------------|
| `VITE_DEMO_MODE` | `false` | When `true`, uses `mockTasks` array for initial task list and mock heartbeat intervals. Set `true` in `.env.production` for the default demo build. |
| `VITE_ORCHESTRATOR_SSE` | `true` | When `false`, disables real-time SSE subscription and uses local mock heartbeats. |
| `VITE_IO_API_BASE` | _(empty)_ | Override the IO API base URL. Empty uses the Vite dev proxy (`/api` → `http://localhost:3001`). |

### API Server (`io/api`)

| Variable | Default | Description |
|---------|---------|-------------|
| `MEMORY_API_BASE` | _(empty)_ | Memory service base URL. When unset, session logs use in-process in-memory storage. |
| `MEMORY_API_KEY` | _(empty)_ | API key for Memory service authentication. |
| `MEMORY_VAULT_URL` | `http://localhost:8080/api/v1/vault` | Memory vault base URL for credential storage. |
| `MEMORY_VAULT_API_KEY` | _(empty)_ | API key for vault write operations. |
| `NATS_URL` | _(empty)_ | NATS server URL. When set, the BFF subscribes to NATS topics and fans out to SSE clients. |
| `NATS_CREDS` | _(empty)_ | NATS credentials file path (optional). |

---

## 8. API Endpoint Reference

All endpoints are served by the IO API server (Bun/Hono) on port 3001. Base URL: `http://localhost:3001`.

### Health

```
GET /health
GET /api/health
```

Response:
```json
{ "status": "ok", "timestamp": "2026-04-05T10:00:00.000Z" }
```

### Task status

```
GET /api/tasks
```

Returns all tasks currently tracked by the BFF.

Response:
```json
{
  "tasks": [
    {
      "taskId": "abc123",
      "status": "working",
      "lastUpdate": "Compiling Rust crate...",
      "expectedNextInputMinutes": 3,
      "timestamp": 1743891200000
    }
  ]
}
```

```
GET /api/tasks/:taskId
```

Returns status for a specific task. Returns 404 if not found.

### Orchestrator event injection

```
POST /api/orchestrator/stream-events
Content-Type: application/json
```

Accepts enveloped `OrchestratorStreamEvent` objects. See Section 3.3 for request/response shapes.

### Chat streaming

```
POST /api/chat
Content-Type: application/json

{
  "taskId": "abc123",
  "content": "Deploy the new schema to production",
  "conversationHistory": [
    { "role": "user", "content": "Initial request" },
    { "role": "assistant", "content": "I'll prepare the migration script." }
  ]
}
```

Response: SSE stream of chunks.

```
data: {"chunk":"I'm analyzing"}\n\n
data: {"chunk":"I'm analyzing your request"}\n\n
data: {"chunk":"I'm analyzing your request and preparing"}\n\n
...
data: {"done":true}\n\n
```

The BFF also logs both the user message and the final assistant message to Memory and updates the task status.

### SSE push stream (per task)

```
GET /api/events/:taskId
```

Opens a Server-Sent Events stream for a specific task. Streams:
1. The current stored status for the task (if any).
2. All subsequent status updates and credential requests broadcast for `taskId`.
3. For `taskId === "13"`: a credential request event after ~900 ms (demo credential).
4. Periodic heartbeat status updates every 2.8 s for tasks with `status === "working"`.

Event format per `data:` line:
```json
{"type":"status","payload":{"taskId":"13","status":"working","lastUpdate":"Running migration…","expectedNextInputMinutes":3,"timestamp":1743891200000}}
```

### Session logs

```
GET /api/logs/:taskId
```

Returns session log entries for a task, mapped from the Memory client.

Response:
```json
{
  "logs": [
    {
      "taskId": "abc123",
      "role": "user",
      "content": "Deploy the new schema to production",
      "at": "2026-04-05T10:45:00.000Z"
    }
  ]
}
```

### Credential submission

```
POST /api/credential
Content-Type: application/json

{
  "taskId": "abc123",
  "requestId": "cred-001",
  "userId": "00000000-0000-0000-0000-000000000001",
  "keyName": "prod_db_admin_password",
  "value": "hunter2"
}
```

Proxies to Memory vault. On success, returns 201:

```json
{
  "taskId": "abc123",
  "requestId": "cred-001",
  "keyName": "prod_db_admin_password",
  "status": "stored"
}
```

On vault error, returns 500 with status `"error"`. On network failure (Memory unavailable), simulates success with `status: "stored"` to avoid blocking the UI.

### Voice transcription

```
POST /api/voice/transcribe
Content-Type: application/json

{
  "audioData": "base64-encoded-audio",
  "format": "wav",
  "language": "en"
}
```

Response:
```json
{ "text": "Transcribed text here" }
```

Requires the transcription worker to be running. Returns 500 if transcription fails.

---

## 9. SSE Event Format

All events on the orchestrator → IO push channel (SSE stream at `GET /api/events/:taskId`) use the **MessageEnvelope** format defined in `io-interfaces.md §1.0`.

### Envelope structure

```typescript
type OrchestratorStreamEvent =
  | { type: 'status'; payload: StatusUpdate }
  | { type: 'credential_request'; payload: CredentialRequest }
```

### Status event

```json
{
  "type": "status",
  "payload": {
    "taskId": "abc123",
    "status": "working",
    "lastUpdate": "Compiling Rust crate...",
    "expectedNextInputMinutes": 3,
    "timestamp": 1743891200000
  }
}
```

- `status`: `"working"` = agent is active; `"awaiting_feedback"` = needs user input; `"completed"` = done.
- `lastUpdate`: Short human-readable string describing current activity.
- `expectedNextInputMinutes`: Minutes from now until user input is expected. `0` = needed now; `null` = no further input needed.
- `timestamp`: Unix epoch milliseconds. Optional but recommended for accurate heartbeat display.

### Credential request event

```json
{
  "type": "credential_request",
  "payload": {
    "taskId": "abc123",
    "requestId": "cred-001",
    "userId": "00000000-0000-0000-0000-000000000001",
    "keyName": "prod_db_admin_password",
    "label": "Production database admin password",
    "description": "Required to run the migration on the production cluster."
  }
}
```

### SSE delivery

Each event is sent as a single `data:` line terminated by double newlines:

```
data: {"type":"status","payload":{...}}\n\n
data: {"type":"credential_request","payload":{...}}\n\n
```

The IO web client (`subscribeOrchestratorTaskStream()` in `io/surfaces/web/src/api/orchestrator.ts`) parses each `data:` line as JSON and routes it to the appropriate state handler (status update → task list; credential request → modal).

---

## Appendix A: File Map

| File | Purpose |
|------|---------|
| `io/surfaces/web/src/App.tsx` | Primary React dashboard component |
| `io/surfaces/web/src/api/orchestrator.ts` | API client: SSE subscription, chat streaming, credential submission |
| `io/api/src/index.ts` | IO API server (Bun/Hono BFF) |
| `io/core/src/types.ts` | Shared TypeScript types and `parseOrchestratorStreamEvent()` |
| `io/core/src/memory-client.ts` | Memory service client with in-memory fallback |
| `io/surfaces/web/vite.config.ts` | Vite config with `VITE_DEMO_MODE` build-time define |
| `io/surfaces/web/.env` | Default env (demo mode off) |
| `io/surfaces/web/.env.production` | Production env (demo mode on) |
| `io/io-interfaces.md` | Full interface specification |

---

## Appendix B: Docker Configuration

The IO web app is built in Docker with `VITE_DEMO_MODE` passed as a build argument:

```dockerfile
# Example Dockerfile snippet
FROM oven/bun:1-alpine AS builder
WORKDIR /app
COPY package.json bun.lockb ./
RUN bun install --frozen-lockfile
COPY . .
ARG VITE_DEMO_MODE=false
ENV VITE_DEMO_MODE=${VITE_DEMO_MODE}
RUN bun run build
```

Build and run:
```bash
docker build --build-arg VITE_DEMO_MODE=false -t cerberos-io .
docker run -p 3001:3001 cerberos-io
```
