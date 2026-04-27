# Heartbeat — service liveness on a cron

This document describes cerberOS' service-level heartbeat system.

## Why

Docker's `healthcheck:` only answers the question "is this container up?"
at the compose level. It does not tell us whether the process inside
is actually serving work, whether it can reach NATS, or whether it has
silently wedged. We want a centralized, auditable "who is alive right
now" signal that works the same way for every component — including
ones running in Firecracker microVMs that Docker cannot observe.

## Design

Every service publishes a small JSON **Beat** on a recurring 10-second
timer. The orchestrator subscribes to the shared wildcard subject,
maintains an in-memory `last_seen_at` map keyed by
`(service, instance_id)`, and runs a **sweeper** on a 30-second cron-style
interval. Any entry whose `last_seen_at` is older than 45 seconds is
marked stale, logged as a warning, and (optionally) hooked for recovery.

```
┌──────────────┐                                    ┌─────────────────┐
│ orchestrator │  aegis.heartbeat.service.orch ───▶ │                 │
├──────────────┤                                    │   orchestrator  │
│   memory     │  aegis.heartbeat.service.memory ─▶ │   heartbeat     │
├──────────────┤                                    │   sweeper       │
│    vault     │  aegis.heartbeat.service.vault ──▶ │                 │
├──────────────┤                                    │  (scans every   │
│  aegis-      │  aegis.heartbeat.service.databus ▶ │   30s, marks    │
│  databus     │                                    │   stale > 45s)  │
├──────────────┤                                    │                 │
│  agents-     │  aegis.heartbeat.service.agents ─▶ │   GET           │
│  component   │                                    │   /heartbeats   │
├──────────────┤                                    │                 │
│   io-api     │  aegis.heartbeat.service.io ─────▶ │                 │
└──────────────┘                                    └─────────────────┘
```

## Subject scheme

| Subject                              | Producer             | Consumer                          |
| ------------------------------------ | -------------------- | --------------------------------- |
| `aegis.heartbeat.<agent_id>`         | individual agent PID | agents-component crash detector   |
| `aegis.heartbeat.service.<service>`  | each long-lived svc  | orchestrator sweeper              |

The two schemes never overlap. NATS `*` matches exactly one token, so
`aegis.heartbeat.*` (subscribed by the agents-component crash
detector) does **not** match `aegis.heartbeat.service.foo`, and
`aegis.heartbeat.service.*` (subscribed by the orchestrator sweeper)
does not match `aegis.heartbeat.<agent_id>`.

## Wire format

Raw JSON, no envelope:

```json
{
  "service":     "memory",
  "instance_id": "memory-pod-7f9c-123",
  "status":      "ok",
  "timestamp":   "2026-04-21T17:42:03Z",
  "pid":         42,
  "hostname":    "pod-7f9c",
  "uptime_s":    184
}
```

Beats are published on core NATS (not JetStream). They are
at-most-once by design — a dropped beat just delays the "stale"
verdict by one sweep cycle.

## Cadence

| Knob              | Default | Where                                          |
| ----------------- | ------- | ---------------------------------------------- |
| Emit interval     | 10 s    | `heartbeat.DefaultInterval` in each emitter    |
| Sweep interval    | 30 s    | `heartbeat.DefaultSweepInterval` (orchestrator) |
| Stale threshold   | 45 s    | `heartbeat.DefaultStaleAfter` (orchestrator)   |

45 s ≈ 4 missed beats. A service that misses a single beat will never
be marked stale.

## Emitter integration

Each Go component has an `internal/heartbeat` (or `engine/heartbeat`)
package with the same shape:

```go
emitter := heartbeat.New(nc, "memory", logger)
go emitter.Start(ctx)
```

The IO API (TypeScript) uses `startHeartbeatEmitter(natsClient)` from
`io/api/src/heartbeat.ts`.

All emitters degrade gracefully when NATS is unavailable — they log a
warning and skip publishing rather than crashing the service.

## Consumer / sweeper (orchestrator)

`orchestrator/internal/heartbeat/sweeper.go` owns the state.

- Subscribes to `aegis.heartbeat.service.*` via `interfaces.NATSClient`.
- Stores the latest Beat per `(service, instance_id)` in memory.
- On each 30 s tick, marks entries older than 45 s as stale and
  emits a `heartbeat: service stale` warning log line. The first
  beat received after that transitions the instance back to alive
  and emits a `heartbeat: service recovered` info line.
- Optional `OnStale` callback lets the recovery manager react
  (not yet wired — future work).

## HTTP surface

Orchestrator exposes the current snapshot at `GET /heartbeats`:

```bash
curl -s http://orchestrator:8080/heartbeats | jq
```

Response shape:

```json
{
  "generated_at":   "2026-04-21T17:42:03Z",
  "sweep_interval": "30s",
  "stale_after":    "45s",
  "total":          6,
  "alive":          6,
  "stale":          0,
  "services": [
    {
      "service":     "agents",
      "instance_id": "agents-host-123",
      "hostname":    "host",
      "last_seen":   "2026-04-21T17:41:58Z",
      "age_seconds": 5.2,
      "status":      "ok",
      "health":      "alive",
      "uptime_s":    1843
    }
  ]
}
```

## Why raw JSON instead of the comms envelope

Services like `agents-component` normally publish through
`internal/comms` which wraps payloads in an `OutboundEnvelope`. For
heartbeats we deliberately **bypass the envelope** and open a
dedicated raw `*nats.Conn`. Rationale:

1. The sweeper parses beats with a plain `json.Unmarshal(&Beat{})` —
   it never needs to know about the comms protocol.
2. Bypassing the envelope means heartbeats do not hit JetStream and
   do not consume stream storage for messages that are explicitly
   at-most-once.
3. Keeps the wire shape consistent across Go and TypeScript
   emitters.

This is the same trade-off the existing `aegis-databus` health
heartbeat already makes.
