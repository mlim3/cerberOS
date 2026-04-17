# cerberOS Logging Standard

All services emit **structured JSON logs**, one object per line, to stdout (stderr for `agents/agent-process` only — its stdout carries the JSON task output).

## Canonical Fields

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `time` | string | ✅ | RFC3339Nano — e.g. `2026-04-17T10:00:00.000Z` |
| `level` | string | ✅ | `DEBUG` `INFO` `WARN` `ERROR` |
| `msg` | string | ✅ | Short imperative sentence; no trailing period |
| `service` | string | ✅ | Fixed per binary — see table below |
| `component` | string | ✅ | Sub-package or logical unit within the service |
| `trace_id` | string | ○ | W3C 32-hex trace ID; omit when not in a traced request |

Additional key-value pairs append after `component`.

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

## Level Semantics

| Level | When to use |
|-------|-------------|
| `DEBUG` | Verbose diagnostics; disabled in production |
| `INFO` | Normal lifecycle events (start, connect, ready, shutdown) |
| `WARN` | Degraded but continuing; no operator action required |
| `ERROR` | Operation failed; service continues |

**There is no `FATAL` or `CRITICAL` level.** For unrecoverable errors, log at `ERROR` then call `os.Exit(1)`. `CRITICAL` maps to `ERROR`. `WARNING` maps to `WARN`.

## Go Usage

```go
// Initialise once in main():
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
    With("service", "myservice", "component", "server")

// Normal log
logger.Info("server started", "addr", ":8080")

// Fatal (log + exit)
logger.Error("config load failed", "error", err)
os.Exit(1)

// With trace ID
logger.Info("request received", "trace_id", traceID, "method", r.Method)
```

## TypeScript Usage (io/* services)

```typescript
import { ioLog, logFromContext } from './logger'

// Without request context
ioLog('info', 'transcription', 'model ready')
ioLog('error', 'transcription', 'warmup failed', { error: String(err) })

// With Hono request context (sets trace_id automatically)
logFromContext(c, 'info', 'nats', 'message published', { subject })
```

## Example Log Lines

```json
{"time":"2026-04-17T10:00:00.000Z","level":"INFO","msg":"vault listening","service":"vault","component":"server","addr":":8000"}
{"time":"2026-04-17T10:00:01.123Z","level":"INFO","msg":"streams ready","service":"databus","component":"server","startup_s":1.123}
{"time":"2026-04-17T10:00:02.456Z","level":"WARN","msg":"compaction failed, continuing","service":"agents","component":"agent-process","task_id":"abc","trace_id":"def","error":"context deadline exceeded"}
{"time":"2026-04-17T10:00:03.789Z","level":"ERROR","msg":"database ping failed","service":"memory","component":"server","error":"connection refused"}
{"time":"2026-04-17T10:00:04.000Z","level":"INFO","msg":"task dispatched","service":"orchestrator","component":"dispatcher","trace_id":"xyz","task_id":"123"}
{"time":"2026-04-17T10:00:05.111Z","level":"INFO","msg":"model ready","service":"io-api","component":"transcription"}
```
