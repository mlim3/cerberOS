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

**Pre-authorization at spawn is still required.** At spawn time, the Credential Broker publishes a `credential.request` (operation: authorize) to the Orchestrator with the agent's required `skill_domains` and TTL. The Orchestrator routes this to the Vault, which registers a scoped policy token and returns a `permission_token` reference — not a raw secret. This lets the Vault fast-check scope at execute time without re-parsing the task spec on every call.

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
| M5 — Operation Broker | `internal/credentials` | Pre-authorizes scoped permission tokens at spawn. Validates runtime requests against pre-authorized scope. Packages `vault.execute.request` payloads (request_id, permission_token, operation_type, operation_params, credential_type, timeout_seconds). Routes to Orchestrator via Comms. The Vault executes; returns `vault.execute.result`. Publishes revocation on termination. **Does NOT call OpenBao directly. Does NOT handle raw credential values.** |
| M6 — Lifecycle Manager | `internal/lifecycle` | Spawns and terminates Firecracker microVMs. Health monitoring, crash recovery, state updates. |
| M7 — Memory Interface | `internal/memory` | Formats and dispatches tagged memory payloads to the Orchestrator via Comms (NATS). Never contacts the Memory Component directly. Enforces tagged writes, filtered reads. |

> **Note on M5 naming:** The package path remains `internal/credentials` for now, but the module's role is operation dispatch — not credential delivery. Do not implement any credential token handling in this package.

---

## External Interface Contracts

The Agents Component has exactly **one** external communication partner: the **Orchestrator**, reached via the **Communications Component (NATS JetStream)**. All requests for Memory reads/writes, credentialed operation execution, User I/O, and any other cross-component need are expressed as structured messages to the Orchestrator. The Orchestrator fulfills them and returns responses through the same channel.

### Orchestrator (sole external contact — via Comms / NATS)

**Inbound from Orchestrator:**

| Message Type | NATS Subject | Delivery | Description |
|---|---|---|---|
| `task.inbound` | `aegis.agents.task.inbound` | At-least-once | New task assignment with skill requirements and permitted scopes |
| `capability.query` | `aegis.agents.capability.query` | At-most-once | Request for skill manifest |
| `lifecycle.terminate` | `aegis.agents.lifecycle.terminate` | At-least-once | Terminate a specific agent |
| `credential.response` | `aegis.agents.credential.response` | At-least-once | Credential authorize/revoke result (restricted to Agents Component consumer group) |
| `vault.execute.result` | `aegis.agents.vault.execute.result` | At-least-once | Final Vault operation result — operation output only, never raw credential (restricted to Agents Component consumer group) |
| `vault.execute.progress` | `aegis.agents.vault.execute.progress` | At-most-once | Progress heartbeat from Vault during long-running operations |
| `state.write.ack` | `aegis.agents.state.write.ack` | At-least-once | State write confirmation |
| `state.read.response` | `aegis.agents.state.read.response` | At-least-once | State records retrieved |
| `clarification.response` | `aegis.agents.clarification.response` | At-least-once | User clarification response |

**Outbound to Orchestrator:**

| Message Type | NATS Subject | Delivery | Description |
|---|---|---|---|
| `task.accepted` | `aegis.orchestrator.task.accepted` | At-least-once | Task receipt confirmation — published immediately on receipt, before provisioning |
| `task.result` | `aegis.orchestrator.task.result` | At-least-once | Completed task output |
| `task.failed` | `aegis.orchestrator.task.failed` | At-least-once | Task failure with error code and user-safe message |
| `capability.response` | `aegis.orchestrator.capability.response` | At-most-once | Skill manifest response |
| `agent.status` | `aegis.orchestrator.agent.status` | At-least-once | Lifecycle state transitions |
| `credential.request` | `aegis.orchestrator.credential.request` | At-least-once | Authorize or revoke scoped permission token |
| `vault.execute.request` | `aegis.orchestrator.vault.execute.request` | At-least-once | Credentialed operation for Vault execution |
| `state.write` | `aegis.orchestrator.state.write` | At-least-once | Tagged memory persistence request |
| `state.read.request` | `aegis.orchestrator.state.read.request` | At-least-once | Filtered memory retrieval request |
| `clarification.request` | `aegis.orchestrator.clarification.request` | At-least-once | User clarification request |
| `audit.event` | `aegis.orchestrator.audit.event` | At-least-once | Security and audit events (append-only) |
| `error` | `aegis.orchestrator.error` | At-least-once | Component-level errors |

