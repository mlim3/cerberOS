# CLAUDE.md — Aegis Agents Component

This file is the persistent project briefing for Claude Code. Read this at the start of every session before writing any code.

---

## What This Repo Is

`aegis-agents` is the **Agents Component** of Aegis OS — a distributed, hardened operating system purpose-built for running autonomous AI agents. This repo is one of several components built by separate teams. We own the agent lifecycle end-to-end: receiving tasks, provisioning agents, managing skills and credentialed operations, and maintaining the agent registry.

We do **not** own: task routing/matching (Orchestrator), persistent storage (Memory Component), secret storage (Credential Vault / OpenBao), or message transport (Communications Component / NATS JetStream). We integrate with all four.

---

## Language & Stack

- **Language:** Go (native — no Python, no Node)
- **Transport:** NATS JetStream (via Communications Component — never call NATS directly from business logic)
- **Secrets:** Stored exclusively in the Credential Vault (OpenBao). Never accessed directly from this component.
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

The Orchestrator is the sole authority for coordinating cross-component interactions. If the Agents Component needs data from Memory, it sends a request to the Orchestrator via the Comms Component. The Orchestrator fulfills it. If a credentialed operation is needed, the request goes to the Orchestrator — not directly to OpenBao. **There are no exceptions to this rule.**

Enforcement in code:
- The `internal/comms` module is the only package permitted to make outbound calls.
- `internal/comms` publishes exclusively to Orchestrator-addressed NATS topics.
- No other internal module (`factory`, `registry`, `skills`, `credentials`, `lifecycle`, `memory`) may import an external SDK, make an HTTP call, or open a network connection.
- The `internal/memory` and `internal/credentials` modules are **request builders only** — they format and tag payloads for the Orchestrator, then hand them to `internal/comms` for delivery. They do not call Memory or OpenBao APIs directly.
- Any code that bypasses this routing is a security violation and must not be merged.

**2. MicroVM per Agent**
Every agent runs in its own Firecracker microVM. A compromised agent cannot reach another agent or the host OS. The Lifecycle Manager owns VM spawn and teardown.

**3. No Agent Framework Layer**
The Anthropic Go SDK (`github.com/anthropics/anthropic-sdk-go`) is used directly. No intermediary agent framework is layered on top (no PI, LangChain, or any other agent SDK). This is ADR-003. Do not introduce agent framework dependencies.

**4. Progressive Skill Disclosure**
Agents do not receive their full skill set at spawn. Skills are organized in a three-level hierarchy (domain → command → parameter spec). Agents discover skills on demand as they need them. Pre-loading all capabilities degrades performance (context rot). The Skill Hierarchy Manager enforces this.

**5. Vault-Delegated Execution**
Agents never receive credential tokens. When a skill invocation requires a credentialed external call, the agent submits a typed `operation_request` to the Vault (routed via Orchestrator). The Vault executes the operation against the external system using its stored credentials and returns only the result (`operation_result`) to the agent. Credentials never leave the Vault. The Credential Broker (M5) owns this model.

This replaces the prior two-phase "lazy delivery" model. The Agents Component is not authorized to hold, cache, or forward credential material of any kind.

**Pre-authorization at spawn is still required.** At spawn time, the Lifecycle Manager sends an `AgentScopeDeclaration` to the Orchestrator, which registers the agent's permitted `credential_ref` set with the Vault. This lets the Vault reject out-of-scope operation requests at execution time without re-checking the `task_spec` on every call.

**6. Stateless Component**
The Agents Component owns no persistent storage. All state that must survive restarts (agent registry, skill definitions, session history) is published to the Orchestrator via NATS using the Memory Interface module — the Orchestrator routes those writes to the Memory Component. Writes are surgical — only explicitly tagged, resolved data is persisted. Never dump full session state.

**7. Minimal Footprint**
This component runs inside Firecracker microVMs. Binary size and startup time are first-class concerns. No fat runtime dependencies, no unnecessary allocations. Go's standard library is preferred over third-party packages wherever equivalent.

**8. Orchestrator Owns Task Matching**
We do not decide which task goes to which agent. The Orchestrator does. We respond to capability queries (does an agent with these skills exist?) and provision agents when asked. Do not build task routing logic into this component.

