# aegis-agents

The **Agents Component** of Aegis OS — a distributed operating system purpose-built for running autonomous AI agents.

---

## Overview

`aegis-agents` manages the full agent lifecycle: receiving task specifications from the Orchestrator, provisioning the right agent for each task (reusing existing agents or spinning up new ones), and shepherding agents from spawn to termination.

This component is one of five in the Aegis OS platform. It does not own task routing, persistent storage, secrets, or message transport — those belong to adjacent components. It integrates with all four through well-defined contracts.

> For the full implementation briefing (module rules, type contracts, build order), see [CLAUDE.md](./CLAUDE.md).

---

## Quick Start (Docker)

The fastest way to bring up the Agents Component alongside its only external dependency (NATS JetStream):

```bash
# 1. Copy the env template and add your Anthropic API key
cp .env.example .env
# edit .env: set ANTHROPIC_API_KEY=sk-ant-...

# 2. Start everything
docker compose up
```

That's it. `docker compose up` will:
- Pull and start a NATS JetStream server
- Build the `aegis-agents` image (both binaries compiled from source)
- Start the Agents Component once NATS is healthy

The `/metrics` Prometheus endpoint is available at `http://localhost:9090/metrics`.

To stop and remove containers (stream state is preserved in the `nats-data` volume):
```bash
docker compose down
```

To also wipe stream state:
```bash
docker compose down -v
```

---

## Running Without Docker

### Prerequisites

- Go 1.24+
- NATS Server with JetStream enabled (for integration testing; not required for unit tests)
- An Anthropic API key (for running agent tasks)

### Build

```bash
go build ./...
```

### Run

```bash
# Start NATS (if you don't already have one running)
docker run --rm -p 4222:4222 nats:latest -js

# Start the Agents Component
AEGIS_NATS_URL=nats://localhost:4222 \
AEGIS_AGENT_PROCESS_PATH=./agent-process \
ANTHROPIC_API_KEY=<your-key> \
  go run ./cmd/aegis-agents/
```

Build the `agent-process` binary first if using `AEGIS_AGENT_PROCESS_PATH`:
```bash
go build -o agent-process ./cmd/agent-process/
```

When `AEGIS_AGENT_PROCESS_PATH` is unset, the component falls back to an in-process stub suitable for unit testing only.

### Run Tests

```bash
# Unit tests — no external dependencies required
go test -count=1 ./...

# Integration tests require NATS
docker run --rm -d -p 4222:4222 nats:latest -js
AEGIS_NATS_URL=nats://localhost:4222 go test -count=1 ./test/integration/...
```

---

## Configuration

All configuration is environment-based. The component starts only when `AEGIS_NATS_URL` is set; everything else has a default.

| Variable | Required | Default | Description |
|---|---|---|---|
| `AEGIS_NATS_URL` | **Yes** | — | NATS JetStream endpoint, e.g. `nats://localhost:4222` |
| `ANTHROPIC_API_KEY` | **Yes*** | — | Anthropic API key — injected into each agent process at spawn. *Required when running real agents; not needed for unit tests. |
| `AEGIS_AGENT_PROCESS_PATH` | No | *(in-process stub)* | Path to the compiled `agent-process` binary. When set, the Lifecycle Manager spawns real agent processes. |
| `AEGIS_COMPONENT_ID` | No | `aegis-agents` | Identity published in all outbound message envelopes |
| `AEGIS_HEARTBEAT_INTERVAL` | No | `5s` | How often each agent process sends a heartbeat |
| `AEGIS_HEARTBEAT_MAX_MISSED` | No | `3` | Consecutive missed heartbeats before declaring an agent crashed |
| `AEGIS_MAX_AGENT_RETRIES` | No | `3` | Max crash-recovery respawns before permanent termination |
| `AEGIS_METRICS_PORT` | No | `9090` | TCP port for the Prometheus `/metrics` endpoint |
| `AEGIS_CRED_AUTH_MAX_ATTEMPTS` | No | `3` | Credential authorize retries before `VAULT_UNREACHABLE` |
| `AEGIS_CRED_AUTH_TIMEOUT` | No | `5s` | Per-attempt deadline for `credential.response` |
| `AEGIS_CRED_AUTH_BASE_BACKOFF` | No | `1s` | Initial backoff between credential retries (doubles each attempt) |
| `AEGIS_COMMS_MAX_DELIVER` | No | `5` | JetStream redelivery budget before dead-lettering a message |
| `AEGIS_IDLE_SUSPEND_TIMEOUT` | No | `0` (disabled) | Time an agent may stay IDLE before being suspended to free VM resources. `0` disables auto-suspension. Example: `5m` |
| `AEGIS_SUSPEND_WAKE_LATENCY_TARGET` | No | `2s` | Informational SLA target for waking a SUSPENDED agent (logged at startup for Platform team visibility) |

