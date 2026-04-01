# IO Interfaces

This document describes the format and requirements for the **IO component**'s interfaces with the **orchestrator** and **memory** components. The spec targets production functionality; the current demo may use simplified mocks (e.g. display-oriented strings) that should be replaced with these contracts for real integration.

---

## 1. Orchestrator

### 1.1 Status updates (semantic heartbeat)

The IO component needs **heartbeat-like status updates** from the orchestrator for each task so the UI can show whether a task is being worked on, is awaiting user input, or is done, and when the next user input is expected.

- **Orchestrator → IO**: The orchestrator (or task runtime) pushes or exposes status updates per task.
- **IO** displays: task status, last update text, seconds since last heartbeat (for "working" tasks), and estimated time until next user input.

**Status update payload (recommended shape)**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskId` | `string` | Yes | Unique identifier of the task. |
| `status` | `'awaiting_feedback' \| 'working' \| 'completed'` | Yes | Current task state. |
| `lastUpdate` | `string` | Yes | Short human-readable description of what is being done or what is needed (e.g. "Creating chart components…", "Awaiting user approval for OAuth provider selection"). |
| `expectedNextInputMinutes` | `number \| null` | Yes | Minutes from now until the next user input is expected. `0` = input needed now; positive number = estimated minutes until next input; `null` = task completed or no further input needed. IO is responsible for formatting for display (e.g. 0 → "Now", null → "Done", 5 → "~5 min"). |
| `timestamp` | `string` (ISO 8601) or `number` (ms) | Optional | When this update was produced; used for "seconds since last heartbeat" if not provided by transport. |

**Frequency and delivery**

- **Frequency**: Updates should be emitted on the order of **every 1–4 seconds** per task when the task is active (e.g. "working"). The interval may be randomized per task (e.g. 2–4 s in 100 ms steps) so not all tasks heartbeat at the same time.
- **Delivery**: Either **push** (e.g. WebSocket, SSE) from orchestrator to IO, or **polling** by IO against an orchestrator endpoint. Push is preferred for low latency.
- **Semantic meaning**: The heartbeat is "semantic": it describes current activity and when the next user action is needed, not only liveness.

**IO behavior**

- **Working** tasks: Show "seconds since last heartbeat" (reset to 0 when an update is received); display `lastUpdate` and derive display from `expectedNextInputMinutes` (e.g. 0 → "Now", 5 → "~5 min").
- **Awaiting feedback**: Highlight that user input is needed; `expectedNextInputMinutes` is 0.
- **Completed**: Treat as done; `expectedNextInputMinutes` is `null`.

### 1.2 Chat (task conversation)

The IO component sends **user messages** to the orchestrator for a given task and receives **streamed assistant replies**.

- **IO → Orchestrator**: Submit user message for a task (with optional conversation history).
- **Orchestrator → IO**: Stream assistant reply as a sequence of chunks (no single blocking "full response" only).

**Send user message (IO → Orchestrator)**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskId` | `string` | Yes | Task this message belongs to. |
| `content` | `string` | Yes | User's message text. |
| `conversationHistory` | `Array<{ role: 'user' \| 'assistant', content: string }>` | Optional | Previous messages in order for context. |

The orchestrator must associate the message with the task and return a reply tied to that task. Replies must be **streamed** (e.g. token-by-token or chunk-by-chunk), not only returned as one full string after completion.

**Stream assistant reply (Orchestrator → IO)**

- **Format**: Stream of **text chunks** (e.g. SSE, WebSocket, or async generator yielding strings).
- **Semantics**: Each chunk is **incremental**; the client accumulates chunks to form the full reply. The IO component should display the accumulated content as it arrives (e.g. live-growing bubble).
- **End**: Stream end signals completion of the reply for that turn.

**Message shape (for history / logging)**

| Field | Type | Description |
|-------|------|-------------|
| `role` | `'user' \| 'assistant'` | Sender. |
| `content` | `string` | Full message text (user message as sent; assistant message as full accumulated reply when stream completes). |

On stream or network failure, the orchestrator (or gateway) should communicate failure so the IO can show an error and optionally retry; the user's message should still be logged (see Memory).

### 1.3 Credentials (secure channel)

When an agent task requires a secret (password, API key, token, etc.), the orchestrator sends a **credential request** to the IO component. The IO surfaces a dedicated modal — **outside** the chat DOM — so the credential never enters the chat pipeline, conversation history, or logging pipeline.

**Credential request (Orchestrator → IO)**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskId` | `string` | Yes | Task that needs the credential. |
| `requestId` | `string` | Yes | Unique per request. Used for idempotency and correlation with the submission acknowledgment. |
| `userId` | `string` (UUID) | Yes | User ID for vault storage. Maps to the Memory service's vault namespace. |
| `keyName` | `string` | Yes | Key name under which the credential will be stored in the vault (e.g. `"prod_db_password"`). |
| `label` | `string` | Yes | Human-readable label shown to the user, e.g. "Production DB password". |
| `description` | `string` | No | Optional explanation, e.g. "Required to run migration on prod cluster". |

**Credential flow**

```
Orchestrator ──CredentialRequest──> IO (surfaces modal)
                                    │
                                    │ user enters secret
                                    ▼
IO ─────POST /api/v1/vault/{userId}/secrets────> Memory (vault)
          { key_name, value }                    │
                                                 │ encrypted + stored
                                                 ▼
