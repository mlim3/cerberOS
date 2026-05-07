# Partner Onboarding Guide — Agents Component

This guide is for teams building components that integrate with the **Agents Component** (`aegis-agents`). It covers exactly what each partner needs to know, implement, and test to bring their integration live.

The Agents Component has **one external communication partner**: the Orchestrator, reached via NATS JetStream. Every other component (Credential Vault, Memory/Storage, User I/O) integrates indirectly — through the Orchestrator, which brokers cross-component requests on our behalf.

---

## Contents

- [Architecture Overview](#architecture-overview)
- [Message Envelope Standard](#message-envelope-standard)
- [Orchestrator](#orchestrator)
- [Communications Component (NATS)](#communications-component-nats)
- [Credential Vault (OpenBao)](#credential-vault-openbao)
- [Memory / Storage Component](#memory--storage-component)
- [User I/O Component](#userio-component)
- [Testing with the Partner Simulator](#testing-with-the-partner-simulator)
- [Security Invariants — Non-Negotiable](#security-invariants--non-negotiable)

---

## Architecture Overview

```
                    ┌─────────────────────────────────┐
                    │           User I/O               │
                    └──────────────┬──────────────────┘
                                   │ user context / clarification
                    ┌──────────────▼──────────────────┐
                    │           Orchestrator           │
                    └──┬──────────┬──────────┬────────┘
           task routing│ cred ops │ state ops│ vault ops
           ┌───────────▼┐  ┌──────▼──┐  ┌───▼──────────────┐
           │   Agents   │  │  Vault  │  │  Memory/Storage  │
           │ Component  │  │(OpenBao)│  │                  │
           └────────────┘  └─────────┘  └──────────────────┘
                   │
           ┌───────▼───────┐
           │  NATS/JetStream│
           │  (Comms Layer) │
           └───────────────┘
```

**Key architectural rule:** The Agents Component does not communicate directly with any component except the Orchestrator, via NATS. All credential, memory, and user I/O operations are dispatched as NATS messages to the Orchestrator, which routes them to the appropriate downstream component and returns responses.

---

## Message Envelope Standard

Every message published to or from the Agents Component must be wrapped in this envelope. **Unwrapped payloads are rejected.**

```json
{
  "message_id": "<UUID v4>",
  "message_type": "<dot.notation.type>",
  "source_component": "<component name>",
  "correlation_id": "<UUID — task_id, request_id, or query_id this message relates to>",
  "timestamp": "<ISO 8601, e.g. 2026-01-01T00:00:00Z>",
  "schema_version": "1.0",
  "payload": { }
}
```

**Special rule for `vault.execute.request`:** `correlation_id` MUST be set to the `request_id` field of the Vault execute operation. This is how the Agents Component correlates results back to in-flight operations.

---

## Orchestrator

The Orchestrator is the Agents Component's only external counterpart. It routes every cross-component request (credentials, memory reads/writes, User I/O) and is responsible for the full task lifecycle.

### Complete Subject Map

#### Inbound (Orchestrator → Agents Component)

| Subject | Delivery | Purpose |
|---------|----------|---------|
| `aegis.agents.task.inbound` | At-least-once | Dispatch a task to the Agents Component |
| `aegis.agents.capability.query` | At-most-once | Ask whether an agent with given skill domains exists |
| `aegis.agents.lifecycle.terminate` | At-least-once | Forcibly terminate a specific agent |
| `aegis.agents.credential.response` | At-least-once | Result of a credential authorize or revoke operation |
| `aegis.agents.vault.execute.result` | At-least-once | Final result of a Vault-executed credentialed operation |
| `aegis.agents.vault.execute.progress` | At-most-once | Progress heartbeat during a long-running Vault operation |
| `aegis.agents.state.write.ack` | At-least-once | Confirmation that a state write was persisted |
| `aegis.agents.state.read.response` | At-least-once | State records retrieved from the Memory Component |
| `aegis.agents.clarification.response` | At-least-once | User's answer to a clarification question |
| `aegis.agents.steering.<agent_id>` | At-most-once | Mid-task directive to a running agent (redirect, abort_tool, inject_context, cancel) |
| `aegis.agents.agent.spawn.response` | At-least-once | Result of a child agent spawned by a parent agent |

#### Outbound (Agents Component → Orchestrator)

| Subject | Delivery | Purpose |
|---------|----------|---------|
| `aegis.orchestrator.task.accepted` | At-least-once | Confirm task received; published before provisioning starts |
| `aegis.orchestrator.task.result` | At-least-once | Final task output on successful completion |
| `aegis.orchestrator.task.failed` | At-least-once | Task failure with error code and user-safe message |
| `aegis.orchestrator.capability.response` | At-most-once | Reply to a capability query |
| `aegis.orchestrator.agent.status` | At-least-once | Agent state transition (SPAWNING → ACTIVE → IDLE, etc.) |
| `aegis.orchestrator.credential.request` | At-least-once | Request to authorize or revoke a scoped permission token |
| `aegis.orchestrator.vault.execute.request` | At-least-once | Request to execute a credentialed operation via the Vault |
| `aegis.orchestrator.vault.execute.cancel` | At-least-once | Cancel an in-flight Vault operation (agent deadline fired) |
| `aegis.orchestrator.state.write` | At-least-once | Request to persist state to the Memory Component |
| `aegis.orchestrator.state.read.request` | At-least-once | Request to retrieve state from the Memory Component |
| `aegis.orchestrator.clarification.request` | At-least-once | Surface a question to the user via User I/O |
| `aegis.orchestrator.steering.ack` | At-least-once | Confirm a steering directive was received and applied |
| `aegis.orchestrator.agent.spawn.request` | At-least-once | Parent agent requesting a child agent sub-task |
| `aegis.orchestrator.audit.event` | At-least-once | Security and audit event (append-only) |
| `aegis.orchestrator.error` | At-least-once | Component-level errors and dead-letter events |

---

### Task Dispatch

The Orchestrator dispatches tasks by publishing a `task.inbound` message. The Agents Component immediately acknowledges with `task.accepted` (within 5 seconds), then begins provisioning or assignment. The Orchestrator should not wait for provisioning to complete before considering the task handed off.

**task.inbound payload:**

```json
{
  "task_id": "uuid",
  "required_skills": ["web", "data"],
  "instructions": "Fetch the homepage of example.com and summarise it.",
  "metadata": { },
  "trace_id": "trace-uuid",
  "user_context_id": "ctx-uuid"
}
```

| Field | Required | Notes |
|-------|----------|-------|
| `task_id` | Yes | UUID; globally unique; idempotency key — duplicate task_ids are rejected after first receipt |
| `required_skills` | Yes | Array of domain names, min 1 item (e.g. `["web"]`) |
| `instructions` | Yes | Natural-language task description passed to the agent at spawn |
| `trace_id` | Yes | Propagated unchanged through all outbound events for distributed tracing |
| `user_context_id` | No | If set, echoed in **every** outbound event for Orchestrator routing to User I/O |

**task.accepted payload (outbound from Agents Component):**

```json
{
  "task_id": "uuid",
  "agent_id": "uuid",
  "agent_type": "new_provision",
  "estimated_completion_at": null,
  "user_context_id": "ctx-uuid",
  "trace_id": "trace-uuid"
}
```

`agent_type` is `"new_provision"` or `"existing_assigned"`. SLA: published within 5 seconds of receiving a well-formed `task.inbound`.

**task.result payload (outbound):**

```json
{
  "task_id": "uuid",
  "agent_id": "uuid",
  "success": true,
  "output": { },
  "error": "",
  "trace_id": "trace-uuid"
}
```

**task.failed payload (outbound):**

```json
{
  "task_id": "uuid",
  "agent_id": "uuid",
  "error_code": "VAULT_UNREACHABLE",
  "error_message": "The agent could not complete the task due to a credential system error.",
  "phase": "credential_auth",
  "trace_id": "trace-uuid"
}
```

Error codes: `PROVISIONING_FAILED`, `TIMEOUT`, `SCOPE_VIOLATION`, `RECOVERY_EXHAUSTED`, `VAULT_UNREACHABLE`, `CLARIFICATION_TIMEOUT`, `CONTEXT_BUDGET_EXCEEDED`.

---

### Capability Queries

The Orchestrator asks whether an agent with specific skill domains is currently available before routing tasks. This is at-most-once — expect no delivery guarantee.

**capability.query payload (inbound):**

```json
{
  "query_id": "uuid",
  "domains": ["web", "data"],
  "trace_id": "trace-uuid"
}
```

**capability.response payload (outbound):**

```json
{
  "query_id": "uuid",
  "domains": ["web", "data"],
  "has_match": true,
  "trace_id": "trace-uuid"
}
```

SLA: respond within 500ms p99.

---

### Agent Status Events

The Agents Component publishes a `agent.status` event within 1 second of every state transition. The Orchestrator must consume these to maintain accurate task tracking.

**agent.status payload (outbound):**

```json
{
  "task_id": "uuid",
  "agent_id": "uuid",
  "state": "active",
  "message": "Agent transitioned from spawning to active.",
  "trace_id": "trace-uuid"
}
```

State machine: `PENDING → SPAWNING → ACTIVE → IDLE → TERMINATED` with possible `RECOVERING` and `SUSPENDED` states. See the README for the full 7-state diagram.

---

### Credential Operation Routing

When an agent is provisioned, the Agents Component publishes a `credential.request` (authorize) to the Orchestrator, which routes it to the Credential Vault and returns the response. At termination, a revoke request is published.

The Orchestrator must:
- Route both `authorize` and `revoke` requests to the Vault
- Return the response on `aegis.agents.credential.response` within 2 seconds p99
- Restrict `aegis.agents.credential.response` to the Agents Component consumer group — no other component should receive it
- Return `status: denied` (not `error`) when the Vault refuses; `error_reason` must not expose vault internals
- Process `revoke` asynchronously and non-blockingly after task completion

See the [Credential Vault section](#credential-vault-openbao) for the full request/response shapes.

---

### Vault Operation Routing

The Orchestrator routes `vault.execute.request` to the Vault and relays results back. This is the primary integration path for credentialed tool calls.

The Orchestrator must:
- Route `vault.execute.request` to the Vault immediately
- Relay `vault.execute.result` to `aegis.agents.vault.execute.result` within `timeout_seconds + routing overhead`
- If the Vault doesn't respond in time, publish a result with `status: timed_out`
- Relay Vault progress events (`vault.execute.progress`) at-most-once — **do not buffer or replay them**
- Restrict `aegis.agents.vault.execute.result` to the Agents Component consumer group
- Echo the `request_id` from the request through to the result — this is the idempotency key for crash recovery
- Enforce that `vault.execute.result.operation_result` contains operation output only, **never a raw credential value**

See the [Credential Vault section](#credential-vault-openbao) for the full execute request/result shapes.

---

### State Persistence Routing

The Agents Component delegates all state persistence to the Memory Component via the Orchestrator.

The Orchestrator must:
- Route `state.write` to the Memory Component and publish `state.write.ack` only **after durability is confirmed** — a false ack is a data loss scenario
- Process `state.read.request` by fetching records from the Memory Component and returning them on `aegis.agents.state.read.response`
- Enforce append-only semantics for `data_type: audit_log` — reject UPDATE/DELETE with `status: rejected, rejection_reason: AUDIT_IMMUTABILITY_VIOLATION`
- Enforce agent-scoped isolation: `state.read.request` returns records for the specified `agent_id` only
- Honour TTL semantics: `ttl_hint_seconds > 0` means the record is eligible for GC after expiry; `0` means never evict

---

### Orchestrator Requirements Checklist

| # | Requirement | SLA |
|---|-------------|-----|
| 1 | Publish `task.accepted` or `task.failed` for every well-formed `task.inbound` | Within 5s of receipt |
| 2 | Never publish the same `task_id` twice unless the first attempt returned `task.failed` or `task.result` | 100% invariant |
| 3 | Return `credential.response` on `aegis.agents.credential.response` | Within 2s p99 |
| 4 | Restrict `aegis.agents.credential.response` to Agents Component consumer group only | 100% invariant |
| 5 | Route `vault.execute.request` to Vault; relay result on `aegis.agents.vault.execute.result` | Within `timeout_seconds + overhead` |
| 6 | Restrict `aegis.agents.vault.execute.result` to Agents Component consumer group only | 100% invariant |
| 7 | Relay Vault progress events at-most-once; do not buffer or replay | 100% invariant |
| 8 | Reject any `vault.execute.result.operation_result` containing raw credential values | 100% invariant |
| 9 | Publish `state.write.ack` only after Memory Component confirms durability | 100% invariant |
| 10 | Echo `request_id` from `vault.execute.request` through to `vault.execute.result` | 100% invariant |
| 11 | Enforce audit_log immutability | 100% invariant |
| 12 | Enforce agent-scoped isolation on state reads | 100% invariant |
| 13 | Echo `user_context_id` from `task.inbound` in every outbound event for that task | 100% invariant |
| 14 | Route `clarification.request` to User I/O; return response within `timeout_seconds` | Per-request timeout |
| 15 | Persist user context in Memory **before** dispatching the task | Before `task.inbound` |

---

## Communications Component (NATS)

The Communications Component provides the NATS JetStream infrastructure. It has no awareness of message content — its responsibility is transport, access control, and durability.

### Required Streams

Two streams must exist before the Agents Component starts:

| Stream | Subject Filter | Retention | Purpose |
|--------|----------------|-----------|---------|
| `AEGIS_ORCHESTRATOR` | `aegis.orchestrator.>` | 24h (configurable) | Outbound messages from the Agents Component to the Orchestrator |
| `AEGIS_AGENTS` | `aegis.agents.>` | 24h (configurable) | Inbound messages to the Agents Component from the Orchestrator |

In local development, the simulator creates both streams automatically. In production, these must be provisioned before any component starts.

**Create streams manually:**

```bash
# ORCHESTRATOR stream (captures all Agents Component outbound messages)
nats stream add AEGIS_ORCHESTRATOR \
  --subjects "aegis.orchestrator.>" \
  --storage file \
  --replicas 3 \
  --max-age 24h \
  --server nats://localhost:4222

# AGENTS stream (captures all inbound messages to the Agents Component)
nats stream add AEGIS_AGENTS \
  --subjects "aegis.agents.>" \
  --storage file \
  --replicas 3 \
  --max-age 24h \
  --server nats://localhost:4222
```

### Access Control Requirements

| Requirement | Detail |
|-------------|--------|
| mTLS on all connections | Refuse connections without certificate signed by the Aegis internal CA |
| Publish/subscribe access by client identity | Agents Component credentials authorize publish on `aegis.orchestrator.*` and subscribe on `aegis.agents.*` only |
| `aegis.agents.credential.response` restricted | Only the Agents Component consumer group may subscribe — enforced at stream consumer level |
| `aegis.agents.vault.execute.result` restricted | Same restriction as `credential.response` |
| `aegis.orchestrator.audit.event` write-protected | Once written, audit events must not be modifiable or deletable by any component |
| AES-256 at rest | All stream storage encrypted; key management via OpenBao |
| Progress events at-most-once | `aegis.agents.vault.execute.progress` must never be redelivered; do not apply at-least-once guarantees to this subject |

### Durable Consumer Names

The Agents Component registers these durable consumers. Avoid collisions when adding new consumers:

| Consumer Name | Subject |
|---------------|---------|
| `agents-task-inbound` | `aegis.agents.task.inbound` |
| `agents-lifecycle-terminate` | `aegis.agents.lifecycle.terminate` |
| `agents-credential-response` | `aegis.agents.credential.response` |
| `agents-vault-execute-result` | `aegis.agents.vault.execute.result` |
| `agents-state-write-ack` | `aegis.agents.state.write.ack` |
| `agents-state-read-response` | `aegis.agents.state.read.response` |
| `agents-clarification-response` | `aegis.agents.clarification.response` |
| `agents-agent-spawn-response` | `aegis.agents.agent.spawn.response` |

The `AEGIS_COMMS_MAX_DELIVER` config variable (default 5) controls the JetStream redelivery budget. When exhausted, the message is dead-lettered to `aegis.orchestrator.error` with `message_type: dead.letter`.

---

## Credential Vault (OpenBao)

The Vault handles all credential lifecycle operations and executes credentialed external calls internally. The Agents Component never receives raw credential values at any point.

### Three-Phase Credential Model

```
Phase 1 — AUTHORIZE (at agent spawn)
  Agents → Orchestrator → Vault
  credential.request {operation: authorize, skill_domains[], ttl_seconds}
  ← credential.response {status: granted, permission_token, expires_at}

Phase 2 — EXECUTE (at runtime, per credentialed tool call)
  Agents → Orchestrator → Vault → [external system]
  vault.execute.request {request_id, permission_token, operation_type, operation_params, credential_type}
  ← vault.execute.result {request_id, status: success, operation_result: {external response}}

Phase 3 — REVOKE (at agent termination)
  Agents → Orchestrator → Vault
  credential.request {operation: revoke, permission_token}
```

**The critical distinction from the old model:** The Vault does not deliver credentials to agents. The Vault *executes the operation itself* using the stored credential, and returns only the result.

---

### Phase 1: Authorize

**credential.request payload (authorize):**

```json
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "task_id": "uuid",
  "operation": "authorize",
  "skill_domains": ["web", "data"],
  "ttl_seconds": 3900
}
```

`ttl_seconds` is the task timeout plus a 300-second buffer.

**credential.response payload (returned via Orchestrator):**

```json
{
  "request_id": "uuid",
  "status": "granted",
  "permission_token": "opaque-token-reference",
  "expires_at": "2026-01-01T01:05:00Z",
  "error_code": "",
  "error_message": ""
}
```

Status values: `granted`, `denied`, `error`. On `denied`, `error_message` must be human-interpretable but must **not** expose vault internals or secret paths.

**What the Vault must do:**
1. Create a scoped **service token** (not batch token — must be per-agent revocable) with policies derived from `skill_domains`: `aegis-agents-{domain}` per domain
2. Attach `agent_id`, `task_id`, `skill_domains[]` as token metadata
3. Enforce TTL: token lifetime must not exceed requested `ttl_seconds`
4. Return opaque `permission_token` reference — never a raw secret value

---

### Phase 2: Execute

**vault.execute.request payload (outbound from Agents Component):**

```json
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "task_id": "uuid",
  "permission_token": "opaque-token-reference",
  "operation_type": "web_fetch",
  "operation_params": {
    "url": "https://example.com",
    "method": "GET"
  },
  "timeout_seconds": 30,
  "credential_type": "web_api_key"
}
```

| Field | Notes |
|-------|-------|
| `request_id` | UUID; **idempotency key** — the Vault must deduplicate on this field |
| `permission_token` | Opaque reference from Phase 1 `authorize` — never a raw credential value |
| `operation_type` | Must match a registered operation in the Vault (e.g. `web_fetch`, `storage_read`) |
| `credential_type` | The type of credential to use; Vault resolves the correct secret internally |
| `timeout_seconds` | Hard execution deadline; range 1–300 |

**vault.execute.result payload (returned via Orchestrator):**

```json
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "status": "success",
  "operation_result": {
    "status_code": 200,
    "body": "..."
  },
  "error_code": "",
  "error_message": "",
  "elapsed_ms": 142
}
```

Status values: `success`, `timed_out`, `scope_violation`, `execution_error`.

`operation_result` contains the **external system's response only** — never the raw credential used to make the call.

**vault.execute.progress payload (optional, at-most-once):**

```json
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "progress_type": "heartbeat",
  "message": "Response headers received, reading body (42KB)",
  "elapsed_ms": 300
}
```

Progress events are informational only. Losing one is acceptable and must not affect correctness.

**What the Vault must do:**
1. Validate `permission_token` scope against `credential_type` and `operation_type` **before executing**
2. On scope violation: return `status: scope_violation` without executing and without accessing the secret
3. Retrieve the credential internally (it never leaves Vault storage)
4. Execute the operation against the external system using the credential
5. Enforce `timeout_seconds` as a hard deadline; return `status: timed_out` if exceeded
6. **Deduplicate on `request_id`**: if a request with this ID has been executed (or is in-progress), return the cached/in-progress result — do not execute again. This guarantees exactly-once execution semantics for agents recovering from crashes.
7. Emit progress events during long-running operations
8. Return `operation_result` containing only the external system's response

---

### Phase 3: Revoke

**credential.request payload (revoke):**

```json
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "task_id": "uuid",
  "operation": "revoke",
  "skill_domains": [],
  "ttl_seconds": 0
}
```

The Vault must revoke the token recursively (token plus all child leases). Revocation must succeed asynchronously — the Agents Component does not block on revoke.

---

### Required Vault Policies

The Vault team must provision these policies before any agent with the corresponding skill domain can be authorized:

| Policy | Secret Paths | Access | Notes |
|--------|-------------|--------|-------|
| `aegis-agents-web` | `aegis/{env}/web/*` | execute | Web/HTTP credentialed operations |
| `aegis-agents-data` | `aegis/{env}/data/*` | execute | Database query operations |
| `aegis-agents-comms` | `aegis/{env}/comms/*` | execute | Messaging service operations |
| `aegis-agents-storage` | `aegis/{env}/storage/*` | execute | Object/file storage operations |
| `aegis-agents-compute` | `aegis/{env}/compute/*` | execute | Cloud compute API operations |
| `aegis-agents-admin` | `aegis/{env}/admin/*` | execute | Reserved; only issued when `admin_access=true` in task |

All policies are **execute-only** from the Agents Component's perspective. The Vault uses the secret internally; the Agents Component never sees it.

---

### Vault Requirements Checklist

| # | Requirement |
|---|-------------|
| 1 | Use service token type (not batch tokens) — enables per-agent revocation |
| 2 | Derive policy set from `skill_domains[]` using `aegis-agents-{domain}` naming |
| 3 | Attach `agent_id`, `task_id`, `skill_domains[]` as token metadata |
| 4 | Enforce token lifetime ≤ requested `ttl_seconds` |
| 5 | Validate scope before executing — reject scope violations without accessing the secret |
| 6 | **Deduplicate on `request_id`** — critical for crash recovery correctness |
| 7 | Execute operations internally; return operation output only in `operation_result` |
| 8 | Enforce `timeout_seconds` hard deadline |
| 9 | Emit progress events during long-running operations |
| 10 | `operation_result` must never contain raw credential values, vault paths, or internal state |
| 11 | `error_message` must never expose credential internals or vault paths |
| 12 | Revoke token recursively (token + all child leases) on revoke request |

---

## Memory / Storage Component

The Memory Component persists all agent state on behalf of the Agents Component. It receives writes and read requests routed through the Orchestrator.

### State Write

**state.write payload (outbound from Agents Component):**

```json
{
  "agent_id": "uuid",
  "session_id": "uuid",
  "data_type": "snapshot",
  "ttl_hint_seconds": 0,
  "payload": { },
  "tags": {
    "context": "crash_snapshot"
  },
  "request_id": "uuid",
  "require_ack": true
}
```

**Data types:**

| `data_type` | Description | TTL |
|-------------|-------------|-----|
| `agent_state` | Current agent registry record | Long-lived |
| `task_result` | Final task output | 7 days |
| `episode` | Individual turn in the session log (user message, tool call, result, compaction) | Per policy |
| `snapshot` | Recovery checkpoint — must include `unknown_vault_request_ids` | Long-lived |
| `skill_cache` | Cached skill tree entries | Short-lived |
| `audit_log` | Security event — **append-only, immutable once written** | Permanent |
| `credential_event` | Credential authorize/revoke audit record | Long-lived |
| `capability_profile` | Per-agent skill capability record | Long-lived |
| `execution_pattern` | Successful task execution template | Long-lived |

**state.write.ack payload (returned via Orchestrator):**

```json
{
  "request_id": "uuid",
  "agent_id": "uuid",
  "status": "accepted",
  "rejection_reason": ""
}
```

Status: `accepted` or `rejected`. `rejection_reason` is set when rejected and must be logged — never silently discarded.

**Critical:** `state.write.ack` must only be published **after durability is confirmed** (replicated to at least one peer for snapshots). A false ack is a data loss scenario — crash recovery depends on reading the last acknowledged snapshot.

---

### State Read

**state.read.request payload (outbound from Agents Component):**

```json
{
  "agent_id": "uuid",
  "data_type": "snapshot",
  "context_tag": "crash_snapshot",
  "trace_id": "trace-uuid"
}
```

**state.read.response payload (returned via Orchestrator):**

```json
{
  "agent_id": "uuid",
  "records": [
    {
      "agent_id": "uuid",
      "session_id": "uuid",
      "data_type": "snapshot",
      "ttl_hint_seconds": 0,
      "payload": { },
      "tags": { }
    }
  ],
  "trace_id": "trace-uuid"
}
```

---

### Crash Snapshot Schema

The snapshot payload written at crash time includes `unknown_vault_request_ids` — this is the list of Vault operations that were in-flight when the agent crashed. The recovered agent reads this field and resubmits those requests using the same `request_id`. The Vault's deduplication guarantee ensures they execute exactly once.

```json
{
  "agent_id": "uuid",
  "task_id": "uuid",
  "failure_count": 1,
  "state": "active",
  "skill_domains": ["web"],
  "permission_set": ["web_fetch"],
  "unknown_vault_request_ids": ["vault-req-uuid-1", "vault-req-uuid-2"],
  "crashed_at": "2026-01-01T00:00:00Z"
}
```

---

### Memory Component Requirements Checklist

| # | Requirement |
|---|-------------|
| 1 | Publish `state.write.ack` only after durability confirmed — false ack = data loss |
| 2 | Validate `data_type` on every write; reject unknown types with `status: rejected` |
| 3 | Enforce append-only for `data_type: audit_log` — reject any UPDATE or DELETE |
| 4 | Enforce agent-scoped isolation on reads — return only records for the requested `agent_id` |
| 5 | For snapshots: replicate to at least one peer before publishing ack |
| 6 | Honour `ttl_hint_seconds`: eligible for GC after expiry; `0` = never evict |
| 7 | Encrypt all stored data at rest (AES-256); key management via OpenBao |
| 8 | Read-your-own-writes consistency required for snapshots (crash recovery depends on it) |
| 9 | Schema changes require joint written agreement and versioned zero-downtime migration plan |

---

## User I/O Component

The User I/O Component provides user context to agents and receives final task results. It also handles mid-task clarification requests.

### User Context Flow

Agents read user context by issuing a `state.read.request` filtered to `data_type: episode, context_tag: user_context`. This means **the User I/O Component must persist context before the Orchestrator dispatches the task** — agents cannot read non-existent records.

```
User I/O → Orchestrator → Memory Component
  state.write {data_type: episode, context_tag: user_context, payload: {structured JSON}}

Then:
Orchestrator → Agents Component
  task.inbound {task_id, user_context_id, ...}

Agent → Orchestrator → Memory Component
  state.read.request {agent_id, data_type: episode, context_tag: user_context}
  ← state.read.response {records: [{...user context...}]}
```

**Context payload requirements:**
- Structured JSON only — **no raw conversation transcripts**
- Maximum 500 tokens
- Raw transcripts cause context rot in the agent's context window

---

### Clarification Flow

When an agent needs user input to proceed, it publishes a `clarification.request`. The Orchestrator routes this to User I/O. If the user doesn't respond within `timeout_seconds`, the task fails with `CLARIFICATION_TIMEOUT`.

**clarification.request payload (outbound from Agents Component):**

```json
{
  "request_id": "uuid",
  "task_id": "uuid",
  "user_context_id": "ctx-uuid",
  "question": "Which format do you want the output in — JSON or Markdown?",
  "options": ["JSON", "Markdown"],
  "timeout_seconds": 120,
  "trace_id": "trace-uuid"
}
```

`options` is optional. If provided, the User I/O Component should render a selection widget. If omitted, the user types a free-form response.

**clarification.response payload (returned via Orchestrator):**

```json
{
  "request_id": "uuid",
  "user_context_id": "ctx-uuid",
  "response": "Markdown",
  "selected_option": "Markdown"
}
```

`selected_option` is present when `options[]` was provided. If the user types a free-form response, `selected_option` is null.

**User I/O must never send a silent non-response.** If the user doesn't respond within `timeout_seconds`, the Orchestrator must publish a `clarification.response` with `status: timed_out` — the Agents Component does not poll.

---

### Output Routing

Task results contain `user_context_id` echoed from the original `task.inbound`. The Orchestrator uses this to route `task.result` to the correct User I/O session. No additional routing information is provided by the Agents Component.

---

### User I/O Requirements Checklist

| # | Requirement |
|---|-------------|
| 1 | Persist user context in Memory **before** the Orchestrator dispatches the task |
| 2 | Context payload must be structured JSON only — no raw conversation transcripts |
| 3 | Context payload must not exceed 500 tokens |
| 4 | Render clarification questions (and optional selection widgets for `options[]`) |
| 5 | Return clarification response within the specified `timeout_seconds` |
| 6 | On user non-response: signal `timed_out` via Orchestrator — never send a silent non-response |
| 7 | Route `task.result` and `task.failed` to the correct session using `user_context_id` |
| 8 | Publish user feedback events within 60 seconds of interaction |

---

## Testing with the Partner Simulator

The repository ships with a **partner simulator** (`cmd/simulator`) that implements the Orchestrator side of the contract. It responds to credential requests, Vault execute requests, state writes, and state reads with synthetic responses — enabling the Agents Component to complete full task flows without any real partner services.

The simulator is included in Docker Compose (`docker-compose.yml`) and runs automatically with `docker compose up`.

### What the simulator implements

| Inbound subject | Synthetic response |
|----------------|--------------------|
| `aegis.orchestrator.credential.request` (authorize) | `credential.response {status: granted, permission_token: "sim-token-<agent_id>"}` |
| `aegis.orchestrator.credential.request` (revoke) | Acked; no response published |
| `aegis.orchestrator.vault.execute.request` | `vault.execute.result {status: success, operation_result: {"simulated": true}}` |
| `aegis.orchestrator.state.write` | `state.write.ack {status: accepted}` |
| `aegis.orchestrator.state.read.request` | `state.read.response {records: []}` |

Observation-only (logged, no reply): `task.accepted`, `task.result`, `task.failed`, `agent.status`, `audit.event`, `clarification.request`, `error`.

### Running the simulator standalone

```bash
# Start NATS
docker run --rm -p 4222:4222 nats:2.10-alpine --jetstream

# Start the simulator (builds from source)
AEGIS_NATS_URL=nats://localhost:4222 go run ./cmd/simulator/
```

### Publishing a test task

```bash
nats pub aegis.agents.task.inbound '{
  "message_id": "msg-001",
  "message_type": "task.inbound",
  "source_component": "orchestrator",
  "correlation_id": "task-001",
  "timestamp": "2026-01-01T00:00:00Z",
  "schema_version": "1.0",
  "payload": {
    "task_id": "task-001",
    "required_skills": ["web"],
    "instructions": "Fetch https://example.com and summarise the page.",
    "trace_id": "trace-001",
    "user_context_id": "ctx-001"
  }
}' --server nats://localhost:4222
```

### Observing responses

```bash
# Watch all Orchestrator-bound messages (everything the Agents Component sends)
nats sub "aegis.orchestrator.>" --server nats://localhost:4222

# Or subscribe to specific subjects:
nats sub aegis.orchestrator.task.accepted --server nats://localhost:4222
nats sub aegis.orchestrator.task.result --server nats://localhost:4222
nats sub aegis.orchestrator.agent.status --server nats://localhost:4222
nats sub aegis.orchestrator.audit.event --server nats://localhost:4222
```

### Integration tests

The integration test suite in `test/integration/` uses the same simulator as a Go library:

```bash
# Run all integration tests (requires NATS)
docker run --rm -d -p 4222:4222 nats:2.10-alpine --jetstream
AEGIS_NATS_URL=nats://localhost:4222 go test -count=1 ./test/integration/... -v
```

Tests cover: full provisioning path, idle agent reuse (fast path), crash detection and recovery, and capability queries.

---

## Security Invariants — Non-Negotiable

These invariants apply to every component in the system. Code that violates any of them must not be merged and must not reach production.

| # | Invariant |
|---|-----------|
| 1 | **Zero raw credential values outside the Vault.** No struct field, NATS message payload, log line, or error message in any component may contain a raw credential value (API key, password, token, secret of any kind). |
| 2 | **Orchestrator-only integration.** The Agents Component has no direct connection to Memory, Vault, User I/O, or any other component. Every outbound request routes: `Agents → Comms → Orchestrator`. |
| 3 | **`permission_token` is a reference, never a value.** The opaque `permission_token` returned from Phase 1 authorize is a Vault-internal reference. It does not contain or reveal the underlying secret. |
| 4 | **`operation_result` isolation.** The `operation_result` field in `vault.execute.result` carries external system output only. It must never appear in audit events and must never contain the raw credential used to produce it. |
| 5 | **`request_id` idempotency.** On agent recovery, in-flight Vault operations are resubmitted with the **original** `request_id`. The Vault deduplicates on this field to guarantee exactly-once execution. A new `request_id` would cause the operation to execute twice. |
| 6 | **Audit records are append-only.** Every audit event written is immutable. No component — including the Orchestrator — may UPDATE or DELETE an audit record. The Communications Component enforces this at the stream level. |
| 7 | **User context pre-persistence.** The User I/O Component must write user context to Memory before the Orchestrator dispatches the task. The Agents Component cannot read context that hasn't been written yet. |
| 8 | **`task.accepted` first.** Published within 5 seconds of receiving a well-formed `task.inbound`, before any provisioning work starts. The Orchestrator must not assume an agent is assigned until it receives `task.accepted`. |
| 9 | **`user_context_id` echoed everywhere.** Every event published by the Agents Component echoes `user_context_id` from the originating `task.inbound`. This is how the Orchestrator routes output to the correct User I/O session. |
| 10 | **No raw transcripts in Memory.** All state written to the Memory Component is structured JSON. Raw conversation transcripts cause context rot and are rejected. |