### Message Envelope (all outbound publications — required by Comms Interface)
```json
{
  "message_id": "uuid",
  "message_type": "dot.notation.type",
  "source_component": "agents",
  "correlation_id": "uuid (task_id, query_id, or request_id)",
  "timestamp": "ISO8601",
  "schema_version": "1.0",
  "payload": {}
}
```
The Communications Component uses `message_id` for deduplication and `source_component` for access control. Unwrapped payloads are rejected. For `vault.execute.request`, `correlation_id` MUST be set to the `request_id` of the Vault execute operation.

### Vault Execute Request / Result Shape

```json
// vault.execute.request (outbound) — routed by Orchestrator to Vault
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "task_id": "uuid",
  "permission_token": "opaque-token-from-prior-authorize",
  "operation_type": "web_fetch | storage_read | ...",
  "operation_params": {},
  "timeout_seconds": 30,
  "credential_type": "web_api_key"
}

// vault.execute.result (inbound) — returned by Vault via Orchestrator
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "status": "success | timed_out | scope_violation | execution_error",
  "operation_result": {},
  "error_code": "",
  "error_message": "",
  "elapsed_ms": 0
}

// vault.execute.progress (inbound, at-most-once transient)
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "progress_type": "heartbeat | milestone",
  "message": "",
  "elapsed_ms": 0
}
```

### Credential Authorization Shape (sent at spawn)

```json
// credential.request (outbound — authorize phase, at spawn)
{
  "agent_id": "uuid",
  "task_id": "uuid",
  "operation": "authorize",
  "skill_domains": ["web", "data"],
  "ttl_seconds": 3600
}

// credential.response (inbound — returned by Orchestrator/Vault)
{
  "status": "granted | denied",
  "permission_token": "opaque-token-reference",
  "expires_at": "ISO8601"
}
```

### What Internal Modules Are Responsible For
- `internal/memory` — formats and tags memory read/write payloads; hands to `internal/comms`. **Does not call any storage API.**
- `internal/credentials` — formats `vault.execute.request` and `credential.request` payloads; hands to `internal/comms`. **Does not call OpenBao API. Does not handle raw credential values.**
- `internal/comms` — the only module that publishes to or reads from NATS. Exclusively addresses Orchestrator topics.

---

## Skill Hierarchy Schema

```
domain (e.g., "web", "data", "comms", "storage")
  └── command (e.g., "web.fetch", "web.parse")
        └── parameter_spec (full schema: types, required fields, validation rules)
```

Agents receive only the domain name at spawn. They query for available commands within a domain when they need to act. They query for parameter specs only when constructing a specific call. The `skills` package enforces this three-step drill-down.

Commands that require external credentials are tagged in their spec with `requires_cred: true` and `required_credential_types`. When an agent invokes such a command, M5 validates scope and formats a `vault.execute.request` rather than calling the external system directly.

---

## Agent Lifecycle

### 7-State Machine

| State | Description | Valid Transitions |
|---|---|---|
| PENDING | Requested but not yet created | → SPAWNING, → TERMINATED |
| SPAWNING | MicroVM being configured and launched | → ACTIVE, → TERMINATED |
| ACTIVE | Running and executing a task | → IDLE, → RECOVERING |
| IDLE | Task complete; microVM running but unassigned | → ACTIVE, → SUSPENDED, → TERMINATED |
| SUSPENDED | State preserved; microVM paused to free resources | → ACTIVE (fresh credential authorize on wake), → TERMINATED |
| RECOVERING | Crashed; Lifecycle Manager attempting recovery | → ACTIVE (same agent_id, new vm_id), → TERMINATED |
| TERMINATED | Permanently removed from service | (terminal — no transitions) |

Invalid state transitions return errors. All transitions publish `agent.status` to `aegis.orchestrator.agent.status` within 1 second. `AgentRecord.state_history` is append-only.

### Lifecycle Flow

```
task.inbound received
  → publish task.accepted to aegis.orchestrator.task.accepted (IMMEDIATELY — before any provisioning work)
  → Factory queries Registry for capable agent
  → [IDLE/SUSPENDED match found] → assign task to existing agent (re-authorize credentials if SUSPENDED)
  → [No match] → Factory initiates provisioning (spawn context budget enforced: 2,048 token max):
      1. Skill Hierarchy Manager: resolve entry-point skill domain
      2. Credential Broker: publish credential.request (authorize) → receive permission_token reference
      3. Lifecycle Manager: spawn Firecracker microVM (state: PENDING → SPAWNING → ACTIVE)
      4. Inject: minimal context + skill domain entry point + permission_token reference
      5. Registry: register agent with state=ACTIVE
  → Agent executes task (ReAct loop; skill discovery on demand; credentialed calls via vault.execute flow)
  → Agent completes → Factory collects result
  → Memory Interface: persist tagged outputs
  → Comms Interface: publish task.result to aegis.orchestrator.task.result
  → Credential Broker: publish credential.request (revoke)
  → Lifecycle Manager: terminate microVM (state: ACTIVE → IDLE → TERMINATED)
  → Registry: update agent state to TERMINATED
```

