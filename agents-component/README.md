# aegis-agents

The **Agents Component** of [Aegis OS](https://github.com/your-org/aegis-os) — a distributed operating system purpose-built for running autonomous AI agents.

---

## Overview

`aegis-agents` is an agent lifecycle management system. It acts as an agent factory: receiving task specifications from the Orchestrator, provisioning the right agent for each task (reusing existing agents or building new ones), and managing agents through their full lifecycle — from spawn to termination.

This component is one of five in the Aegis OS platform. It does not own task routing, persistent storage, secrets, or message transport — those belong to adjacent components. It integrates with all four through well-defined contracts.

---

## Responsibilities

- **Agent Provisioning** — Spawn new agents inside Firecracker microVMs when no capable agent exists for a task
- **Agent Registry** — Maintain a catalog of all agents, their capabilities, states, and credential permission sets
- **Skill Management** — Serve agent skills via a progressive disclosure hierarchy (domain → command → parameter spec)
- **Credential Brokering** — Pre-authorize credential access at spawn; deliver credentials lazily at runtime via requests to the Orchestrator
- **Lifecycle Management** — Health monitoring, crash recovery, graceful shutdown, and VM teardown
- **State Persistence** — Delegate all persistence to the Memory Component via a disciplined interface

---

## Architecture

The component is organized into seven modules with a strict single-responsibility principle. All external communication flows through a single gateway — no module reaches out to an external component directly.

```
┌─────────────────────────────────────────────────────┐
│                  aegis-agents                        │
│                                                      │
│  ┌─────────────┐        ┌──────────────────────┐    │
│  │ Comms       │◄──────►│   Agent Factory (M2) │    │
│  │ Interface   │        └──────────────────────┘    │
│  │ (M1)        │           │        │        │      │
│  └─────────────┘           ▼        ▼        ▼      │
│         │           ┌────────┐ ┌──────┐ ┌────────┐  │
│         │           │Registry│ │Skills│ │Creds   │  │
│         │           │ (M3)   │ │ (M4) │ │Broker  │  │
│         │           └────────┘ └──────┘ │ (M5)   │  │
│         │                               └────────┘  │
│         │           ┌───────────────────────────┐   │
│         │           │  Lifecycle Manager (M6)   │   │
│         │           └───────────────────────────┘   │
│         │           ┌───────────────────────────┐   │
│         │           │  Memory Interface (M7)    │   │
│         │           └───────────────────────────┘   │
└─────────────────────────────────────────────────────┘
         │
         ▼
   ┌─────────────────────────────────┐
   ┌─────────────────────────────────┐
   │ Orchestrator (sole external peer│
   │      via NATS JetStream)        │
   └─────────────────────────────────┘
```

| Module | Package | Role |
|--------|---------|------|
| M1 — Communications Interface | `internal/comms` | Single NATS gateway for all inter-component messaging |
| M2 — Agent Factory | `internal/factory` | Central coordinator for all agent provisioning |
| M3 — Agent Registry | `internal/registry` | In-memory catalog; state persisted via M7 → Orchestrator → Memory Component |
| M4 — Skill Hierarchy Manager | `internal/skills` | Three-level skill tree with on-demand discovery |
| M5 — Credential Broker | `internal/credentials` | Formats `credential_request` payloads; routes to Orchestrator via Comms. Does not call OpenBao directly. |
| M6 — Lifecycle Manager | `internal/lifecycle` | Firecracker microVM spawn, monitoring, teardown |
| M7 — Memory Interface | `internal/memory` | Formats `memory_write_request` / `memory_read_request` payloads; routes to Orchestrator via Comms. Does not call any storage API directly. |

---

## Key Design Decisions

**Progressive Skill Disclosure** — Agents do not receive their full skill set at spawn. Skills are served on demand as agents drill down the hierarchy. This prevents context rot and keeps agent context focused on the active task.

**Lazy Credential Delivery** — Credentials are pre-authorized at spawn (scoped to the task's required skills) but not delivered until the agent explicitly requests them at runtime. Minimizes exposure window.

**Stateless by Design** — This component owns no persistent storage. All state is delegated to the Memory Component. Enables clean crash recovery and horizontal scaling.

**Single Comms Gateway** — All inter-component messaging flows through `internal/comms`. No module bypasses it. Simplifies auditing, retry logic, and integration testing.

**MicroVM Isolation** — Every agent runs in its own Firecracker microVM. A compromised agent cannot reach another agent or the host.

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

All external dependencies (NATS, Firecracker, Orchestrator, Memory Component, Credential Vault) have in-process stubs. The binary runs fully in-memory without any external services — useful for development and unit testing. Only `AEGIS_NATS_URL` is required; it can point to a non-existent address since no real connection is made in stub mode:

```bash
AEGIS_NATS_URL=nats://localhost:4222 go run ./cmd/aegis-agents
```

### Run Tests

```bash
# Unit tests (no external dependencies)
go test ./internal/...

# Integration tests (requires NATS)
go test ./test/integration/...
```

---

## How It Works

### Startup

On launch, `main.go` loads config, wires all seven modules via dependency injection into the Agent Factory, seeds the skill tree, and subscribes to the `task_spec` NATS subject. The component is then ready to receive tasks.

### Task Flow

```
Orchestrator
    │
    │  Envelope{ payload: TaskSpec }
    ▼
comms.Subscribe("task_spec")          ← M1: Communications Interface
    │
    │  Unmarshal Envelope → TaskSpec
    ▼
factory.HandleTaskSpec(spec)          ← M2: Agent Factory
    │
    ├─► registry.FindBySkills(domains) ← M3: Registry
    │       │
    │       ├─ [idle agent found] ──► registry.AssignTask → publish status_update
    │       │
    │       └─ [no match] → provision new agent:
    │               1. skills.GetDomain(domain)          ← M4: Skills
    │               2. credentials.PreAuthorize(agentID) ← M5: Credential Broker → comms.Publish("credential_request") → Orchestrator → Vault
    │               3. lifecycle.Spawn(vmConfig)         ← M6: Lifecycle Manager → Firecracker
    │               4. registry.Register(agentRecord)    ← M3: Registry
    │
    │  Agent executes task (skill discovery + lazy credential delivery on demand)
    │
    ▼
factory.CompleteTask(agentID, output)
    │
    ├─► memory.Write(taggedResult)      ← M7: Memory Interface → comms.Publish("memory_write_request") → Orchestrator → Memory Component
    ├─► comms.Publish("task_result")    ← M1: back to Orchestrator
    ├─► lifecycle.Terminate(agentID)    ← M6: teardown microVM
    ├─► credentials.Revoke(agentID)     ← M5: invalidate scoped token
    └─► registry.UpdateState("idle")    ← M3: mark agent available
```

### Skill Discovery (Progressive Disclosure)

Agents do not receive their full capability set at spawn. The three-step drill-down prevents context bloat:

1. **Domain** — Agent receives only the entry-point domain name at spawn (e.g., `"web"`)
2. **Commands** — Agent queries `GetCommands("web")` to list available operations (e.g., `"web.fetch"`)
3. **Spec** — Agent queries `GetSpec("web", "web.fetch")` only when constructing a specific call

### Credential Delivery (Two-Phase)

1. **Pre-authorize (spawn time)** — Credential Broker sends a `credential_request` to the Orchestrator with the permission set scoped to the task's required skill domains. The Orchestrator proxies this to the Vault and returns a `credential_response`. The token is stored internally; the agent receives only a pointer.
2. **Lazy delivery (runtime)** — When the agent invokes a skill that requires a credential, the Broker validates the request against the pre-approved permission set and sends another `credential_request` to the Orchestrator for the specific secret value. Nothing else is disclosed.

### Shutdown

On `SIGINT` or `SIGTERM`, the component drains in-flight work, closes the comms connection, and exits cleanly.

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
│   ├── credentials/           # M5: Credential Broker
│   ├── lifecycle/             # M6: Lifecycle Manager
│   └── memory/                # M7: Memory Interface
├── pkg/
│   └── types/                 # Shared types (TaskSpec, AgentRecord, etc.)
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

## External Integrations

The Agents Component communicates with **exactly one external partner**: the Orchestrator, via the Communications Component (NATS JetStream). There are no direct connections to any other component.

| Need | How It's Handled |
|------|-----------------|
| Task assignment | Orchestrator sends `task_spec` inbound |
| Task results | Agents publishes `task_result` to Orchestrator |
| Memory persistence | Agents sends `memory_write_request` to Orchestrator; Orchestrator fulfills it |
| Memory retrieval | Agents sends `memory_read_request` to Orchestrator; Orchestrator returns `memory_response` |
| Credential access | Agents sends `credential_request` to Orchestrator; Orchestrator proxies to Vault and returns `credential_response` |
| Capability queries | Orchestrator sends `capability_query`; Agents responds with `capability_response` |

> **Authorization Rule:** The Agents Component is not authorized to communicate with Memory, Credential Vault, User I/O, or any other Aegis OS component except through the Orchestrator. This is a security and architectural boundary — not a convention.

---

## Documentation

Full design documentation lives in `/docs/`:

- **EDD** — Engineering Design Document covering all module specs, data flows, and interface contracts
- **ADR-001** — Native Go implementation rationale
- **ADR-002** — Centralized communications gateway decision

---

## Contributing

This component is part of Aegis OS. Before contributing, read `CLAUDE.md` for architectural constraints and `docs/EDD.pdf` for the full design spec. All PRs must maintain the module boundaries defined in the EDD.

---

## License

See [LICENSE](LICENSE).
