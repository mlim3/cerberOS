# cerberOS Logging Standard

All runtime components emit **structured JSON logs**, one object per line, using
the same schema. Logs go to stdout except for `agents-component/cmd/agent-process`,
which writes logs to stderr because stdout carries the JSON task protocol.

## Components

cerberOS has exactly **six** components, named after the top-level folders that
own them. The `component` field on every log line is one of these values, and
nothing else:

| `component` | Folder | Description |
|-------------|--------|-------------|
| `agents` | `agents-component/` | Agent supervisor and `agent-process` workers |
| `orchestrator` | `orchestrator/` | Task router, planner, monitor, recovery |
| `io` | `io/` | API gateway, core engine, web/cli surfaces |
| `memory` | `memory/` | Memory store and retrieval service |
| `vault` | `vault/` | Credential vault and OpenBao broker |
| `databus` | `aegis-databus/` | NATS JetStream message bus |

A binary always identifies itself with the component that owns it. Sub-units
inside a component (server, http, dispatcher, outbox-relay, surface adapter,
heartbeat emitter, etc.) go in the **`module`** field, never in `component`.

There is **no** `service` field. The previous `service` field has been removed
because it duplicated `component` and produced confusing labels in Loki.

## Canonical Schema

Every log line uses this field order:

1. Required base fields: `time`, `level`, `msg`, `component`, `module`
2. Correlation fields: `task_id`, `conversation_id`, `trace_id`, `request_id`, `message_id` when applicable
3. Safe component-specific fields

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `time` | string | yes | RFC3339Nano or ISO-8601 UTC timestamp |
| `level` | string | yes | `DEBUG` `INFO` `WARN` `ERROR` |
| `msg` | string | yes | Short event name or sentence; no trailing period |
| `component` | string | yes | One of the six components above |
| `module` | string | yes | Sub-unit within the component (e.g. `server`, `dispatcher`, `http`) |
| `task_id` | string | task logs | Required while handling or routing a task |
| `conversation_id` | string | conversation logs | Stable thread ID; required when a log line pertains to a chat conversation (chat task, snapshot, message), absent otherwise |
| `trace_id` | string | traced logs | W3C 32-hex trace ID; required when available in context |
| `request_id` | string | request logs | Vault execute, HTTP, NATS, or component request ID |
| `message_id` | string | message logs | NATS/envelope message ID when available |

Additional key-value pairs append after the canonical fields. They must be safe
metadata, never raw payloads, raw user input, credentials, permission tokens, or
operation results. The one allowed exception is the bounded `content_preview`
described in the next section.

### Writing meaningful `msg` values

`msg` is the line a human reads first when scanning logs. Treat it like a tiny
sentence in plain English describing what just happened, not the route that was
hit or an internal enum token. The structured fields under it carry the IDs and
metadata; `msg` carries the story.

Good:

- `received chat message from user`
- `forwarded chat message to orchestrator`
- `subtask completed by agent`
- `dropped planner response for already-completed task`
- `agent-process finished task (success)`

Avoid:

- `POST /api/chat` — that's the route, not the action
- `task_accepted_sent_early` — enum token; use `sent early task acknowledgment to io`
- `received task result from agents` — say *which* agent and whether it succeeded
- `subtask completed` — say *who* completed and add `subtask_id` as a structured
  field on the same line

When an outcome is binary (success/failure, accepted/rejected), put it in the
`msg` so humans don't have to scan attributes to find it. Add the same
information as a structured boolean too (e.g. `success: false`) for LogQL
filters.

### Allowed previews vs. forbidden raw content

User text and model output carry value for debugging conversations, but the raw
payload is sensitive. The split is:

**Forbidden — never log:**

- `payload.raw_input` (the full user message field carried into the agent)
- credential values, permission tokens, OAuth/API keys
- full task result payloads or streamed model output
- full planner output

**Allowed — bounded previews only:**

| Field | Source | Cap | Helper |
|-------|--------|-----|--------|
| `content_preview` | user-typed chat message | head + tail: first 15 words, last 10 words; middle replaced by `[..N chars..]` indicating omitted character count | `previewHeadTail` (TS), `PreviewHeadTail` (Go) |
| `result_preview` | agent final result string (or relayed agent text) | head + tail (same as above) | `previewHeadTail` / `PreviewHeadTail` |
| `error_message_preview` | agent / system failure message text | head + tail (same as above) | `previewHeadTail` / `PreviewHeadTail` |
| `detail_preview` | orchestrator `error_response` user_message | head + tail (same as above) | `previewHeadTail` / `PreviewHeadTail` |
| `label_preview`, `description_preview` | credential prompt label / description shown to the user | first 20 words or 140 characters; suffixed with `…` when truncated | `previewWords` / `PreviewWords` |
| `title_preview`, `reason_preview`, `transcript_preview`, `message_preview` | short metadata strings (conversation title, plan rejection reason, voice transcript, vault progress message) | first 20 words or 140 characters; suffixed with `…` | `previewWords` / `PreviewWords` |
| `raw_result_sample` | planner output that *failed to parse* (debug aid for malformed plans) | first 1024 bytes; suffixed with `…[truncated]`; emitted only when plan-parse failed | inline cap |