---

## Module Map

| Module | Package | Responsibility |
|---|---|---|
| M1 — Communications Interface | `internal/comms` | Single inbound/outbound NATS gateway. All external messages enter and leave here. |
| M2 — Agent Factory | `internal/factory` | Central coordinator. Receives task specs, queries registry, initiates provisioning for new agents. |
| M3 — Agent Registry | `internal/registry` | In-memory catalog of agents (ID, state, skill domains, authorized operation types). State is persisted via M7 → Orchestrator → Memory Component. |
| M4 — Skill Hierarchy Manager | `internal/skills` | Owns the skills tree. Serves skill discovery on demand. Never pre-loads leaf-level detail. |
| M5 — Operation Broker | `internal/credentials` | Packages `vault_operation_request` payloads (operation type, endpoint, credential_ref, scope). Routes to Orchestrator via Comms. The Vault executes; returns `vault_operation_result`. **Does NOT call OpenBao directly. Does NOT handle credential values.** |
| M6 — Lifecycle Manager | `internal/lifecycle` | Spawns and terminates Firecracker microVMs. Health monitoring, crash recovery, state updates. |
| M7 — Memory Interface | `internal/memory` | Formats and dispatches tagged memory payloads to the Orchestrator via Comms (NATS). Never contacts the Memory Component directly. Enforces tagged writes, filtered reads. |

> **Note on M5 naming:** The package path remains `internal/credentials` for now, but the module's role is operation dispatch — not credential delivery. Do not implement any credential token handling in this package.

---

## External Interface Contracts

The Agents Component has exactly **one** external communication partner: the **Orchestrator**, reached via the **Communications Component (NATS JetStream)**. All requests for Memory reads/writes, credentialed operation execution, User I/O, and any other cross-component need are expressed as structured messages to the Orchestrator. The Orchestrator fulfills them and returns responses through the same channel.

### Orchestrator (sole external contact — via Comms / NATS)

**Inbound from Orchestrator:**

| Message Type | NATS Subject | Description |
|---|---|---|
| `task_spec` | `aegis.agents.tasks.{agent_id}` | Task assignment with skill requirements and permitted scopes |
| `capability_query` | `aegis.agents.capability` | Request for skill manifest |
| `vault_operation_result` | `aegis.agents.vault.result.{agent_id}` | Result of a Vault-executed credentialed operation |
| `memory_read_result` | `aegis.agents.memory.result.{agent_id}` | Result of a Memory Component read request |

**Outbound to Orchestrator:**

| Message Type | NATS Subject | Description |
|---|---|---|
| `task_result` | `aegis.orchestrator.results` | Completed task output |
| `capability_response` | `aegis.orchestrator.capability` | Skill manifest response |
| `vault_operation_request` | `aegis.orchestrator.vault` | Credentialed operation for Vault execution |
| `memory_write_request` | `aegis.orchestrator.memory.write` | Tagged memory persistence request |
| `memory_read_request` | `aegis.orchestrator.memory.read` | Filtered memory retrieval request |
| `agent_status` | `aegis.orchestrator.status` | Lifecycle events (spawned, healthy, failed, terminated) |

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

### Operation Request / Result Shape

```json
// vault_operation_request (outbound)
{
  "agent_id": "uuid",
  "task_id": "uuid",
  "operation_type": "http_get | http_post | ...",
  "endpoint": "https://...",
  "headers": {},
  "body": {},
  "credential_ref": "stripe-api-key",
  "scope_required": "aegis-agents-payments"
}

// vault_operation_result (inbound)
{
  "request_id": "uuid",
  "status_code": 200,
  "body": {},
  "error": ""
}
```

### Scope Declaration Shape (sent at spawn)

```json
// agent_scope_declaration (outbound, sent by Lifecycle Manager at spawn)
{
  "agent_id": "uuid",
  "task_id": "uuid",
  "permitted_scopes": ["aegis-agents-web", "aegis-agents-data"],
  "ttl_seconds": 3600
}
```