No addresses are configured for OpenBao, the Memory Component, or any other peer. All cross-component communication routes through the Orchestrator via NATS.

---

## Invoking an Agent Directly

The `agent-process` binary can be exercised without a running NATS stack by piping a `SpawnContext` JSON to stdin. Useful for smoke-testing the ReAct loop in isolation.

```bash
# General reasoning (no external tools)
echo '{"task_id":"t1","skill_domain":"general","permission_token":"","instructions":"Give me the steps to make a sandwich.","trace_id":"tr1"}' \
  | ANTHROPIC_API_KEY=<your-key> go run ./cmd/agent-process/

# Web domain (uses web_fetch tool)
echo '{"task_id":"t2","skill_domain":"web","permission_token":"","instructions":"Fetch https://example.com and summarise the page.","trace_id":"tr2"}' \
  | ANTHROPIC_API_KEY=<your-key> go run ./cmd/agent-process/
```

Output streams:
- **stdout** — JSON-encoded `TaskOutput` (the final result)
- **stderr** — structured JSON logs from the ReAct loop (heartbeat warnings are expected when NATS env vars are absent)

---

## Sending a Task via NATS

With the component running (via Docker or directly), publish a `task.inbound` message to trigger agent provisioning:

```json
{
  "message_id": "msg-001",
  "message_type": "task.inbound",
  "source_component": "orchestrator",
  "correlation_id": "task-001",
  "timestamp": "2026-01-01T00:00:00Z",
  "schema_version": "1.0",
  "payload": {
    "task_id": "task-001",
    "required_skills": ["general"],
    "instructions": "Give me the steps to make a sandwich.",
    "trace_id": "trace-abc123",
    "user_context_id": "ctx-user-42"
  }
}
```

Publish to subject: `aegis.agents.task.inbound`

The component responds on:
- `aegis.orchestrator.task.accepted` — immediate acknowledgment, published before provisioning begins
- `aegis.orchestrator.task.result` — final result once the agent completes
- `aegis.orchestrator.task.failed` — if provisioning or execution fails

---

## Responsibilities

- **Agent Provisioning** — Spawn new agents when no capable agent exists; reuse IDLE or SUSPENDED agents when one matches
- **Agent Registry** — Maintain a catalog of all agents: capabilities, states, and authorized operation types
- **Skill Management** — Serve agent skills via a progressive disclosure hierarchy (domain → command → parameter spec)
- **Operation Brokering** — When a skill requires a credentialed external call, dispatch a typed operation to the Vault via the Orchestrator. The Vault executes and returns only the result — credentials never leave the Vault and are never held by this component
- **Lifecycle Management** — Health monitoring, crash recovery, graceful shutdown, and VM teardown
- **State Persistence** — Delegate all persistence to the Memory Component via a disciplined interface

---

## Architecture

All external communication flows through a single gateway (`internal/comms`). No module reaches out to an external component directly.