### Crash Recovery Sequence

1. Save last known state via Memory Interface (snapshot MUST include `inflight_vault_requests` field listing in-flight Vault execute `request_id`s)
2. Inspect in-flight Vault operations: session log checked for `request_id`s with no corresponding result → flagged UNKNOWN
3. Determine restart vs. replace: `failure_count < max_retries` → respawn; `>= max_retries` → TERMINATED
4. Respawn: fresh microVM with same `agent_id`, new `vm_id`, recovered state injected as context
5. Credential re-authorization: fresh `credential.request` (operation: authorize)
6. Resume from checkpoint: agent reads session log (`is_latest=true` snapshots + uncompacted turns since last snapshot); resubmit in-flight Vault operations with original `request_id` (Vault idempotency guarantees exactly-once execution)
7. Registry: update with new `vm_id`, increment `failure_count` (reset to 0 on successful task completion)

---

## Key Types (pkg/types)

When creating new types, ensure they conform to these core shapes:

```go
// TaskSpec — received from Orchestrator
type TaskSpec struct {
    TaskID              string          `json:"task_id"`              // required; idempotency key
    RequiredSkillDomains []string       `json:"required_skill_domains"` // required; min 1 item
    Priority            int             `json:"priority"`             // 1–10
    TimeoutSeconds      int             `json:"timeout_seconds"`      // 30–86400; hard deadline
    Payload             json.RawMessage `json:"payload"`              // max 1MB; opaque to Agents Component
    UserContextID       string          `json:"user_context_id,omitempty"` // echoed in ALL outbound events
}

// AgentRecord — stored in Registry
// agent_id persists through recovery and microVM replacement; vm_id changes on respawn
type AgentRecord struct {
    AgentID         string       `json:"agent_id"`
    VMID            string       `json:"vm_id,omitempty"` // null if SUSPENDED or TERMINATED
    State           string       `json:"state"` // PENDING | SPAWNING | ACTIVE | IDLE | SUSPENDED | RECOVERING | TERMINATED
    SkillDomains    []string     `json:"skill_domains"`
    PermittedScopes []string     `json:"permitted_scopes"`
    AssignedTask    string       `json:"assigned_task,omitempty"`
    FailureCount    int          `json:"failure_count"` // reset to 0 on successful task completion
    StateHistory    []StateEvent `json:"state_history"`  // append-only ordered state transitions
    CreatedAt       time.Time    `json:"created_at"`
    LastActiveAt    time.Time    `json:"last_active_at"`
}

// StateEvent — one entry in AgentRecord.StateHistory
type StateEvent struct {
    State     string    `json:"state"`
    Timestamp time.Time `json:"timestamp"`
    Reason    string    `json:"reason"`
}

// VaultOperationRequest — sent to Orchestrator for Vault execution
// request_id is the idempotency key; Vault deduplicates on it; safe to resubmit after crash with SAME request_id
// permission_token is the opaque reference from credential pre-authorization — never a raw credential value
type VaultOperationRequest struct {
    RequestID       string          `json:"request_id"`        // UUID; idempotency key
    AgentID         string          `json:"agent_id"`
    TaskID          string          `json:"task_id"`
    PermissionToken string          `json:"permission_token"`  // opaque token from prior authorize; never a raw secret
    OperationType   string          `json:"operation_type"`    // e.g. "web_fetch", "storage_read"
    OperationParams json.RawMessage `json:"operation_params"`  // schema defined per operation_type by Vault
    TimeoutSeconds  int             `json:"timeout_seconds"`   // 1–300; passed to Vault as hard deadline
    CredentialType  string          `json:"credential_type"`   // e.g. "web_api_key"; Vault resolves correct secret internally
}

// VaultOperationResult — returned from Orchestrator after Vault execution
// operation_result contains operation output only — never the raw credential value
type VaultOperationResult struct {
    RequestID       string          `json:"request_id"`
    AgentID         string          `json:"agent_id"`
    Status          string          `json:"status"`            // "success" | "timed_out" | "scope_violation" | "execution_error"
    OperationResult json.RawMessage `json:"operation_result"`  // present on success; contains operation output only
    ErrorCode       string          `json:"error_code,omitempty"`
    ErrorMessage    string          `json:"error_message,omitempty"` // must not expose credential internals or vault paths
    ElapsedMS       int             `json:"elapsed_ms"`
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
// data_type determines routing; audit_log entries are append-only and immutable once written
// Valid data_type values: agent_state | task_result | skill_cache | audit_log | credential_event |
//   episode | snapshot | capability_profile | execution_pattern
type MemoryWrite struct {
    AgentID    string      `json:"agent_id"`
    DataType   string      `json:"data_type"`
    Timestamp  time.Time   `json:"timestamp"`
    Payload    interface{} `json:"payload"` // structured only; raw text/transcripts rejected
    TTLSeconds *int        `json:"ttl_seconds"` // null = no eviction
    RequireAck bool        `json:"require_ack"` // default true for snapshots and audit events
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
│   ├── aegis-agents/
│   │   └── main.go            # Component entry point — starts Comms Interface, wires all modules
│   └── agent-process/
│       └── main.go            # Agent process binary — runs INSIDE each Firecracker microVM; owns ReAct loop
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
│   │   └── simulator/         # Partner simulator: subscribes aegis.orchestrator.*; publishes synthetic responses
│   └── stubs/
│       └── vault/             # OpenBao stub for operation broker tests
└── docs/
    ├── cerberOS_agents_edd_v1_5.pdf   # Engineering Design Document
    ├── cerberOS_agents_prd_v1_5.pdf   # Product Requirements Document
    ├── cerberOS_agents_cic_v1_5.pdf   # Component Interface Catalog
    ├── cerberOS_agents_plan_v1_5.pdf  # Execution Plan
    └── ADR-004-Vault Executed Operations.md
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

## Performance Targets (Non-Functional Requirements)

| ID | Requirement | Target |
|---|---|---|
| NFR-01 | Existing agent path: task receipt to agent-ready | < 2 seconds |
| NFR-02 | New agent, full provisioning | < 30 seconds |
| NFR-03 | Skill hierarchy query (any level) | < 100ms p99 |
| NFR-04 | Vault execute request routing latency | < 50ms p99 |
| NFR-05 | Registry query | < 50ms p99, O(log n) lookup |
| NFR-06 | Capability query response | < 500ms p99 |
| NFR-07 | Auto-recovery success rate | > 95% |
| NFR-08 | Raw credential values in any event/log | 0 occurrences (100% invariant) |
| NFR-09 | In-flight Vault operation recovery | 100% safe resubmission via request_id idempotency |
| NFR-10 | Concurrent agents supported | 1 to 10,000+ |

---

## Development Guidelines

- **Interfaces first.** Define the interface for each module before implementing it. This allows parallel development and clean mocking in tests.
- **No direct external calls from business logic.** All external communication must route through `internal/comms`. No internal module may import an external SDK, open a socket, or make an HTTP call. `internal/memory` and `internal/credentials` are request builders — they format payloads and hand them to `internal/comms`.
- **No credential material in this component.** Agents submit `vault.execute.request` payloads. The Vault executes. Only `operation_result` data (the execution output) flows back into the agent. Never store, log, or forward credential values or `operation_result` content in audit events.
- **`task.accepted` first.** Publish `task.accepted` to `aegis.orchestrator.task.accepted` immediately on receiving a well-formed task — before any provisioning work starts. Deadline: 5 seconds from receipt.
- **`user_context_id` echo.** Echo `user_context_id` from the task spec in every outbound event without exception.
- **Error propagation.** Use structured errors with context. Every error should carry the module name and operation that produced it.
- **Config via environment.** No hardcoded addresses. Required: `AEGIS_NATS_URL` and `AEGIS_COMPONENT_ID`. Loaded from environment via `config/config.go`.
- **Testing.** Each module must have unit tests with mocked dependencies. Integration tests live in `/test/integration/` and require a running NATS instance. Use the partner simulator (`test/integration/simulator`) for cross-component flows.
- **Logging.** Structured JSON logs via `log/slog`. Every in-flight-task log entry must include `trace_id`. Never log credential values, permission tokens, or operation results.

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

## Agent Runtime Specification (cmd/agent-process)

The `cmd/agent-process` binary runs inside each Firecracker microVM. It owns the ReAct execution loop. This is the highest-risk deliverable — everything in M3+ depends on it.

### ReAct Execution Loop (4 Phases)

| Phase | Name | What Happens | Termination Condition |
|---|---|---|---|
| 1 | Reason | LLM receives context window; produces text, tool call, or clarification request | Text with no tool call → task complete; token budget ≥ 95% → CONTEXT_OVERFLOW |
| 2 | Act | Dispatch tool call; if credentialed: Credential Broker validates scope, publishes `vault.execute.request`, goroutine yields awaiting result | Tool error → return error content to LLM; timeout+5s → TOOL_TIMEOUT + cancellation; max retries → task failure |
| 3 | Observe | Receive tool result; split into `content` (enters LLM context) and `details` (monitoring only, never enters context); append to history | Result > 16KB → truncate, append truncation notice |
| 4 | Update Context | History grows by one turn; check token count | < 80% → no action; ≥ 80% → trigger compaction before next Reason phase |

### Tool Contract (every skill MUST satisfy)

Every registered skill must include:
- `name` — unique snake_case, max 64 chars
- `label` — human-readable for monitoring; never shown to LLM
- `description` — max 300 chars; **must include negative guidance** (highest-leverage field for preventing LLM hallucination)
- `parameters` — full JSON Schema with descriptions on every parameter (parameters without descriptions cause hallucination)
- `required_credential_types` — string[] (conditional; required if tool accesses external services; triggers Vault execute routing)
- `execute` — async function returning `{content: MessageContent[], details: object}`
- `timeout_seconds` — default 30s; hard max 300s; passed to Vault as the execute deadline

### Context Budget Thresholds

| Threshold | Value | Enforcement |
|---|---|---|
| Spawn context budget | 2,048 tokens max | Agent Factory at provisioning; abort with `CONTEXT_BUDGET_EXCEEDED` if exceeded |
| Compaction threshold | 80% of model context window | Before each Reason phase |
| Hard abort | 95% of model context window | Abort current turn; emit `CONTEXT_OVERFLOW`; transition to RECOVERING |

### Compaction Algorithm (summarise-and-evict)

1. Compaction window = all turns older than most recent N (default 10, configurable)
2. Internal summarisation LLM call: preserve tool call outcomes, intermediate task state, commitments/constraints; discard conversational filler and redundant observations
3. Validate: if summary > 25% of original compaction window → fall back to extractive (tool call names, status codes, key values only)
4. Replace compaction window with single structured summary turn: `role=system`, prefixed `[COMPACTED SUMMARY — turns N through M]`
5. Persist compaction event (`data_type: episode, entry_type: compaction`)

### Session Persistence Model (append-only tree)

Each turn is a `state.write` with: `entry_id` (UUID), `parent_entry_id` (UUID | null for root), `turn_type` (user_message | assistant_response | tool_call | tool_result | compaction), `content`, `timestamp`.

**Critical**: in-flight Vault execute `request_id` MUST be recorded in the session log BEFORE the goroutine yields. On recovery, inspect session log for `request_id`s with no corresponding result — resubmit with the SAME `request_id`.

### Skill Retrieval Modes

| Mode | When | How | Cost |
|---|---|---|---|
| Structural | Agent knows which domain it needs | `skills --help` → drill to command → param spec; max 3 round trips | Very low |
| Semantic | Agent doesn't know which domain | `skills --search '<query>'` → embedding similarity → top-3 commands (name + description only, no parameters) | Low-medium |

---

## Build Order

Implement in this order:
1. `pkg/types` — shared type definitions
2. `config/config.go` — environment config
3. `internal/comms` — Communications Interface (stub NATS until integration; real NATS JetStream with durable consumers, at-least-once delivery, exponential backoff reconnect)
4. `internal/registry` — Agent Registry (in-memory first, Memory Component integration second)
5. `internal/skills` — Skill Hierarchy Manager
6. `internal/credentials` — Operation Broker (formats `vault.execute.request`; no credential token logic)
7. `internal/lifecycle` — Lifecycle Manager (stub microVM until Firecracker integration; full 7-state machine)
8. `internal/memory` — Memory Interface
9. `internal/factory` — Agent Factory (wires all modules together; enforces 2,048 token spawn budget)
10. `cmd/agent-process/main.go` — Agent process binary (ReAct loop; runs inside microVM)
11. `cmd/aegis-agents/main.go` — Component entry point and wiring

**Critical path warning:** `cmd/agent-process` and its ReAct loop is the single highest-risk deliverable. All lifecycle hardening and persistence work depends on it. No other work should block the agent process binary.
