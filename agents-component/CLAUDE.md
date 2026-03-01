# CLAUDE.md — Aegis Agents Component

This file is the persistent project briefing for Claude Code. Read this at the start of every session before writing any code.

---

## What This Repo Is

`aegis-agents` is the **Agents Component** of Aegis OS — a distributed, hardened operating system purpose-built for running autonomous AI agents. This repo is one of several components built by separate teams. We own the agent lifecycle end-to-end: receiving tasks, provisioning agents, managing skills and credentials, and maintaining the agent registry.

We do **not** own: task routing/matching (Orchestrator), persistent storage (Memory Component), secret storage (Credential Vault / OpenBao), or message transport (Communications Component / NATS JetStream). We integrate with all four.

---

## Language & Stack

- **Language:** Go (native — no Python, no Node)
- **Transport:** NATS JetStream (via Communications Component — never call NATS directly from business logic)
- **Secrets:** Requested via Orchestrator → OpenBao (never called directly from this component)
- **Isolation:** Firecracker microVMs — one VM per agent
- **Storage:** None owned. All persistence delegated to the Memory Component via the Memory Interface module.

---

## Core Architecture Principles

These are settled decisions. Do not re-litigate them.

**1. All Communications Route Through the Orchestrator — No Exceptions**
The Agents Component is **not authorized** to communicate directly with any other Aegis OS component. This includes the Memory Component, Credential Vault (OpenBao), User I/O, and any other system component. The only permitted communication path is:

```
Agents Component → Communications Component → Orchestrator
```

The Orchestrator is the sole authority for coordinating cross-component interactions. If the Agents Component needs data from Memory, it sends a request to the Orchestrator via the Comms Component. The Orchestrator fulfills it. If credentials are needed, the request goes to the Orchestrator — not directly to OpenBao. **There are no exceptions to this rule.**

Enforcement in code:
- The `internal/comms` module is the only package permitted to make outbound calls.
- `internal/comms` publishes exclusively to Orchestrator-addressed NATS topics.
- No other internal module (`factory`, `registry`, `skills`, `credentials`, `lifecycle`, `memory`) may import an external SDK, make an HTTP call, or open a network connection.
- The `internal/memory` and `internal/credentials` modules are **request builders only** — they format and tag payloads for the Orchestrator, then hand them to `internal/comms` for delivery. They do not call Memory or OpenBao APIs directly.
- Any code that bypasses this routing is a security violation and must not be merged.

**2. MicroVM per Agent**
Every agent runs in its own Firecracker microVM. A compromised agent cannot reach another agent or the host OS. The Lifecycle Manager owns VM spawn and teardown.

**3. Progressive Skill Disclosure**
Agents do not receive their full skill set at spawn. Skills are organized in a three-level hierarchy (domain → command → parameter spec). Agents discover skills on demand as they need them. Pre-loading all capabilities degrades performance (context rot). The Skill Hierarchy Manager enforces this.

**4. Lazy Credential Delivery**
Credentials are pre-authorized at spawn time (permission set scoped to the task) but not delivered until the agent explicitly requests a specific credential during skill invocation. This minimizes the exposure window and enforces least privilege. The Credential Broker owns this two-phase model.

**5. Stateless Component**
The Agents Component owns no persistent storage. All state that must survive restarts (agent registry, skill definitions, session history) is published to the Orchestrator via NATS using the Memory Interface module — the Orchestrator routes those writes to the Memory Component. Writes are surgical — only explicitly tagged, resolved data is persisted. Never dump full session state.

**6. Orchestrator Owns Task Matching**
We do not decide which task goes to which agent. The Orchestrator does. We respond to capability queries (does an agent with these skills exist?) and provision agents when asked. Do not build task routing logic into this component.

---

## Module Map

