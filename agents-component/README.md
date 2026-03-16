# aegis-agents

The **Agents Component** of [Aegis OS](https://github.com/your-org/aegis-os) — a distributed operating system purpose-built for running autonomous AI agents.

---

## Overview

`aegis-agents` manages the full agent lifecycle: receiving task specifications from the Orchestrator, provisioning the right agent for each task (reusing existing agents or building new ones), and shepherding agents from spawn to termination.

This component is one of five in the Aegis OS platform. It does not own task routing, persistent storage, secrets, or message transport — those belong to adjacent components. It integrates with all four through well-defined contracts.

> For the full implementation briefing (module rules, type contracts, build order), see [CLAUDE.md](./CLAUDE.md).

---

## Responsibilities

- **Agent Provisioning** — Spawn new agents inside Firecracker microVMs when no capable agent exists for a task
- **Agent Registry** — Maintain a catalog of all agents, their capabilities, states, and authorized operation types
- **Skill Management** — Serve agent skills via a progressive disclosure hierarchy (domain → command → parameter spec)
- **Operation Brokering** — When a skill requires a credentialed external call, dispatch a typed operation to the Vault via the Orchestrator. The Vault executes the operation and returns only the result — credentials never leave the Vault and are never held by this component.
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
| M2 — Agent Factory | `internal/factory` | Central coordinator for all agent provisioning |
| M3 — Agent Registry | `internal/registry` | In-memory catalog; state persisted via M7 → Orchestrator → Memory Component |
| M4 — Skill Hierarchy Manager | `internal/skills` | Three-level skill tree with on-demand discovery |
| M5 — Operation Broker | `internal/credentials` | Formats `operation_request` payloads; routes to Orchestrator. The Vault executes and returns `operation_result`. No credential tokens handled here. |
| M6 — Lifecycle Manager | `internal/lifecycle` | Firecracker microVM spawn, monitoring, teardown |
| M7 — Memory Interface | `internal/memory` | Formats `memory_write_request` / `memory_read_request` payloads; routes to Orchestrator via Comms. Does not call any storage API directly. |

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

**Vault-Delegated Execution** — Agents never hold credential tokens. When a skill requires a credentialed external call, the agent submits a typed `vault_operation_request` to the Vault (routed via the Orchestrator). The Vault executes the operation and returns only the result. Credentials never leave the Vault.

**Stateless by Design** — This component owns no persistent storage. All state is delegated to the Memory Component. Enables clean crash recovery and horizontal scaling.

**Single Comms Gateway** — All inter-component messaging flows through `internal/comms`. No module bypasses it. Simplifies auditing, retry logic, and integration testing.

**MicroVM Isolation** — Every agent runs in its own Firecracker microVM. A compromised agent cannot reach another agent or the host.

**No Agent Framework Layer** — The Anthropic Go SDK is used directly. No PI, LangChain, or other agent SDK is layered on top (ADR-003).

---

## Getting Started

### Prerequisites

- Go 1.22+
- NATS Server (for integration testing)
- Firecracker binary (for microVM lifecycle — stub available for development)

### Install & Run

```bash
git clone https://github.com/your-org/aegis-agents
cd aegis-agents
go mod tidy
go build ./...
```

### Configuration

All configuration is environment-based:

| Variable | Required | Description |
|----------|----------|-------------|
| `AEGIS_NATS_URL` | Yes | NATS JetStream endpoint (e.g., `nats://localhost:4222`) |
| `AEGIS_COMPONENT_ID` | No | Identity published in message envelopes (defaults to `aegis-agents`) |

No addresses are configured for OpenBao, the Memory Component, or any other peer. All cross-component communication routes through the Orchestrator via NATS.

### Standalone / Stub Mode

All external dependencies (NATS, Firecracker, Orchestrator, Memory Component, Credential Vault) have in-process stubs. The binary runs fully in-memory without any external services — useful for development and unit testing.

```bash
AEGIS_NATS_URL=nats://localhost:4222 go run ./cmd/aegis-agents
```

### Run Tests

```bash
# Unit tests (no external dependencies)
go test ./...

# Integration tests (requires NATS)
go test ./test/integration/... -tags integration
```

---

## How It Works

### Task Flow

```
Orchestrator
    │
    │  Envelope{ payload: TaskSpec }
    ▼
comms.Subscribe("task_spec")              ← M1: Communications Interface
    │
    │  Unmarshal Envelope → TaskSpec
    ▼
factory.HandleTaskSpec(spec)              ← M2: Agent Factory
    │
    ├─► registry.FindBySkills(domains)    ← M3: Registry
    │       │
    │       ├─ [idle agent found] ──► registry.AssignTask → publish status_update
    │       │
    │       └─ [no match] → provision new agent:
    │               1. skills.GetDomain(domain)              ← M4: Skills
    │               2. lifecycle.Spawn(vmConfig)             ← M6: Lifecycle Manager → Firecracker
    │               3. registry.Register(agentRecord)        ← M3: Registry
    │
    │  Agent executes task (skill discovery on demand)
    │  Credentialed calls → credentials.Dispatch(opReq)     ← M5: Operation Broker
    │                     → comms.Publish("operation_request") → Orchestrator → Vault
    │                     ← operation_result returned to agent
    │
    ▼
factory.CompleteTask(agentID, output)
    │
    ├─► memory.Write(taggedResult)        ← M7: Memory Interface → comms.Publish("memory_write_request")
    ├─► comms.Publish("task_result")      ← M1: back to Orchestrator
    ├─► lifecycle.Terminate(agentID)      ← M6: teardown microVM
    └─► registry.UpdateState("idle")      ← M3: mark agent available
```

### Skill Discovery (Progressive Disclosure)

1. **Domain** — Agent receives only the entry-point domain name at spawn (e.g., `"web"`)
2. **Commands** — Agent queries `GetCommands("web")` to list available operations (e.g., `"web.fetch"`)
3. **Spec** — Agent queries `GetSpec("web", "web.fetch")` only when constructing a specific call

Commands tagged `requires_vault_execution: true` in their spec are dispatched as `operation_request` messages rather than executed directly.

### Credentialed Operations (Vault-Delegated)

When an agent invokes a skill that requires a credentialed external call:

1. The agent calls the skill with its parameters.
2. M5 (Operation Broker) validates the operation type against the agent's `AllowedOps` list and formats an `operation_request`.
3. M1 (Comms) publishes the request to the Orchestrator.
4. The Orchestrator routes it to the Vault. The Vault executes using its stored credentials.
5. The Vault returns the execution result via Orchestrator → `operation_result` inbound message.
6. The agent receives the result and continues processing.

The agent never sees a credential token. No credential material transits this component.

### Shutdown

On `SIGINT` or `SIGTERM`, the component drains in-flight work, closes the comms connection, and exits cleanly.

---

## External Interface

The Agents Component communicates with **exactly one external partner**: the Orchestrator, via NATS JetStream.

| Need | How It's Handled |
|------|-----------------|
| Task assignment | Orchestrator sends `task_spec` inbound |
| Task results | Agents publishes `task_result` to Orchestrator |
| Credentialed operation | Agent sends `vault_operation_request` to Orchestrator; Vault executes and returns `vault_operation_result` |
| Memory persistence | Agents sends `memory_write_request` to Orchestrator; Orchestrator fulfills it |
| Memory retrieval | Agents sends `memory_read_request` to Orchestrator; Orchestrator returns data |
| Capability queries | Orchestrator sends `capability_query`; Agents responds with `capability_response` |

> **Authorization Rule:** The Agents Component is not authorized to communicate with Memory, Credential Vault, User I/O, or any other Aegis OS component except through the Orchestrator. This is a security and architectural boundary — not a convention.

---

## Project Structure

```
aegis-agents/
├── CLAUDE.md                  # AI development briefing (read before coding)
├── README.md                  # This file
├── go.mod
├── go.sum
├── cmd/
│   └── aegis-agents/
│       └── main.go
├── internal/
│   ├── comms/                 # M1: Communications Interface
│   ├── factory/               # M2: Agent Factory
│   ├── registry/              # M3: Agent Registry
│   ├── skills/                # M4: Skill Hierarchy Manager
│   ├── credentials/           # M5: Operation Broker
│   ├── lifecycle/             # M6: Lifecycle Manager
│   └── memory/                # M7: Memory Interface
├── pkg/
│   └── types/                 # Shared types (TaskSpec, AgentRecord, OperationRequest, etc.)
├── config/
│   └── config.go
├── test/
│   └── integration/
└── docs/
    ├── EDD.pdf
    └── ADR/
        ├── 001-native-go.md
        └── 002-centralized-comms.md
```

---

## Documentation

Full design documentation lives in `/docs/`:

- **EDD** — Engineering Design Document covering all module specs, data flows, and interface contracts
- **ADR-001** — Native Go implementation rationale
- **ADR-002** — Centralized communications gateway decision
- **ADR-003** — Anthropic Go SDK direct usage (no agent framework layer)
- **ADR-004** — Vault-executed operations model (no credential delivery to agents)

---

## Contributing

Before contributing, read `CLAUDE.md` for architectural constraints and `docs/EDD.pdf` for the full design spec. All PRs must maintain the module boundaries defined in the EDD.

---

## License

See [LICENSE](LICENSE).