```
                ┌─────────────────────────────────────────┐
                │           Aegis OS Orchestrator          │
                └────────────────┬────────────────────────┘
                                 │ NATS JetStream
                ┌────────────────▼────────────────────────┐
                │        Communications Component          │
                └────────────────┬────────────────────────┘
                                 │
                ┌────────────────▼────────────────────────┐
                │             Agents Component             │
                │  ┌──────────┐  ┌──────────┐            │
                │  │ Factory  │  │ Registry │            │
                │  └──────────┘  └──────────┘            │
                │  ┌──────────┐  ┌──────────┐            │
                │  │  Skills  │  │Lifecycle │            │
                │  └──────────┘  └──────────┘            │
                │  ┌──────────┐  ┌──────────┐            │
                │  │   Op     │  │  Memory  │            │
                │  │  Broker  │  │Interface │            │
                │  └──────────┘  └──────────┘            │
                └─────────────────────────────────────────┘
```

| Module | Package | Role |
|--------|---------|------|
| M1 — Communications Interface | `internal/comms` | Single NATS gateway for all inter-component messaging |
| M2 — Agent Factory | `internal/factory` | Central coordinator for all agent provisioning and lifecycle transitions |
| M3 — Agent Registry | `internal/registry` | In-memory catalog; state persisted via M7 → Orchestrator → Memory Component |
| M4 — Skill Hierarchy Manager | `internal/skills` | Three-level skill tree with on-demand discovery |
| M5 — Operation Broker | `internal/credentials` | Formats `vault.execute.request` payloads; routes to Orchestrator. The Vault executes and returns `vault.execute.result`. No credential tokens handled here. |
| M6 — Lifecycle Manager | `internal/lifecycle` | Firecracker microVM spawn, monitoring, teardown |
| M7 — Memory Interface | `internal/memory` | Formats `state.write` / `state.read.request` payloads; routes to Orchestrator via Comms. Does not call any storage API directly. |

---

## Security Model

Two invariants are non-negotiable:

**1. All communications route through the Orchestrator.**
The Agents Component has no direct connection to any other Aegis OS component — not Memory, not the Credential Vault, not User I/O. Every outbound message travels:
```
Agents → Communications Component → Orchestrator
```

**2. Credentialed operations are executed by the Vault.**
Agents never receive raw credential values. When a skill requires calling an external service with a credential, the agent sends the operation (endpoint, method, parameters, and a logical credential reference) to the Vault via the Orchestrator. The Vault fetches the credential internally, executes the call, and returns the result. The credential never leaves the Vault.

```
Agent → Op Broker → Comms → Orchestrator → Vault → external service
                                                ↓
                                        result returned to agent
```

---

## Key Design Decisions

**Progressive Skill Disclosure** — Agents do not receive their full skill set at spawn. Skills are served on demand as agents drill down the hierarchy. This prevents context rot and keeps agent context focused on the active task.

**Vault-Delegated Execution** — Agents never hold credential tokens. When a skill requires a credentialed external call, the agent submits a typed `vault.execute.request` to the Vault (routed via the Orchestrator). The Vault executes the operation and returns only the result. Credentials never leave the Vault.

**Stateless by Design** — This component owns no persistent storage. All state is delegated to the Memory Component. Enables clean crash recovery and horizontal scaling.

**Single Comms Gateway** — All inter-component messaging flows through `internal/comms`. No module bypasses it. Simplifies auditing, retry logic, and integration testing.

**MicroVM Isolation** — Every agent runs in its own Firecracker microVM. A compromised agent cannot reach another agent or the host. In Docker/process-manager mode, each agent runs as an isolated child process.

**No Agent Framework Layer** — The Anthropic Go SDK is used directly. No LangChain or other agent SDK is layered on top (ADR-003).

---

## Task Flow