| Module | Package | Responsibility |
|---|---|---|
| M1 — Communications Interface | `internal/comms` | Single inbound/outbound NATS gateway. All external messages enter and leave here. |
| M2 — Agent Factory | `internal/factory` | Central coordinator. Receives task specs, queries registry, initiates provisioning for new agents. |
| M3 — Agent Registry | `internal/registry` | In-memory catalog of agents (ID, state, skill domains, credential permission set). State is persisted via M7 → Orchestrator → Memory Component. |
| M4 — Skill Hierarchy Manager | `internal/skills` | Owns the skills tree. Serves skill discovery on demand. Never pre-loads leaf-level detail. |
| M5 — Credential Broker | `internal/credentials` | Formats credential request payloads scoped to task requirements. Routes requests to Orchestrator via Comms. Does NOT call OpenBao directly. |
| M6 — Lifecycle Manager | `internal/lifecycle` | Spawns and terminates Firecracker microVMs. Health monitoring, crash recovery, state updates. |
| M7 — Memory Interface | `internal/memory` | Formats and dispatches tagged memory payloads to the Orchestrator via Comms (NATS). Never contacts the Memory Component directly. Enforces tagged writes, filtered reads. |

---

## External Interface Contracts

The Agents Component has exactly **one** external communication partner: the **Orchestrator**, reached via the **Communications Component (NATS JetStream)**. All requests for Memory reads/writes, credential access, User I/O, and any other cross-component need are expressed as structured messages to the Orchestrator. The Orchestrator fulfills them and returns responses through the same channel.

### Orchestrator (sole external contact — via Comms / NATS)

**Inbound from Orchestrator:**
- `task_spec` — task assignment including required skill domains and task metadata
- `memory_response` — data returned by the Orchestrator in response to a memory read request
- `credential_response` — scoped credential token returned by the Orchestrator after a credential request
- `capability_query` — Orchestrator asking whether an agent with specific skills exists

**Outbound to Orchestrator:**
- `task_result` — completion payload when an agent finishes a task
- `status_update` — progress and health events during execution
- `capability_response` — answer to Orchestrator capability queries
- `memory_write_request` — request for the Orchestrator to persist tagged agent state
- `memory_read_request` — request for the Orchestrator to retrieve filtered context
- `credential_request` — request for the Orchestrator to obtain a scoped credential from the Vault

### Message Envelope (all outbound messages)
```json
{
  "message_id": "uuid",
  "source": "agents-component",
  "destination": "orchestrator",
  "timestamp": "ISO8601",
  "trace_id": "uuid",
  "payload": {}
}
```

### What Internal Modules Are Responsible For
- `internal/memory` — formats and tags memory read/write payloads; hands to `internal/comms`. **Does not call any storage API.**
- `internal/credentials` — formats credential request payloads with permission scope; hands to `internal/comms`. **Does not call OpenBao API.**
- `internal/comms` — the only module that publishes to or reads from NATS. Exclusively addresses Orchestrator topics.

---

## Skill Hierarchy Schema

```
domain (e.g., "web", "data", "comms", "storage")
  └── command (e.g., "web.fetch", "web.parse")
        └── parameter_spec (full schema: types, required fields, validation rules)
```

Agents receive only the domain name at spawn. They query for available commands within a domain when they need to act. They query for parameter specs only when constructing a specific call. The `skills` package enforces this three-step drill-down.

---

## Agent Lifecycle

```
task_spec received
  → Factory queries Registry for capable agent
  → [Match found] → assign task to existing agent
  → [No match] → Factory initiates provisioning:
      1. Skill Hierarchy Manager: resolve entry-point skill domain
      2. Credential Broker: pre-authorize permission set for task
      3. Lifecycle Manager: spawn Firecracker microVM
      4. Inject: minimal context + skill domain entry point + credential pointer
      5. Registry: register agent with state=active
  → Agent executes task (skill discovery and credential delivery on demand)
  → Agent completes → Factory collects result
  → Memory Interface: persist tagged outputs
  → Comms Interface: publish task_result to Orchestrator
  → Lifecycle Manager: terminate microVM
  → Registry: update agent state to idle or terminated
```

---

## Directory Structure