### What Internal Modules Are Responsible For
- `internal/memory` — formats and tags memory read/write payloads; hands to `internal/comms`. **Does not call any storage API.**
- `internal/credentials` — formats `vault_operation_request` payloads; hands to `internal/comms`. **Does not call OpenBao API. Does not handle credential values.**
- `internal/comms` — the only module that publishes to or reads from NATS. Exclusively addresses Orchestrator topics.

---

## Skill Hierarchy Schema

```
domain (e.g., "web", "data", "comms", "storage")
  └── command (e.g., "web.fetch", "web.parse")
        └── parameter_spec (full schema: types, required fields, validation rules)
```

Agents receive only the domain name at spawn. They query for available commands within a domain when they need to act. They query for parameter specs only when constructing a specific call. The `skills` package enforces this three-step drill-down.

Commands that require external credentials are tagged in their spec with `requires_cred: true`. When an agent invokes such a command, M5 formats a `vault_operation_request` rather than calling the external system directly.

---

## Agent Lifecycle

```
task_spec received
  → Factory queries Registry for capable agent
  → [Match found] → assign task to existing agent
  → [No match] → Factory initiates provisioning:
      1. Skill Hierarchy Manager: resolve entry-point skill domain
      2. Lifecycle Manager: spawn Firecracker microVM
      3. Inject: minimal context + skill domain entry point + authorized operation types
      4. Registry: register agent with state=active
  → Agent executes task (skill discovery on demand; credentialed calls dispatched via Operation Broker)
  → Agent completes → Factory collects result
  → Memory Interface: persist tagged outputs
  → Comms Interface: publish task_result to Orchestrator
  → Lifecycle Manager: terminate microVM
  → Registry: update agent state to idle or terminated
```

---

## Key Types (pkg/types)

When creating new types, ensure they conform to these core shapes:

```go
// TaskSpec — received from Orchestrator
type TaskSpec struct {
    TaskID          string          `json:"task_id"`
    AgentID         string          `json:"agent_id"`
    SkillDomains    []string        `json:"skill_domains"`
    PermittedScopes []string        `json:"permitted_scopes"` // credential_ref values this task may invoke
    Instructions    string          `json:"instructions"`
    ContextWindow   json.RawMessage `json:"context_window,omitempty"`
    TimeoutSeconds  int             `json:"timeout_seconds"`
}

// AgentRecord — stored in Registry
type AgentRecord struct {
    AgentID         string    `json:"agent_id"`
    State           string    `json:"state"` // idle | active | terminated
    SkillDomains    []string  `json:"skill_domains"`
    PermittedScopes []string  `json:"permitted_scopes"`
    AssignedTask    string    `json:"assigned_task,omitempty"`
    CreatedAt       time.Time `json:"created_at"`
    UpdatedAt       time.Time `json:"updated_at"`
}

// VaultOperationRequest — sent to Orchestrator for Vault execution
type VaultOperationRequest struct {
    AgentID       string            `json:"agent_id"`
    TaskID        string            `json:"task_id"`
    OperationType string            `json:"operation_type"` // "http_get", "http_post", etc.
    Endpoint      string            `json:"endpoint"`
    Headers       map[string]string `json:"headers,omitempty"`
    Body          json.RawMessage   `json:"body,omitempty"`
    CredentialRef string            `json:"credential_ref"` // logical name, e.g. "stripe-api-key"
    ScopeRequired string            `json:"scope_required"` // e.g. "aegis-agents-payments"
}

// VaultOperationResult — returned from Orchestrator after Vault execution
type VaultOperationResult struct {
    RequestID  string          `json:"request_id"`
    StatusCode int             `json:"status_code"`
    Body       json.RawMessage `json:"body"`
    Error      string          `json:"error,omitempty"`
}

// AgentScopeDeclaration — sent at spawn to register permitted credential_refs with Vault
type AgentScopeDeclaration struct {
    AgentID         string   `json:"agent_id"`
    TaskID          string   `json:"task_id"`
    PermittedScopes []string `json:"permitted_scopes"`
    TTLSeconds      int      `json:"ttl_seconds"`
}

// SkillDescriptor — entry in the skill manifest
type SkillDescriptor struct {
    Name          string `json:"name"`
    Domain        string `json:"domain"`
    Description   string `json:"description"`
    RequiresCred  bool   `json:"requires_cred"`
    CredentialRef string `json:"credential_ref,omitempty"` // logical name only — never a value
}

// SkillNode — node in the skill hierarchy
type SkillNode struct {
    Name          string                `json:"name"`
    Level         string                `json:"level"` // domain | command | spec
    Children      map[string]*SkillNode `json:"children,omitempty"`
    Spec          *SkillSpec            `json:"spec,omitempty"` // only at leaf level
    RequiresCred  bool                  `json:"requires_cred,omitempty"`
    CredentialRef string                `json:"credential_ref,omitempty"`
}

// MemoryWrite — payload sent to Memory Component via Orchestrator
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
│   ├── credentials/           # M5: Operation Broker
│   ├── lifecycle/             # M6: Lifecycle Manager
│   └── memory/                # M7: Memory Interface
├── pkg/
│   └── types/                 # Shared types: TaskSpec, AgentRecord, SkillNode, etc.
├── config/
│   └── config.go              # Environment-based config (NATS URL only — no other peer addresses)
├── test/
│   ├── integration/
│   └── stubs/
│       └── vault/             # OpenBao stub for operation broker tests
└── docs/
    ├── EDD.pdf                # Engineering Design Document
    └── ADR/
        ├── 001-native-go.md
        ├── 002-centralized-comms.md
        ├── 003-anthropic-sdk.md
        └── 004-vault-executed-operations.md
```

