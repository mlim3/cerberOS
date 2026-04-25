# cerberOS Logging Standard

All runtime components emit **structured JSON logs**, one object per line, using
the same schema. Logs go to stdout except for `agents-component/cmd/agent-process`,
which writes logs to stderr because stdout carries the JSON task protocol.

## Canonical Schema

Every log line uses this field order:

1. Required base fields: `time`, `level`, `msg`, `service`, `component`
2. Correlation fields: `task_id`, `trace_id`, `request_id`, `message_id` when applicable
3. Safe component-specific fields

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `time` | string | yes | RFC3339Nano or ISO-8601 UTC timestamp |
| `level` | string | yes | `DEBUG` `INFO` `WARN` `ERROR` |
| `msg` | string | yes | Short event name or sentence; no trailing period |
| `service` | string | yes | Fixed per binary; see table below |
| `component` | string | yes | Sub-package or logical unit within the service |
| `task_id` | string | task logs | Required while handling or routing a task |
| `trace_id` | string | traced logs | W3C 32-hex trace ID; required when available in context |
| `request_id` | string | request logs | Vault execute, HTTP, NATS, or component request ID |
| `message_id` | string | message logs | NATS/envelope message ID when available |

Additional key-value pairs append after the canonical fields. They must be safe
metadata, never raw payloads, raw user input, credentials, permission tokens, or
operation results.

## Service Names

| Binary | `service` value |
|--------|----------------|
| `vault/engine` | `vault` |
| `aegis-databus` | `databus` |
| `agents-component/cmd/aegis-agents` | `agents` |
| `agents-component/cmd/agent-process` | `agents` |
| `memory` | `memory` |
| `orchestrator` | `orchestrator` |
| `io/api` | `io-api` |
| `io/core` | `io-core` |
| `io/surfaces/web` | `io-web` |
| `io/surfaces/cli` | `io-cli` |

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
`trace_id` on the same log line.

## Out of Scope

The logging standard does not apply to:

- Intentional CLI or demo stdout, such as `vault inject` output
- Test-only loggers in `*_test.go`
- Prometheus text endpoints
- JSON HTTP response bodies
- Vault audit records, which are a separate channel and record key names only

## Loki and Grafana

Promtail parses the canonical JSON fields before pushing logs to Loki. The
bounded-cardinality fields `service`, `component`, and `level` are promoted to
Loki labels so Grafana can filter component logs efficiently.

High-cardinality correlation fields such as `task_id`, `trace_id`, `request_id`,
and `message_id` remain in the JSON log body. Query them with LogQL JSON parsing:

```logql
{job="docker", service="orchestrator"} | json | task_id="task-123"
```

Use the bundled Grafana dashboard `CerberOS — container logs` to view all
component logs:

```logql
{job="docker", service=~".+"}
```

## Go Usage

```go
// Initialise once in main():
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
    With("service", "myservice", "component", "server")

// Normal log
logger.Info("server started", "addr", ":8080")

// Fatal (log + exit)
logger.Error("config load failed", "error", err, "exit_code", 1)
os.Exit(1)

// With task and trace IDs
logger.Info("request received", "task_id", taskID, "trace_id", traceID, "method", r.Method)
```

## TypeScript Usage (io/* services)

```typescript
import { ioLog, logFromContext } from './logger'

// Without request context
ioLog('info', 'transcription', 'model ready')
ioLog('error', 'transcription', 'warmup failed', { error: String(err) })

// With Hono request context (sets trace_id automatically)
logFromContext(c, 'info', 'nats', 'message published', { task_id: taskId, subject })
```

## Example Log Lines

```json
{"time":"2026-04-17T10:00:00.000Z","level":"INFO","msg":"vault listening","service":"vault","component":"server","addr":":8000"}
{"time":"2026-04-17T10:00:01.123Z","level":"INFO","msg":"streams ready","service":"databus","component":"server","startup_s":1.123}
{"time":"2026-04-17T10:00:02.456Z","level":"WARN","msg":"compaction failed, continuing","service":"agents","component":"agent-process","task_id":"abc","trace_id":"def","error":"context deadline exceeded"}
{"time":"2026-04-17T10:00:03.789Z","level":"ERROR","msg":"database ping failed","service":"memory","component":"server","error":"connection refused"}
{"time":"2026-04-17T10:00:04.000Z","level":"INFO","msg":"task dispatched","service":"orchestrator","component":"dispatcher","trace_id":"xyz","task_id":"123"}
{"time":"2026-04-17T10:00:05.111Z","level":"ERROR","msg":"config load failed","service":"io-api","component":"server","exit_code":1,"error":"missing NATS_URL"}
```