IO <────────────── 201 Created ─────────────────┘
│
│ acknowledgment (no secret)
▼
IO ─────CredentialAck────> Orchestrator
      { taskId, requestId, keyName, status: 'stored' }
```

**Credential submission (IO → Memory Vault API)**

IO writes the credential **directly to the Memory service's vault endpoint**, bypassing the Orchestrator. This ensures the Orchestrator never handles plaintext secrets.

| Memory API | Method | Path |
|------------|--------|------|
| Save secret | POST | `/api/v1/vault/{userId}/secrets` |

Request body:

| Field | Type | Description |
|-------|------|-------------|
| `key_name` | `string` | The `keyName` from the credential request. |
| `value` | `string` | The user-provided secret (plaintext). Memory encrypts it server-side with AES-256-GCM. |

Headers: `X-API-KEY` (Memory's internal vault key), `X-Trace-ID` (optional, for distributed tracing).

**Credential acknowledgment (IO → Orchestrator)**

After the secret is successfully stored in Memory, IO sends a **lightweight acknowledgment** back to the Orchestrator (over the same transport as status updates, or a dedicated endpoint). This message contains **no secret material**.

| Field | Type | Description |
|-------|------|-------------|
| `taskId` | `string` | Task this credential belongs to. |
| `requestId` | `string` | Correlates with the original request. |
| `keyName` | `string` | The vault key name where the secret is now stored. |
| `status` | `'stored' \| 'error'` | Whether the secret was successfully stored. |
| `error` | `string` | (Optional) Error message if `status` is `'error'`. |

The Orchestrator (or agent) can then retrieve the secret from Memory's vault API using `GET /api/v1/vault/{userId}/secrets?key_name={keyName}` when it needs to use the credential.

**Security guarantees**

- The credential **never** appears in chat message state, conversation history, or streamed replies.
- The credential **never** flows through the Orchestrator — it goes directly from IO to Memory's encrypted vault.
- The credential **never** enters the chat logging pipeline. The activity log records only "Credential submitted through secure channel (content not logged)".
- Memory stores the secret encrypted (AES-256-GCM); only authorized services with the vault API key can decrypt.
- The IO surfaces the credential entry in a **dedicated modal overlay** with a separate DOM tree from the chat window.
- The modal supports masked input (`type="password"`) with a hold-to-reveal control.
- After successful submission, the chat displays a system event ("Credential provided securely via isolated channel") with an `isRedacted` flag — no actual content.

**Delivery**: The credential request may arrive as a structured event on the same transport as status updates (WebSocket, SSE), or as a field in the streamed reply metadata. The IO component watches for this event and surfaces the modal.

---

## 2. Memory

### 2.1 Responsibility

- **IO (or Orchestrator)** sends log entries to the memory component when:
  - The user sends a message (log user message).
  - The orchestrator finishes a reply (log full assistant message).
- **Memory** stores entries (and may expose a read API for replay, analytics, or debugging).

### 2.2 Log entry format

Each log entry should include at least:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `taskId` | `string` | Yes | Task this message belongs to. |
| `role` | `'user' \| 'orchestrator'` | Yes | Who produced the content (`orchestrator` = assistant/agent). |
| `content` | `string` | Yes | Full message text. |
| `at` | `string` (ISO 8601) | Yes | Timestamp when the event occurred (e.g. when the message was sent or when the reply was completed). |

### 2.3 API (recommended)

- **Append**
  - `logUserResponse(taskId: string, content: string): void`  
    Call when the user sends a message. Memory assigns `role: 'user'` and `at: now`.
  - `logOrchestratorResponse(taskId: string, content: string): void`  
    Call when the orchestrator reply is complete (full content). Memory assigns `role: 'orchestrator'` and `at: now`.

- **Read (optional)**  
  - `getMemoryLog(): readonly LogEntry[]`  
    Returns all log entries (e.g. for debugging or export). Entries may be ordered by `at` or insertion order.

### 2.4 Persistence

- For production, the memory component should **persist** entries (e.g. to disk or a store). The demo may use an in-memory buffer only.
- Retention and indexing (e.g. by `taskId`, time range) are left to the memory component design.

---

## 3. Summary

| Interface | Direction | Purpose |
|-----------|-----------|---------|
| **Status updates** | Orchestrator → IO | Semantic heartbeat per task: status, lastUpdate, expectedNextInputMinutes (scalar, minutes from now); 1–4 s per task, push or poll. |
| **Chat (send)** | IO → Orchestrator | User message + optional history; taskId required. |
| **Chat (stream)** | Orchestrator → IO | Streamed assistant reply (chunks); IO accumulates and displays. |
| **Credential request** | Orchestrator → IO | Request a secret from the user; includes `userId` and `keyName` for vault storage. Triggers a dedicated modal outside the chat DOM. |
| **Credential store** | IO → Memory (Vault) | Secret written directly to Memory's encrypted vault via `POST /api/v1/vault/{userId}/secrets`. Orchestrator never sees plaintext. |
| **Credential ack** | IO → Orchestrator | Lightweight acknowledgment (`requestId`, `keyName`, `status`) after secret is stored. No secret material. |
| **Logging** | IO/Orchestrator → Memory | Append user and orchestrator messages with taskId, role, content, timestamp. Credentials are never logged. |

All identifiers (`taskId`, `userId`) must be consistent across status, chat, credential, and logging interfaces so that a task's conversation, credentials, and status can be correlated in the IO UI and in memory.