Two preview shapes:

1. **Word truncation** (`previewWords` / `PreviewWords`) — caps at *N* words OR
   *M* characters and appends `…` when truncated. Use for short metadata strings
   where the whole point is the start of the value (titles, reasons, error
   codes, progress messages).

2. **Head + tail** (`previewHeadTail` / `PreviewHeadTail`) — keeps the first
   ~15 words and the last ~10 words and replaces the middle with
   `[..1234 chars..]`, where the integer is the count of characters omitted.
   Use for **conversation messages**: long chat messages and long agent
   replies. The motivation is debugger UX — when a user pastes a 500-word
   document and asks a question at the end, the question matters as much as
   the start, and a beginning-only preview hides it. Head + tail also makes
   the same message recognisable in IO, orchestrator, and agent logs at a
   glance.

The cap is the safety mechanism. The same field without a cap is a raw-input
log and is forbidden. Use the shared helpers — never roll your own slice.
Never include any preview field in error messages returned to clients; they
are log-only.

### task_id vs conversation_id

A `conversation_id` is the long-lived ID of a chat thread; a `task_id` is one
agent run inside that thread. One conversation can produce many tasks. Both
are correlation keys, but they answer different debugging questions:

- `task_id` — "what happened during this single run?"
- `conversation_id` — "what happened across every run in this thread?"

Within a single run, `trace_id` already covers end-to-end stitching across all
components, so for one-task debugging `trace_id` is sufficient and `conversation_id`
is redundant. `conversation_id` becomes the load-bearing key when a debugger
needs to widen across multiple tasks in the same thread.

### Why `vault` and `databus` logs do not carry `conversation_id`

`vault` and `databus` only see envelope-level metadata — they never crack open
the user-task payload — so neither receives `conversation_id` on the wire today.
Their logs include `trace_id`, `task_id`, `request_id`, and `message_id` as
applicable, but not `conversation_id`. This is a deliberate scope choice, not
an oversight.

The mapping is recoverable from persistent state: `chat_schema.tasks` in the
Memory component records `(task_id, conversation_id, trace_id)` on the same row,
so a debugger can resolve `trace_id → conversation_id` with a single query and
then widen the search across the rest of the components. When that two-step
becomes a recurring pain point, `conversation_id` can be added to the
`CloudEvent` envelope and the vault/credential request payloads — at which
point this section should be removed.

## Module Names

Modules are free-form within a component but should be stable, descriptive, and
lowercase-with-dashes. Common modules per component:

| Component | Typical modules |
|-----------|-----------------|
| `agents` | `aegis-agents`, `agent-process`, `heartbeat`, `simulator` |
| `orchestrator` | `main`, `server`, `dispatcher`, `task_dispatcher`, `task_monitor`, `plan_executor`, `comms_gateway`, `recovery`, `heartbeat_emitter`, `heartbeat_sweeper`, `io-client` |
| `io` | `server`, `http`, `nats`, `transcription`, `voice`, `credential`, `trace`, `heartbeat`, `memory-client`, `orchestrator-proxy`, `web-surface-adapter`, `web-surface-factory`, `cli`, `cli-surface-adapter`, `cli-session-store` |
| `memory` | `server`, `heartbeat` |
| `vault` | `server`, `heartbeat` |
| `databus` | `server`, `outbox-relay`, `dlq-replay`, `health-heartbeat`, `jetstream-metrics`, `orchestrator-stub`, `memory-stub`, `stubs`, `demo` |

### Naming rule: never reuse a component name as a module

A `module` value MUST NOT equal any of the six component names. A log line
that says `component=io, module=orchestrator` is confusing because readers
cannot tell whether the entry belongs to the `io` component or to the
`orchestrator` component.

If a sub-unit relates to another component (for example, the layer in `io`
that proxies to the orchestrator), encode the relationship in the module
name: `orchestrator-proxy`, `orchestrator-stub`, `orchestrator-client`,
`io-client`, `memory-client`, etc. Component names may appear *inside* a
compound module name, but never on their own.

When a binary embeds another component's identity (for example, the databus
demo simulates an orchestrator stub), the `component` is still the host
binary's component (`databus`) and the simulation goes in `module`
(`orchestrator-stub`, never bare `orchestrator`).

## Level Semantics