```
aegis-agents/
├── CLAUDE.md                  # This file
├── README.md
├── go.mod
├── go.sum
├── cmd/
│   └── aegis-agents/
│       └── main.go            # Entry point
├── internal/
│   ├── comms/                 # M1: Communications Interface
│   ├── factory/               # M2: Agent Factory
│   ├── registry/              # M3: Agent Registry
│   ├── skills/                # M4: Skill Hierarchy Manager
│   ├── credentials/           # M5: Credential Broker
│   ├── lifecycle/             # M6: Lifecycle Manager
│   └── memory/                # M7: Memory Interface
├── pkg/
│   └── types/                 # Shared types: TaskSpec, AgentRecord, SkillNode, etc.
├── config/
│   └── config.go              # Environment-based config (NATS URL only — no other peer addresses)
└── docs/
    ├── EDD.pdf                # Engineering Design Document
    └── ADR/
        ├── 001-native-go.md
        └── 002-centralized-comms.md
```

---

## Key Types (pkg/types)

When creating new types, ensure they conform to these core shapes:

```go
// TaskSpec — received from Orchestrator
type TaskSpec struct {
    TaskID       string            `json:"task_id"`
    RequiredSkills []string        `json:"required_skills"` // domain names only
    Metadata     map[string]string `json:"metadata"`
    TraceID      string            `json:"trace_id"`
}

// AgentRecord — stored in Registry
type AgentRecord struct {
    AgentID       string    `json:"agent_id"`
    State         string    `json:"state"` // idle | active | terminated
    SkillDomains  []string  `json:"skill_domains"`
    PermissionSet []string  `json:"permission_set"`
    AssignedTask  string    `json:"assigned_task,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
    UpdatedAt     time.Time `json:"updated_at"`
}

// SkillNode — node in the skill hierarchy
type SkillNode struct {
    Name     string               `json:"name"`
    Level    string               `json:"level"` // domain | command | spec
    Children map[string]*SkillNode `json:"children,omitempty"`
    Spec     *SkillSpec           `json:"spec,omitempty"` // only at leaf level
}

// MemoryWrite — payload sent to Memory Component
type MemoryWrite struct {
    AgentID   string            `json:"agent_id"`
    SessionID string            `json:"session_id"`
    DataType  string            `json:"data_type"`
    TTLHint   int               `json:"ttl_hint_seconds"`
    Payload   interface{}       `json:"payload"`
    Tags      map[string]string `json:"tags"`
}
```

---

## Development Guidelines

- **Interfaces first.** Define the interface for each module before implementing it. This allows parallel development and clean mocking in tests.
- **No direct external calls from business logic.** All external communication must route through `internal/comms`. No internal module may import an external SDK, open a socket, or make an HTTP call. `internal/memory` and `internal/credentials` are request builders — they format payloads and hand them to `internal/comms`.
- **Error propagation.** Use structured errors with context. Every error should carry the module name and operation that produced it.
- **Config via environment.** No hardcoded addresses. The only required external endpoint is `AEGIS_NATS_URL`. Loaded from environment via `config/config.go`.
- **Testing.** Each module must have unit tests with mocked dependencies. Integration tests live in `/test/integration/` and require a running NATS instance.
- **Logging.** Structured JSON logs. Every log entry must include `trace_id` when processing a task.

---

## What We Are Building First

Implement in this order:
1. `pkg/types` — shared type definitions
2. `config/config.go` — environment config
3. `internal/comms` — Communications Interface (stub NATS until integration)
4. `internal/registry` — Agent Registry (in-memory first, Memory Component integration second)
5. `internal/skills` — Skill Hierarchy Manager
6. `internal/credentials` — Credential Broker
7. `internal/lifecycle` — Lifecycle Manager (stub microVM until Firecracker integration)
8. `internal/memory` — Memory Interface
9. `internal/factory` — Agent Factory (wires all modules together)
10. `cmd/aegis-agents/main.go` — Entry point and wiring