```
Orchestrator
    │
    │  Envelope{ payload: TaskSpec }
    ▼
comms.Subscribe("task.inbound")           ← M1: Communications Interface
    │
    │  Unmarshal Envelope → TaskSpec
    ▼
factory.HandleTaskSpec(spec)              ← M2: Agent Factory
    │
    ├─► registry.FindBySkills(domains)    ← M3: Registry
    │       │
    │       ├─ [IDLE agent]      ──────► assignTask (instant reuse)
    │       ├─ [SUSPENDED agent] ──────► wakeAgent (re-auth + new VM)
    │       └─ [no match]        ──────► provision new agent:
    │               1. skills.GetDomain(domain)              ← M4: Skills
    │               2. credentials.PreAuthorize(agentID)     ← M5: Op Broker
    │               3. lifecycle.Spawn(vmConfig)             ← M6: Lifecycle
    │               4. registry.Register(agentRecord)        ← M3: Registry
    │
    │  Agent executes task (ReAct loop; skill discovery on demand)
    │  Credentialed calls → credentials.Dispatch(opReq)     ← M5: Op Broker
    │                     → comms.Publish("vault.execute.request")
    │                     ← vault.execute.result returned to agent
    │
    ▼
factory.CompleteTask(agentID, output)
    │
    ├─► memory.Write(taggedResult)        ← M7: Memory Interface
    ├─► comms.Publish("task.result")      ← M1: back to Orchestrator
    ├─► credentials.Revoke(agentID)       ← M5: Op Broker
    ├─► lifecycle.Terminate(agentID)      ← M6: teardown microVM
    └─► registry.UpdateState("idle")      ← M3: mark agent available
         └─ [if IdleSuspendTimeout > 0] → sweep will transition to SUSPENDED
```

---

## Project Structure

```
aegis-agents/
├── CLAUDE.md                  # AI development briefing (read before coding)
├── README.md                  # This file
├── Dockerfile                 # Multi-stage build: compiles both binaries, alpine runtime
├── docker-compose.yml         # Brings up NATS + aegis-agents with one command
├── .env.example               # Environment variable template
├── go.mod
├── go.sum
├── cmd/
│   ├── aegis-agents/
│   │   └── main.go            # Component entry point — starts Comms, wires all modules
│   └── agent-process/
│       └── main.go            # Agent binary — runs inside each microVM; owns the ReAct loop
├── internal/
│   ├── comms/                 # M1: Communications Interface
│   ├── factory/               # M2: Agent Factory
│   ├── registry/              # M3: Agent Registry
│   ├── skills/                # M4: Skill Hierarchy Manager
│   ├── credentials/           # M5: Operation Broker
│   ├── lifecycle/             # M6: Lifecycle Manager
│   └── memory/                # M7: Memory Interface
├── pkg/
│   └── types/                 # Shared types (TaskSpec, AgentRecord, VaultOperationRequest, etc.)
├── config/
│   └── config.go              # Environment-based config
├── test/
│   └── integration/           # Integration tests (require NATS)
└── docs/
    └── *.pdf                  # EDD, PRD, CIC, ADR
```

---

## External Interface Summary

The Agents Component communicates with **exactly one external partner**: the Orchestrator, via NATS JetStream.

| Need | Outbound subject | Inbound subject |
|------|-----------------|-----------------|
| Task assignment | — | `aegis.agents.task.inbound` |
| Task result | `aegis.orchestrator.task.result` | — |
| Task failure | `aegis.orchestrator.task.failed` | — |
| Capability query | `aegis.orchestrator.capability.response` | `aegis.agents.capability.query` |
| Agent status | `aegis.orchestrator.agent.status` | — |
| Credential authorize/revoke | `aegis.orchestrator.credential.request` | `aegis.agents.credential.response` |
| Vault execute | `aegis.orchestrator.vault.execute.request` | `aegis.agents.vault.execute.result` |
| Memory write | `aegis.orchestrator.state.write` | `aegis.agents.state.write.ack` |
| Memory read | `aegis.orchestrator.state.read.request` | `aegis.agents.state.read.response` |

> **Authorization Rule:** The Agents Component is not authorized to communicate with Memory, Credential Vault, User I/O, or any other Aegis OS component except through the Orchestrator. This is a security and architectural boundary — not a convention.

---

## Documentation

Full design documentation lives in `/docs/`:

- **EDD** — Engineering Design Document covering all module specs, data flows, and interface contracts
- **ADR-003** — Anthropic Go SDK direct usage (no agent framework layer)
- **ADR-004** — Vault-executed operations model (no credential delivery to agents)

---

## Contributing

Before contributing, read `CLAUDE.md` for architectural constraints and `docs/EDD.pdf` for the full design spec. All PRs must maintain the module boundaries defined in the EDD.

---

## License

See [LICENSE](LICENSE).