| Level | When to use |
|-------|-------------|
| `DEBUG` | Verbose diagnostics; disabled in production |
| `INFO` | Normal lifecycle events (start, connect, ready, shutdown) |
| `WARN` | Degraded but continuing; no operator action required |
| `ERROR` | Operation failed; service continues |

Only `DEBUG`, `INFO`, `WARN`, and `ERROR` are emitted. This matches Go
`log/slog` and keeps the format portable across Go and TypeScript components.

Unrecoverable failures are logged at `ERROR` with safe context such as
`exit_code`, `reason`, or `operation`, then the process exits.

Input/migration aliases:

| Input | Emitted |
|-------|---------|
| `FATAL` | `ERROR` |
| `CRITICAL` | `ERROR` |
| `WARNING` | `WARN` |

## Coverage Requirements

Each runtime component should log these lifecycle events in the canonical JSON
format:

- Startup/configuration loaded
- External connection state (NATS, database, OpenBao, HTTP listener)
- Ready/healthy state
- Shutdown
- Error paths

Task-handling logs must include `task_id`. If trace context is available, include
`trace_id` on the same log line. Logs that pertain to a chat conversation
(chat tasks, conversation snapshots, message persistence) must additionally
include `conversation_id` whenever the value is in scope.

## Out of Scope

The logging standard does not apply to:

- Intentional CLI or demo stdout, such as `vault inject` output
- Test-only loggers in `*_test.go`
- Prometheus text endpoints
- JSON HTTP response bodies
- Vault audit records, which are a separate channel and record key names only

## Loki and Grafana

Promtail parses the canonical JSON fields before pushing logs to Loki. The
bounded-cardinality fields `component`, `module`, and `level` are promoted to
Loki labels so Grafana can filter component logs efficiently.

High-cardinality correlation fields such as `task_id`, `conversation_id`,
`trace_id`, `request_id`, and `message_id` remain in the JSON log body. Query
them with LogQL JSON parsing:

```logql
{job="docker", component="orchestrator"} | json | task_id="task-123"
{job="docker", component=~"orchestrator|agents|io|memory"} | json | conversation_id="conv-abc"
```

Use the bundled Grafana dashboard `CerberOS — container logs` to view all
component logs:

```logql
{job="docker", component=~".+"}
```

To narrow to a single sub-unit:

```logql
{job="docker", component="memory", module="server"}
```

## Go Usage

```go
// Initialise once in main():
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
    With("component", "memory", "module", "server")

// Normal log
logger.Info("server started", "addr", ":8080")

// Unrecoverable failure (log + exit)
logger.Error("config load failed", "error", err, "exit_code", 1)
os.Exit(1)

// With task and trace IDs
logger.Info("request received", "task_id", taskID, "trace_id", traceID, "method", r.Method)
```

For the orchestrator, use the helpers in `orchestrator/internal/observability`:

```go
log := observability.LoggerWithModule("dispatcher")
log.Info("task dispatched", "task_id", taskID)

// or pull all IDs from context:
observability.LogFromContext(ctx).Info("task dispatched")
```

## TypeScript Usage (io/* services)

```typescript
import { ioLog, logFromContext } from './logger'

// Without request context (component is always "io"; second arg is the module)
ioLog('info', 'transcription', 'model ready')
ioLog('error', 'transcription', 'warmup failed', { error: String(err) })

// With Hono request context (sets trace_id and task_id automatically)
logFromContext(c, 'info', 'nats', 'message published', { subject })
```

## Example Log Lines

```json
{"time":"2026-04-17T10:00:00.000Z","level":"INFO","msg":"vault listening","component":"vault","module":"server","addr":":8000"}
{"time":"2026-04-17T10:00:01.123Z","level":"INFO","msg":"streams ready","component":"databus","module":"server","startup_s":1.123}
{"time":"2026-04-17T10:00:02.456Z","level":"WARN","msg":"compaction failed, continuing","component":"agents","module":"agent-process","task_id":"abc","trace_id":"def","error":"context deadline exceeded"}
{"time":"2026-04-17T10:00:03.789Z","level":"ERROR","msg":"database ping failed","component":"memory","module":"server","error":"connection refused"}
{"time":"2026-04-17T10:00:04.000Z","level":"INFO","msg":"task dispatched","component":"orchestrator","module":"dispatcher","trace_id":"xyz","task_id":"123","conversation_id":"conv-abc"}
{"time":"2026-04-17T10:00:04.500Z","level":"INFO","msg":"received chat message from user; queuing for orchestrator","component":"io","module":"http","trace_id":"xyz","task_id":"123","conversation_id":"conv-abc","content_preview":"can you summarise the latest design doc for me and tell me which sections have changed [..2147 chars..] thanks — keep it under 500 words please"}
{"time":"2026-04-17T10:00:05.111Z","level":"ERROR","msg":"config load failed","component":"io","module":"server","exit_code":1,"error":"missing NATS_URL"}
```
