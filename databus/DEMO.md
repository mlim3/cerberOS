# Aegis DataBus EDD Demo

## Quick Start

```bash
# 1. Start NATS cluster
make up

# 2. Run Data Bus (streams + heartbeat)
./bin/aegis-databus &

# 3. Run 6-component demo
./bin/aegis-demo
```

Or use `make demo-full` to run the orchestrated script.

## Live Monitoring

- **Grafana**: http://localhost:3000 (admin/admin) → Aegis DataBus - NATS
- **NATS**: http://localhost:8222/connz, http://localhost:8222/jsz
- See [MONITORING.md](MONITORING.md) for full instructions.

## Components (6)

| Component | Role | Publishes | Subscribes |
|-----------|------|-----------|------------|
| **I/O** | UI Layer | `aegis.ui.action` | `aegis.tasks.>` |
| **Orchestrator** | Task Router, Planner, Agent Manager | `aegis.tasks.routed`, `plan_created`, `aegis.agents.created` | `aegis.ui.action`, `aegis.tasks.routed`, `plan_created` |
| **Memory** | Memory & Context Manager | `aegis.memory.saved` | `aegis.agents.created` |
| **Vault** | Permission Manager | — | `aegis.vault.>` |
| **Agent** | Agent runtime | `aegis.runtime.completed` | `aegis.agents.created` |
| **Monitoring** | Observability | — | All 7 stream subjects |

## Demo Guide

See [docs/DEMO_GUIDE.md](docs/DEMO_GUIDE.md) for step-by-step walkthroughs of each requirement, including **Zero Trust (NKey)** and **Failure Recovery**.

## EDD Requirements Demonstrated

| ID | Requirement | Demo |
|----|-------------|------|
| FR-DB-001 | Publish-Subscribe | I/O → Orchestrator → Memory → Agent flow |
| FR-DB-002 | Request-Reply | `bus.Request` / `bus.SubscribeRequestReply` for personalization |
| FR-DB-004 | Queue Groups | Orchestrator uses `QueueSubscribe(..., "agent-managers")` |
| FR-DB-005 | Wildcard routing | Monitoring subscribes to `aegis.tasks.>`, `aegis.agents.>`, etc. |
| FR-DB-008 | Event replay | `bus.ReplayLastN` (see `tests/replay_test.go`) |
| FR-DB-009 | Ack & DLQ | `tests/harness_test.go` TC005 |
| FR-DB-010 | Schema validation | `envelope.Validate` rejects malformed CloudEvents |
| FR-DB-011 | Outbox pattern | `internal/relay/outbox.go` + TC004 |

## Test Cases (EDD 9.3)

```bash
make test
```

- TC-001: Pub/sub latency < 5ms
- TC-002: Queue group (3 subs, each msg to 1)
- TC-003: Durable consumer recovery
- TC-004: Outbox relay replay
- TC-005: DLQ after 5 attempts