---

## Security Invariant

No raw credential value — API key, password, token, or secret of any kind — may appear in:
- Any struct field in this codebase
- Any NATS message payload
- Any log line
- Any error message

If a test requires a credential value, use the OpenBao stub (`test/stubs/vault`) which returns synthetic tokens. A credential value appearing anywhere in the codebase is an architectural violation.

If you find yourself adding a field like `APIKey string` to any type, stop — you are building the wrong thing.

---

## Development Guidelines

- **Interfaces first.** Define the interface for each module before implementing it. This allows parallel development and clean mocking in tests.
- **No direct external calls from business logic.** All external communication must route through `internal/comms`. No internal module may import an external SDK, open a socket, or make an HTTP call. `internal/memory` and `internal/credentials` are request builders — they format payloads and hand them to `internal/comms`.
- **No credential material in this component.** Agents submit `vault_operation_request` payloads. The Vault executes. Only `vault_operation_result` data (the execution output) flows back into the agent. Never store, log, or forward credential values.
- **Error propagation.** Use structured errors with context. Every error should carry the module name and operation that produced it.
- **Config via environment.** No hardcoded addresses. The only required external endpoint is `AEGIS_NATS_URL`. Loaded from environment via `config/config.go`.
- **Testing.** Each module must have unit tests with mocked dependencies. Integration tests live in `/test/integration/` and require a running NATS instance.
- **Logging.** Structured JSON logs. Every log entry must include `trace_id` when processing a task.

---

## Code Enforcement Rules

These will be caught in review. Do not ship code that violates them.

- `internal/credentials` must contain **zero** references to actual credential values, OpenBao SDK imports, or HTTP client code. It is a request formatter and router only.
- `internal/memory` must contain **zero** direct database calls, libSQL imports, or storage SDK references. It is a request formatter and router only.
- Only `internal/comms` may import `github.com/nats-io/nats.go` or make network calls.
- No package other than `internal/comms` may open a network connection of any kind.
- `VaultOperationRequest` and `VaultOperationResult` must never contain a field that holds a raw credential value.
- All NATS subjects must follow the `aegis.*` namespace convention defined in the interface contracts above.

---

## Build Order

Implement in this order:
1. `pkg/types` — shared type definitions
2. `config/config.go` — environment config
3. `internal/comms` — Communications Interface (stub NATS until integration)
4. `internal/registry` — Agent Registry (in-memory first, Memory Component integration second)
5. `internal/skills` — Skill Hierarchy Manager
6. `internal/credentials` — Operation Broker (formats `operation_request`; no credential token logic)
7. `internal/lifecycle` — Lifecycle Manager (stub microVM until Firecracker integration)
8. `internal/memory` — Memory Interface
9. `internal/factory` — Agent Factory (wires all modules together)
10. `cmd/aegis-agents/main.go` — Entry point and wiring
