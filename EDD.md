# Aegis OS — Orchestrator Component
## Engineering Design Document (EDD)

| Field | Value |
|---|---|
| Document ID | EDD-AEGIS-ORC-002 |
| Version | 2.0 |
| Status | Draft — For Review |
| Date | February 2026 |
| Component | Orchestrator Component |
| Parent System | Aegis OS |
| Owner | Junyu Ding |
| Classification | Internal / Design |

---

## Table of Contents

1. [Component Overview](#1-component-overview)
2. [Design Context and Principles](#2-design-context-and-principles)
3. [System Context](#3-system-context)
4. [Internal Architecture](#4-internal-architecture)
5. [Functional Requirements](#5-functional-requirements)
6. [Non-Functional Requirements](#6-non-functional-requirements)
7. [Primary Data Flows](#7-primary-data-flows)
8. [Sequence Diagrams](#8-sequence-diagrams)
9. [Task Lifecycle State Machine](#9-task-lifecycle-state-machine)
10. [Data Models](#10-data-models)
11. [Interface Specifications](#11-interface-specifications)
12. [Heartbeat & Health Monitoring](#12-heartbeat--health-monitoring)
13. [Security Design](#13-security-design)
14. [Error Handling & Resilience](#14-error-handling--resilience)
15. [Observability Design](#15-observability-design)
16. [Configuration & Environment Variables](#16-configuration--environment-variables)
17. [Proof of Concept (PoC)](#17-proof-of-concept-poc)
18. [Open Questions](#18-open-questions)
19. [Document Revision History](#19-document-revision-history)

---

# PART I — Component Overview & Design Principles

## 1. Component Overview

The Orchestrator is the central control plane of Aegis OS. It is the single point of authority for receiving user-originated tasks, enforcing security policy, coordinating agent provisioning through the Agents Component, and managing the full execution lifecycle of every task from receipt to result delivery.

The Orchestrator does **not** execute tasks itself. It is a **Policy Enforcement Point (PEP)**: every task that enters the system must pass through the Orchestrator's validation pipeline before any agent is activated, any credential is pre-authorized, or any result is delivered back to the user.

Three core concerns define the Orchestrator's responsibility:

- **Task Authority:** The Orchestrator is the exclusive entry point for all tasks from the User I/O Component. No agent can be provisioned without an Orchestrator-issued `task_spec`.
- **Policy Enforcement:** Before any task is dispatched, the Orchestrator validates it against Vault-held security policies and enforces permission boundaries. Tasks that fail policy checks are rejected — not silently dropped.
- **Lifecycle Coordination:** The Orchestrator tracks every active task, monitors agent health via the Agents Component, and drives recovery when agents fail. It is the authority on whether a task is RUNNING, RECOVERING, COMPLETED, or FAILED.

> **ℹ NOTE:** The Orchestrator is a stateless coordinator. All task state and agent state is persisted via the Memory Component. This enables horizontal scaling, crash recovery, and node migration without data loss.

---

## 2. Design Context and Principles

### 2.1 Relationship to the Agents Component

The Orchestrator and Agents Component have a strict principal-agent relationship. The Orchestrator is the **principal**: it defines tasks, establishes security scope, and expects results. The Agents Component is the **executor**: it builds agents, manages their lifecycle, and returns outcomes. The Orchestrator never manages microVMs directly — that is the Agents Component's exclusive concern.

Communication between the Orchestrator and Agents Component is **exclusively via NATS JetStream** through the Communications Component. No direct calls.

### 2.2 Policy-First Design

Every task dispatched by the Orchestrator carries a validated policy scope. This scope is derived from the user's configured permissions, the task's required skill domains, and the current Vault policy set. An agent cannot request credentials outside its scope because the scope was established before the agent was created.

> **🔴 CRITICAL:** Policy enforcement is not advisory. A task that cannot be scoped safely is returned as `POLICY_VIOLATION` to the User I/O Component with a human-readable explanation. It is never silently dropped or partially executed.

### 2.3 Stateless Component Design

The Orchestrator is stateless by design. Any state it needs persisted is immediately written to the Memory Component. This enables horizontal scaling and crash recovery. On restart, the Orchestrator rehydrates its active task list from the Memory Component and resumes monitoring.

### 2.4 Idempotency and At-Least-Once Safety

All inbound task messages carry a `task_id`. The Orchestrator maintains a deduplication window (default 300 seconds) to prevent duplicate execution. Receiving the same `task_id` twice within the deduplication window is a no-op: the Orchestrator returns the status of the existing task without spawning a second agent.

---

# PART II — Architecture

## 3. System Context

### 3.1 Context Diagram (Described)

The Orchestrator sits at the center of the Aegis OS control plane, interfacing with all five other components:

| External Component | Direction | Protocol | Data Exchanged |
|---|---|---|---|
| User I/O Component | Bidirectional | NATS / Comms Interface | Inbound: `user_task`. Outbound: `task_result`, `task_status_update`, `clarification_request`, `error_response` |
| Agents Component | Bidirectional | NATS / Comms Interface | Outbound: `task_spec`, `capability_query`, `agent_terminate`. Inbound: `task_result`, `agent_status_update`, `capability_response` |
| Vault (OpenBao) | Bidirectional | OpenBao HTTP API | Outbound: `policy_validation_request`, `token_revoke`. Inbound: `policy_result`, `scoped_token` |
| Memory Component | Bidirectional | Memory Interface abstraction | Outbound: tagged task state writes, audit events. Inbound: task state reads for recovery and deduplication |
| Communications (NATS) | Bidirectional | NATS JetStream | All inter-component messages routed through NATS streams. Orchestrator publishes and subscribes via defined topic hierarchy. |

---

## 4. Internal Architecture

The Orchestrator consists of **six internal modules**. Each module has a single, well-defined responsibility. Modules communicate through defined internal interfaces, never directly manipulating each other's data.

### 4.1 Module Inventory

#### M1: Communications Gateway

| | |
|---|---|
| **Responsibilities** | Single inbound/outbound gateway for all NATS messaging. Receives `user_task` from User I/O Component. Routes outbound messages (results, status, errors) to User I/O and Agents Component. Enforces message envelope validation — rejects malformed messages before they enter the pipeline. Manages NATS consumer ACK/NAK and dead-letter queue monitoring. |
| **Inputs** | `user_task` from User I/O (via NATS); `task_result` and `agent_status_update` from Agents Component (via NATS); internal messages from Task Dispatcher |
| **Outputs** | Parsed `user_task` to Task Dispatcher; routed responses to User I/O and Agents Component |
| **Interfaces With** | Task Dispatcher (internal), NATS / Communications Component (external) |

#### M2: Task Dispatcher

| | |
|---|---|
| **Responsibilities** | Central coordinator for all incoming task routing decisions. Validates `task_spec` schema completeness; rejects invalid specs immediately. Performs deduplication check via Memory Component using `task_id`. Routes to Policy Enforcer for permission validation before any agent interaction. Persists `DISPATCH_PENDING` before publishing to Agents Component, then persists `DISPATCHED` after successful publish. Tracks active tasks and correlates incoming results to the originating `user_task`. |
| **Inputs** | Parsed `user_task` from Communications Gateway; `policy_result` from Policy Enforcer; `task_result`/`agent_status_update` from Comms Gateway; `dedup_result` from Memory Interface |
| **Outputs** | `policy_check_request` to Policy Enforcer; `task_spec` to Communications Gateway (for Agents Component); `task_accepted`/`task_failed`/`policy_violation` to Communications Gateway (for User I/O) |
| **Interfaces With** | Communications Gateway, Policy Enforcer, Memory Interface, Recovery Manager |

#### M3: Policy Enforcer

| | |
|---|---|
| **Responsibilities** | Validates every task against the current Vault policy set before dispatch. Queries OpenBao to confirm the user's permission scope covers the task's `required_skill_domains`. Derives and attaches a `policy_scope` to the validated `task_spec` — this scope is the ceiling for all agent credential requests. Rejects tasks requesting skills outside the user's configured permission set. Logs every policy decision (ALLOW/DENY) as a structured audit event. On Vault unavailability: applies configurable fail-open or fail-closed behavior (default: fail-closed). |
| **Inputs** | `policy_check_request` from Task Dispatcher (`task_id`, `user_id`, `required_skill_domains[]`); `policy_result` from OpenBao Vault |
| **Outputs** | `policy_result` to Task Dispatcher (ALLOWED + `policy_scope`, or DENIED + reason); `audit_event` to Memory Interface (every decision) |
| **Interfaces With** | Task Dispatcher, Vault (OpenBao), Memory Interface |

#### M4: Task Monitor

| | |
|---|---|
| **Responsibilities** | Tracks every active task from dispatch to completion or failure. Maintains an in-memory task state map (rehydrated from Memory Component on startup). Enforces task-level timeout: if a task exceeds `timeout_seconds`, signals Recovery Manager to terminate the agent and return `TIMED_OUT`. Subscribes to `agent_status_update` events from the Agents Component. Detects RECOVERING/TERMINATED events and escalates to Recovery Manager. Emits `task_progress` events when agents publish intermediate progress. Transitions RECOVERING tasks into a monitored waiting state; timeout remains the safety net if self-recovery does not complete. |
| **Inputs** | Task dispatch confirmation from Task Dispatcher; `agent_status_update` from Comms Gateway; `timeout_tick` from internal scheduler |
| **Outputs** | `timeout_signal` to Recovery Manager; `task_progress` to Communications Gateway; `state_write` to Memory Interface (on every state change) |
| **Interfaces With** | Task Dispatcher, Communications Gateway, Recovery Manager, Memory Interface |

#### M5: Recovery Manager

| | |
|---|---|
| **Responsibilities** | Responds to all non-nominal task events: agent failure, timeout, policy violation, Vault/Memory unavailability. On agent failure: determines recovery strategy based on failure count and failure type. If an agent is in `RECOVERING`, the Recovery Manager does not immediately re-dispatch; it trusts the existing agent to self-heal and relies on timeout or a later `TERMINATED` event as the safety net. If an agent is `TERMINATED`, coordinates with Memory Component to retrieve last valid task state before recovery attempt. Issues `agent_terminate` or `task_cancel` instructions to Communications Gateway. Escalates irrecoverable failures as `task_failed` messages. Manages retry budget: tracks per-task retry count; does not exceed configurable `max_retries`. Ensures credential revocation is triggered on Vault for any terminated agent. |
| **Inputs** | `timeout_signal` from Task Monitor; `agent_status_update` (RECOVERING/TERMINATED) from Comms Gateway; `component_failure` event from Memory Interface or Policy Enforcer |
| **Outputs** | `agent_terminate`/`task_cancel` to Comms Gateway; `task_failed` to Comms Gateway (for User I/O); `revoke_credentials` to Policy Enforcer; `recovery_audit_event` to Memory Interface |
| **Interfaces With** | Task Monitor, Communications Gateway, Policy Enforcer, Memory Interface |

#### M6: Memory Interface

| | |
|---|---|
| **Responsibilities** | Disciplined persistence gateway — the only module that writes to or reads from the Memory Component. Enforces structured, tagged write payloads: untagged writes are rejected with an error. Serves task state read requests for deduplication, recovery, and on-startup rehydration. Never accepts raw session transcripts — only structured, extracted state is persisted. Manages write retries (up to 3 attempts with exponential backoff) and escalates to Recovery Manager on persistent failure. |
| **Inputs** | Tagged write payloads from: Task Dispatcher, Policy Enforcer, Task Monitor, Recovery Manager; read requests from: Task Dispatcher (dedup), Recovery Manager (state restore), Task Monitor (startup rehydration) |
| **Outputs** | Persisted state to Memory Component via the Memory Component API/adapter; retrieved state slices to requesting modules; `write_failure` event to Recovery Manager |
| **Interfaces With** | Task Dispatcher, Policy Enforcer, Task Monitor, Recovery Manager, Memory Component |

---

# PART III — Functional & Non-Functional Requirements

## 5. Functional Requirements

### 5.1 Task Reception & Routing

| ID | Requirement | Priority |
|---|---|---|
| FR-TRK-01 | The Orchestrator SHALL be the exclusive entry point for all tasks from the User I/O Component. No task may reach the Agents Component without passing through the Orchestrator's validation pipeline. | MUST |
| FR-TRK-02 | On receiving a `user_task`, the Communications Gateway SHALL validate the message envelope schema before routing. Malformed messages SHALL be rejected with a structured error; they SHALL NOT enter the dispatch pipeline. | MUST |
| FR-TRK-03 | The Task Dispatcher SHALL perform a deduplication check against the Memory Component using `task_id` before initiating any processing. If the `task_id` is found within the deduplication window, the Orchestrator SHALL return the current task status without creating a new execution. | MUST |
| FR-TRK-04 | The Task Dispatcher SHALL reject any `user_task` where `required_skill_domains[]` is empty or absent, returning `INVALID_TASK_SPEC` to the caller. | MUST |
| FR-TRK-05 | Every task SHALL be assigned a unique `orchestrator_task_ref` that is distinct from the user-provided `task_id`. This internal reference is used for all Orchestrator-to-Agents communication. | MUST |
| FR-TRK-06 | The Task Dispatcher SHOULD track task queue depth and emit a `queue_pressure` metric when the pending queue exceeds a configurable high-water mark. | SHOULD |

### 5.2 Policy Enforcement

| ID | Requirement | Priority |
|---|---|---|
| FR-PE-01 | Every task SHALL be validated against the Vault (OpenBao) policy set before any agent is provisioned or activated. Tasks that fail policy validation SHALL be returned as `POLICY_VIOLATION` with a human-readable reason. | MUST |
| FR-PE-02 | The Policy Enforcer SHALL derive a `policy_scope` from the Vault response. This scope defines the ceiling for all credential requests during task execution and SHALL be attached to the `task_spec` sent to the Agents Component. | MUST |
| FR-PE-03 | Every policy decision — ALLOW or DENY — SHALL be written to the Memory Component as an `audit_event` with: `user_id`, `task_id`, `required_skill_domains[]`, outcome, timestamp, and `vault_policy_version`. | MUST |
| FR-PE-04 | If the Vault is unreachable and `vault_failure_mode` is `FAIL_CLOSED` (default), the Policy Enforcer SHALL deny the task and return `VAULT_UNAVAILABLE`. If `FAIL_OPEN`, it SHALL proceed with a cached policy (if within `cache_ttl_seconds`) or deny if no cache exists. | MUST |
| FR-PE-05 | The Policy Enforcer SHALL maintain a policy cache with a configurable TTL. Cached policies SHALL be invalidated on any Vault policy update event received via NATS. | SHOULD |

### 5.3 Agent Lifecycle Coordination

| ID | Requirement | Priority |
|---|---|---|
| FR-ALC-01 | After successful policy validation, the Task Dispatcher SHALL query the Agents Component's capability endpoint to determine if a capable agent exists, before requesting provisioning. | MUST |
| FR-ALC-02 | The Orchestrator SHALL NOT dictate how the Agents Component provisions agents. It SHALL only send a fully-formed `task_spec` (including `policy_scope`) and wait for a `task_accepted` or `provisioning_error` response. | MUST |
| FR-ALC-03 | The Orchestrator SHALL confirm `task_accepted` to the User I/O Component within **5 seconds** (existing agent path) or within **30 seconds** (new agent path). Confirmed tasks receive an `estimated_completion_at` field. | MUST |
| FR-ALC-04 | The Orchestrator SHALL respond to `capability_query` from the Agents Component on the `aegis.orchestrator.capability.query` topic within **500ms p99**. | MUST |
| FR-ALC-05 | The Orchestrator SHALL forward intermediate agent progress updates to the User I/O Component if a `user_context_id` is associated with the task. | SHOULD |

### 5.4 Self-Healing & Recovery

| ID | Requirement | Priority |
|---|---|---|
| FR-SH-01 | The Task Monitor SHALL enforce a per-task timeout. When `timeout_seconds` is exceeded, the Task Monitor SHALL signal the Recovery Manager to terminate the running agent and return `TIMED_OUT` to the User I/O Component. | MUST |
| FR-SH-02 | On receiving an `agent_status_update` with `state=RECOVERING`, the Recovery Manager SHALL retrieve the last persisted task state from the Memory Component and re-submit the task context to the Agents Component for agent respawn. | MUST |
| FR-SH-03 | The Recovery Manager SHALL enforce a configurable `max_task_retries` (default: 3). If retries are exhausted, the task SHALL be marked `FAILED` and the User I/O Component SHALL be notified with `error_code=MAX_RETRIES_EXCEEDED`. | MUST |
| FR-SH-04 | On any terminal task outcome (COMPLETED, FAILED, TIMED_OUT, POLICY_VIOLATION), the Recovery Manager SHALL trigger credential revocation via the Policy Enforcer for the associated agent. | MUST |
| FR-SH-05 | On Orchestrator startup, the Task Monitor SHALL rehydrate its active task map from the Memory Component and resume monitoring any tasks that were RUNNING or RECOVERING at the time of shutdown. | MUST |
| FR-SH-06 | Recovery decisions SHALL be policy-checked with the Vault before re-dispatching. A re-dispatched task cannot receive a broader scope than the original `policy_scope`. | MUST |

### 5.5 Observability & Audit

| ID | Requirement | Priority |
|---|---|---|
| FR-OBS-01 | Every state transition of every task SHALL produce a structured audit event written to the Memory Component via Memory Interface. Events SHALL include: `orchestrator_task_ref`, `task_id`, `user_context_id` (if any), `state`, `previous_state`, `timestamp`, `node_id`, `initiating_module`. | MUST |
| FR-OBS-02 | The Orchestrator SHALL expose a `/health` endpoint returning: component status, active task count, queue depth, Memory Component reachability, Vault reachability. | MUST |
| FR-OBS-03 | The Orchestrator SHALL emit structured metrics to the `aegis.orchestrator.metrics` NATS subject at a configurable interval (default: 15 seconds). | MUST |
| FR-OBS-04 | Audit log records SHALL be append-only. The Memory Interface must reject UPDATE or DELETE operations on orchestrator audit records. | MUST |
| FR-OBS-05 | The Orchestrator SHOULD produce distributed trace spans (compatible with OpenTelemetry) for every task. | SHOULD |

---

## 6. Non-Functional Requirements

| ID | Category | Requirement | Target |
|---|---|---|---|
| NFR-01 | Performance | Task receipt to `task_accepted` (existing agent path) | < 5 seconds |
| NFR-02 | Performance | Task receipt to `task_accepted` (new agent, full provisioning) | < 35 seconds |
| NFR-03 | Performance | Policy validation round-trip (Vault available) | < 200ms p99 |
| NFR-04 | Performance | Capability query response to Agents Component | < 500ms p99 |
| NFR-05 | Reliability | Task state must survive Orchestrator node failure without data loss | Zero data loss |
| NFR-06 | Reliability | Startup rehydration from Memory must complete before accepting new tasks | < 10 seconds |
| NFR-07 | Reliability | Recovery success rate for transient agent failures | > 95% |
| NFR-08 | Security | All policy decisions logged with 100% coverage | 100% audit coverage |
| NFR-09 | Security | Vault unreachable: default behavior | FAIL_CLOSED (no task dispatched) |
| NFR-10 | Scalability | Concurrent tasks managed without race conditions | 1 to 10,000+ tasks |
| NFR-11 | Scalability | Orchestrator horizontal scale-out without coordination protocol | Stateless; N instances |
| NFR-12 | Observability | All task state transitions produce structured audit events | 100% coverage |

---

# PART IV — Data Flows & Sequence Diagrams

## 7. Primary Data Flows

### Flow 1: Task Reception and Schema Validation
1. User I/O Component publishes a `user_task` to `aegis.orchestrator.tasks.inbound` over NATS.
2. `user_task` includes: `task_id`, `user_id`, `required_skill_domains[]`, `priority`, `timeout_seconds`, `payload`, `callback_topic`, `user_context_id` (optional).
3. Communications Gateway validates the message envelope schema. Malformed messages return a structured error to `aegis.orchestrator.errors` without entering the pipeline.
4. Communications Gateway dispatches the validated `user_task` to Task Dispatcher.

### Flow 2: Deduplication Check
1. Task Dispatcher queries Memory Interface: `read({ data_type: 'task_state', filter: { task_id } })`.
2. If a record is found within `idempotency_window_seconds`, Task Dispatcher returns the current task status — no new execution is initiated.
3. If no record is found, Task Dispatcher proceeds to policy validation.

### Flow 3: Policy Validation
1. Task Dispatcher sends a `policy_check_request` to Policy Enforcer: `{ task_id, user_id, required_skill_domains[] }`.
2. Policy Enforcer queries OpenBao: `POST /v1/auth/token/create` with policies derived from `required_skill_domains[]`.
3. OpenBao returns: `ALLOW + effective_scope`, or `DENY + reason`.
4. Policy Enforcer writes an `audit_event` to Memory Interface **regardless of outcome**.
5. On ALLOW: Policy Enforcer returns `policy_result (ALLOWED, policy_scope)` to Task Dispatcher. `task_spec` is assembled with `policy_scope` attached.
6. On DENY: Policy Enforcer returns `policy_result (DENIED, reason)`. Task Dispatcher returns `POLICY_VIOLATION` to User I/O. **No agent is touched.**

### Flow 4: Agent Capability Query & Dispatch
1. Task Dispatcher sends `capability_query` to Agents Component (via NATS topic `aegis.agents.capability.query`).
2. Agents Component returns `capability_response`: `match | partial_match | no_match`.
3. Task Dispatcher assembles final `task_spec` and publishes to `aegis.agents.tasks.inbound`.
4. Agents Component responds with `task_accepted (agent_id, estimated_completion)` or `provisioning_error`.
5. Task Dispatcher persists task state (`DISPATCH_PENDING`) to Memory Interface **before** publishing `task_spec`. After successful publish, it persists `DISPATCHED` and confirms `task_accepted` to User I/O Component. If publish fails, it persists `DELIVERY_FAILED`.

### Flow 5: Task Monitoring and Completion
1. Task Monitor subscribes to `agent_status_update` events on `aegis.agents.status.events`.
2. As agent progresses, intermediate updates are forwarded to User I/O Component if `user_context_id` is present.
3. On `task_result` from Agents Component: Task Dispatcher writes COMPLETED state to Memory, delivers result to User I/O via `callback_topic`. Recovery Manager triggers credential revocation.
4. On timeout: Task Monitor signals Recovery Manager → `agent_terminate` issued → TIMED_OUT returned to User I/O.

### Flow 6: Recovery Flow
1. Agent RECOVERING event received from Agents Component.
2. Recovery Manager treats `RECOVERING` as a self-healing signal and does not immediately re-dispatch. The existing agent may return to `ACTIVE`; otherwise timeout remains the safety net.
3. If the agent later reports `TERMINATED`, Recovery Manager checks retry count. If count < `max_task_retries`:
   - Memory Interface read for last task state snapshot.
   - Vault checked (policy still valid for recovery scope).
   - Updated `task_spec` re-dispatched to Agents Component (same `policy_scope`).
4. If count >= `max_task_retries`: task marked FAILED, User I/O notified, credentials revoked.

---

## 8. Sequence Diagrams

### 8.1 New Task — Full Provisioning Path

*Participants: User I/O → Comms Gateway → Task Dispatcher → Policy Enforcer → OpenBao → Agents Component → Memory Interface → User I/O*

1. User I/O → Comms Gateway: `user_task` (`aegis.orchestrator.tasks.inbound`)
2. Comms Gateway validates envelope → Task Dispatcher: parsed `user_task`
3. Task Dispatcher → Memory Interface: read dedup check (`task_id`)
4. Memory Interface → Task Dispatcher: `NOT_FOUND` (proceed)
5. Task Dispatcher → Policy Enforcer: `policy_check_request {task_id, user_id, required_skill_domains[]}`
6. Policy Enforcer → OpenBao (HTTP): `POST /v1/auth/token/create {policies: ['aegis-agents-web', ...]}`
7. OpenBao → Policy Enforcer: `{allowed: true, policy_scope: {...}, token_accessor: '...'}`
8. Policy Enforcer → Memory Interface: write `audit_event {ALLOW, task_id, user_id, timestamp}`
9. Policy Enforcer → Task Dispatcher: `policy_result {ALLOWED, policy_scope}`
10. Task Dispatcher → Agents Component: `capability_query` (`aegis.agents.capability.query`)
11. Agents Component → Task Dispatcher: `capability_response {no_match, provisioning_estimate: 28s}`
12. Task Dispatcher → Memory Interface: write `task_state {DISPATCH_PENDING, task_id, timestamp}`
13. Task Dispatcher → Agents Component: `task_spec` (`aegis.agents.tasks.inbound`) with `policy_scope` attached
14. Task Dispatcher → Memory Interface: write `task_state {DISPATCHED, task_id, timestamp}`
15. Task Dispatcher → User I/O: `task_accepted {task_id, agent_id, estimated_completion_at}`
16. *(agent provisions, executes, returns result)*
17. Agents Component → Comms Gateway: `task_result` (callback_topic)
18. Task Dispatcher → Memory Interface: write `task_state {COMPLETED}`
19. Task Dispatcher → Policy Enforcer: trigger credential revocation for `agent_id`
20. Policy Enforcer → OpenBao (HTTP): `POST /v1/auth/token/revoke`
21. Comms Gateway → User I/O: `task_result` delivered to `callback_topic`

### 8.2 Task — Existing Agent (Fast Path)

Steps 1–9 identical to 8.1 (schema validation, dedup, policy check).

10. Task Dispatcher → Agents Component: `capability_query`
11. Agents Component → Task Dispatcher: `capability_response {match, agent_id: A3, state: IDLE}`
12. Task Dispatcher → Memory Interface: write `task_state {DISPATCH_PENDING}`
13. Task Dispatcher → Agents Component: `task_spec` (same agent activated, no new provisioning)
14. Task Dispatcher → Memory Interface: write `task_state {DISPATCHED}`
15. Task Dispatcher → User I/O: `task_accepted {task_id, agent_id: A3, estimated_completion_at}`
16. Agents Component → Comms Gateway: `task_result`
17. Task Dispatcher → Memory Interface: write `task_state {COMPLETED}`; trigger credential revocation
18. Comms Gateway → User I/O: `task_result` delivered

### 8.3 Self-Healing Recovery Flow

*Triggered when: `agent_status_update {state: RECOVERING}` or later `agent_status_update {state: TERMINATED}` is received from Agents Component*

1. Task Monitor receives `agent_status_update {agent_id, state: RECOVERING, task_id}`
2. Task Monitor → Recovery Manager: `recovery_signal {task_id, retry_count, policy_scope, reason: AGENT_RECOVERING}`
3. Recovery Manager logs the self-healing condition and does not immediately re-dispatch.
4. If the agent later reports `TERMINATED`, Task Monitor → Recovery Manager: `recovery_signal {task_id, retry_count, policy_scope, reason: AGENT_TERMINATED}`
5. Recovery Manager checks `retry_count`: if < `max_task_retries` → proceed; else → FAIL
6. Recovery Manager → Memory Interface: read `task_state` snapshot `{task_id, latest}`
7. Memory Interface → Recovery Manager: `task_state {progress_summary, policy_scope, original_task_spec}`
8. Recovery Manager → Policy Enforcer: re-validate policy scope `{task_id, policy_scope}`
9. Policy Enforcer → OpenBao: verify scope still valid
10. OpenBao → Policy Enforcer: VALID (or EXPIRED → Recovery Manager escalates to FAIL)
11. Recovery Manager → Comms Gateway: re-dispatch `task_spec` to `aegis.agents.tasks.inbound`
12. Recovery Manager → Memory Interface: write `recovery_event {attempt_n, timestamp}`
13. Agents Component responds with `task_accepted` — monitoring resumes at Task Monitor

> **🔴 CRITICAL:** On `max_retries` exceeded: Recovery Manager issues `agent_terminate`, writes FAILED state, triggers credential revocation, and returns `task_failed {error_code: MAX_RETRIES_EXCEEDED}` to User I/O.

### 8.4 Policy Violation Flow

*Triggered when: Policy Enforcer receives DENY from OpenBao*

1. Task Dispatcher → Policy Enforcer: `policy_check_request`
2. Policy Enforcer → OpenBao: token create request
3. OpenBao → Policy Enforcer: `{allowed: false, reason: 'domain storage not in user policy'}`
4. Policy Enforcer → Memory Interface: write `audit_event {DENY, task_id, user_id, reason, timestamp}`
5. Policy Enforcer → Task Dispatcher: `policy_result {DENIED, reason}`
6. Task Dispatcher → Comms Gateway: `policy_violation_response {task_id, error_code: POLICY_VIOLATION, user_message: '...'}`
7. Comms Gateway → User I/O: `policy_violation` delivered to `callback_topic`

**No agent is provisioned. No credential is issued. The `policy_scope` remains at zero-trust.**

---

# PART V — Data Models & State Machine

## 9. Task Lifecycle State Machine

The Task Monitor is the **sole authority** for state transitions.

| State | Description | Entry Condition | Valid Transitions |
|---|---|---|---|
| RECEIVED | Task arrived, schema validated | `user_task` passes envelope validation | → DEDUP_CHECK → POLICY_CHECK → REJECTED (schema fail) |
| POLICY_CHECK | Awaiting Vault policy validation | Passed dedup check; `policy_check_request` sent | → DISPATCH_PENDING (ALLOW) → POLICY_VIOLATION (DENY) |
| DISPATCH_PENDING | Task state persisted before agent publish | `policy_result` ALLOWED; pre-dispatch persistence succeeded | → DISPATCHED → DELIVERY_FAILED |
| DISPATCHED | `task_spec` successfully published to Agents Component; `task_accepted` confirmed | Agent publish succeeded | → RUNNING → RECOVERING → TIMED_OUT |
| RUNNING | Agent actively executing the task | Agent ACTIVE confirmed by Agents Component | → COMPLETED → RECOVERING → TIMED_OUT |
| RECOVERING | Agent self-healing; Orchestrator monitoring without immediate re-dispatch | `agent_status_update` RECOVERING received | → RUNNING (agent recovers) → FAILED (later TERMINATED / max retries) → TIMED_OUT |
| DELIVERY_FAILED | Task could not be published to an agent after pre-dispatch persistence | `task_spec` publish failed | **Terminal.** Credentials revoked; cleanup performed. |
| TIMED_OUT | Task exceeded `timeout_seconds` | Task Monitor `timeout_signal` fires | **Terminal.** Agent terminated; credentials revoked. |
| POLICY_VIOLATION | Task denied by policy validation | `policy_result` DENIED from Policy Enforcer | **Terminal.** No agent touched. |
| FAILED | Irrecoverable task failure | Max retries exceeded or provisioning failure | **Terminal.** Credentials revoked; audit written. |
| COMPLETED | Task result received and delivered to User I/O | `task_result` received from Agents Component | **Terminal.** Credentials revoked; result persisted. |

---

## 10. Data Models

### 10.1 Task State Record Schema

Stored in the Memory Component via Memory Interface. One record per task, updated on every state transition.

| Field | Type | Description |
|---|---|---|
| `orchestrator_task_ref` | UUID (string) | Orchestrator-internal reference. Distinct from user-provided `task_id`. |
| `task_id` | UUID (string) | User-provided task identifier. Used for deduplication and correlation. |
| `user_id` | string | Identifier of the user or session that submitted the task. |
| `state` | enum | `RECEIVED \| POLICY_CHECK \| DISPATCH_PENDING \| DISPATCHED \| RUNNING \| RECOVERING \| COMPLETED \| FAILED \| DELIVERY_FAILED \| TIMED_OUT \| POLICY_VIOLATION` |
| `required_skill_domains` | string[] | Skill domains declared by the task. Validated against Vault policy. |
| `policy_scope` | object | Effective permission scope derived from Vault. Attached to `task_spec`. Never expanded during execution. |
| `agent_id` | UUID \| null | Assigned agent ID from Agents Component. Null until DISPATCHED. |
| `retry_count` | integer | Number of recovery attempts for this task. |
| `dispatched_at` | ISO 8601 | Timestamp when `task_spec` was sent to Agents Component. |
| `timeout_at` | ISO 8601 | Computed as `dispatched_at + timeout_seconds`. Task Monitor checks this field on every tick. |
| `completed_at` | ISO 8601 \| null | Timestamp of terminal outcome. Null while task is in progress. |
| `error_code` | string \| null | Set on any non-COMPLETED terminal state. E.g., `POLICY_VIOLATION`, `MAX_RETRIES_EXCEEDED`, `TIMED_OUT`. |
| `state_history` | StateEvent[] | Ordered list of `{state, timestamp, reason, node_id}`. Append-only. |

### 10.2 User Task Schema (Inbound from User I/O)

| Field | Type | Required | Description / Constraints |
|---|---|---|---|
| `task_id` | UUID string | YES | Globally unique. Deduplication key. |
| `user_id` | string | YES | User or session identifier. Used for policy lookup. |
| `required_skill_domains` | string[] | YES | Min 1 item. Known domain names only. Unknown domains return `INVALID_TASK_SPEC`. |
| `priority` | integer | YES | 1 (lowest) to 10 (highest). Used to prioritize dispatch queue. |
| `timeout_seconds` | integer | YES | Min 30, max 86400. Orchestrator enforces hard cutoff at this value. |
| `payload` | object | YES | Task-specific data. Opaque to Orchestrator. Max 1MB serialized. Passed verbatim to Agents Component. |
| `callback_topic` | string | YES | Valid NATS topic. All results for this task published here. |
| `user_context_id` | UUID \| null | NO | Optional user session reference. Enables progress streaming and clarification requests. |
| `idempotency_window_seconds` | integer | NO | Default 300. How long to remember this `task_id` for deduplication. |

### 10.3 Orchestrator Audit Log Schema

Stored in Memory Component, **append-only**. Every state transition and policy decision writes one record.

| Field | Type | Description |
|---|---|---|
| `log_id` | UUID | Unique log entry identifier. |
| `orchestrator_task_ref` | UUID | Associated task reference. |
| `event_type` | enum | `task_received \| policy_allow \| policy_deny \| task_dispatched \| task_completed \| task_failed \| recovery_attempt \| credential_revoked \| vault_unavailable \| component_failure` |
| `initiating_module` | string | `CommunicationsGateway \| TaskDispatcher \| PolicyEnforcer \| TaskMonitor \| RecoveryManager \| MemoryInterface` |
| `outcome` | enum | `success \| denied \| failed \| partial \| recovered` |
| `event_detail` | JSON | Structured detail. No raw text, no PII, no credential values. |
| `timestamp` | ISO 8601 | Event time. Immutable once written. |
| `node_id` | string | Aegis OS cluster node ID where event was generated. |

---

# PART VI — Interfaces & Security

## 11. Interface Specifications

### 11.1 Inbound: User I/O Component → Orchestrator

- **Topic:** `aegis.orchestrator.tasks.inbound`
- **Message format:** JSON. Schema: UserTask (see §10.2).
- **Delivery:** At-least-once. Consumer must ACK after successful validation.
- **Error handling:** Invalid envelope → `aegis.orchestrator.errors {task_id, error_code, message}`. No partial execution.
- **Dead-letter:** Unacknowledged after `max_redelivery` (default: 5) → `aegis.orchestrator.tasks.deadletter`.

### 11.2 Outbound: Orchestrator → Agents Component

| Message Type | NATS Topic | Trigger |
|---|---|---|
| `task_spec` | `aegis.agents.tasks.inbound` | Policy validated; dispatching to Agents Component |
| `capability_query` | `aegis.agents.capability.query` | Before every task dispatch |
| `agent_terminate` | `aegis.agents.lifecycle.terminate` | Timeout exceeded or max retries reached |
| `task_cancel` | `aegis.agents.tasks.cancel` | Policy violation or irrecoverable failure |

### 11.3 Outbound: Policy Enforcer → Vault (OpenBao)

**Pre-authorization call:** `POST /v1/auth/token/create`
- Policies: derived from `required_skill_domains[]` — e.g., `['aegis-agents-web', 'aegis-agents-data']`
- Token TTL: `task timeout_seconds + 300 seconds` buffer. Hard max: 86,700 seconds.
- Token type: service token (supports revocation).
- Metadata attached: `{ user_id, task_id, orchestrator_task_ref, required_skill_domains[], issued_at }`.

**Revocation call:** `POST /v1/auth/token/revoke`
- Triggered by Recovery Manager on every terminal task outcome.
- On Vault unavailability: Recovery Manager logs `REVOCATION_FAILED` critical event and schedules retry. Does not block task termination.

### 11.4 Outbound: Memory Interface → Memory Component

All interactions are mediated by M6 (Memory Interface). **Direct Memory Component database calls from other modules are prohibited.**

- **Write:** via the Memory Component's write API/adapter — body: `OrchestratorMemoryWritePayload {orchestrator_task_ref, task_id, data_type, timestamp, payload, ttl_seconds}`
- **Read:** via the Memory Component's read API/adapter — query params: `orchestrator_task_ref OR task_id`, `data_type`, `from_timestamp`, `to_timestamp`
- **Read returns:** array of matching payload objects, ordered by timestamp ascending
- **Supported `data_type` values:** `task_state | audit_log | recovery_event | policy_event`

### 11.5 Communications Component (NATS) — Topic Hierarchy

| Topic | Direction | Delivery | Max Payload |
|---|---|---|---|
| `aegis.orchestrator.tasks.inbound` | INBOUND | At-least-once | 1 MB |
| `aegis.orchestrator.tasks.results.>` | OUTBOUND | At-least-once | 2 MB |
| `aegis.orchestrator.status.events` | OUTBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.errors` | OUTBOUND | At-least-once | 64 KB |
| `aegis.orchestrator.audit.events` | OUTBOUND | Persistent (no TTL) | 16 KB |
| `aegis.orchestrator.metrics` | OUTBOUND | At-most-once | 8 KB |
| `aegis.orchestrator.tasks.deadletter` | OUTBOUND | Persistent | 1 MB |

> **🔴 CRITICAL:** All NATS connections MUST use mutual TLS (mTLS). The Orchestrator's NATS credentials MUST only permit publish/subscribe on `aegis.orchestrator.>` topics. Cross-component topic access requires explicit authorization.

---

## 12. Heartbeat & Health Monitoring

The Orchestrator monitors the health of the **components it depends on** (not agent heartbeats — that is the Agents Component's responsibility).

### 12.1 Dependency Health Checks

| Dependency | Check Method | Interval | On Failure |
|---|---|---|---|
| Vault (OpenBao) | `GET /v1/sys/health` | Every 10 seconds | Apply `vault_failure_mode` (FAIL_CLOSED default). Emit `vault_unavailable` audit event. |
| Memory Component | `ping()` via Memory Interface | Every 10 seconds | Queue writes locally for up to `memory_write_buffer_seconds`. Escalate to critical alert if exceeded. |
| NATS / Comms | NATS server PING | Every 5 seconds | Reconnect with exponential backoff. No messages accepted or published during reconnect. |
| Agents Component | `capability_query` probe | Every 30 seconds | Log `agents_component_unavailable`. New task dispatches paused. In-flight tasks continue. |

### 12.2 Orchestrator `/health` Endpoint

Exposed on internal network. Returns JSON:

```json
{
  "status": "healthy|degraded|unhealthy",
  "active_tasks": 42,
  "queue_depth": 3,
  "vault_reachable": true,
  "memory_reachable": true,
  "nats_connected": true,
  "uptime_seconds": 86400,
  "node_id": "orchestrator-node-1"
}
```

**Status rules:**
- `healthy` = all dependencies reachable, `queue_depth < high_water_mark`
- `degraded` = one non-critical dependency unreachable
- `unhealthy` = Vault or Memory unreachable

---

## 13. Security Design

### 13.1 Zero-Trust Task Entry

Every task entering the system is treated as untrusted until the Policy Enforcer validates it against the Vault. This applies equally to tasks from internal components as to user-originated tasks. There is no whitelist of "trusted" task sources.

### 13.2 Policy Scope as a Security Contract

The `policy_scope` attached to a `task_spec` by the Policy Enforcer is **immutable during task execution**. The Agents Component's Credential Broker cannot issue credentials outside this scope, and the Orchestrator's Recovery Manager cannot expand the scope during re-dispatch. Any attempt to expand scope during recovery is treated as a `SCOPE_VIOLATION` event.

### 13.3 Credential Lifecycle

- **Pre-authorization:** Policy Enforcer requests a scoped Vault token at dispatch time. The token reference (not the token itself) is passed to the Agents Component via `policy_scope`.
- **Revocation:** On every terminal task outcome — COMPLETED, FAILED, TIMED_OUT, POLICY_VIOLATION — the Recovery Manager triggers credential revocation via the Policy Enforcer. This is **non-optional**.
- **Revocation on failure:** If Vault is unavailable during revocation, the event is logged as `REVOCATION_FAILED` and queued for retry. Termination proceeds without waiting for revocation confirmation.

### 13.4 Audit Trail Immutability

All orchestrator audit records in the Memory Component are append-only. The Memory Interface MUST reject UPDATE and DELETE operations on records with `data_type = audit_log` or `data_type = policy_event`. This is enforced at the **storage layer**, not just the application layer.

### 13.5 Message Envelope Security

All messages published by the Orchestrator include a signed envelope:

```json
{
  "message_id": "uuid",
  "message_type": "task_spec",
  "source_component": "orchestrator",
  "correlation_id": "task_uuid",
  "timestamp": "ISO8601",
  "schema_version": "1.0",
  "payload": {}
}
```

Messages without a valid envelope are rejected by the Communications Gateway before entering the pipeline.

### 13.6 Security Classification

| Data Element | Classification | Controls |
|---|---|---|
| `task payload` | SENSITIVE | mTLS in transit. AES-256 at rest. Not logged at transport layer. |
| `policy_scope` | SENSITIVE | Never exposed to User I/O. Passed only to Agents Component as opaque token reference. |
| `audit_log records` | AUDIT — PROTECTED | AES-256 at rest + write-once integrity hash. Read only by Security/Audit role. |
| `user_id` | INTERNAL — SENSITIVE | Logged in audit records. Never in NATS payload headers. mTLS required. |
| `task_result payload` | SENSITIVE | Delivered only to `callback_topic`. Not persisted in NATS beyond 7 days. |

---

## 14. Error Handling & Resilience

### 14.1 Provisioning / Dispatch Failures

| Failure Scenario | Detection | Response |
|---|---|---|
| Vault unavailable at policy check | HTTP timeout or 503 | `FAIL_CLOSED`: return `VAULT_UNAVAILABLE`. `FAIL_OPEN`: use cached policy if within TTL. Log `vault_unavailable` event. |
| Agents Component unreachable | NATS timeout on `capability_query` | Retry `capability_query` up to 3 times with 1s backoff. On persistent failure, return `AGENTS_UNAVAILABLE`. |
| Memory write fails on dispatch | Memory Interface returns error | Retry up to 3 times. On persistent failure: abort dispatch, return `STORAGE_UNAVAILABLE`. Ensure no orphaned agent exists. |
| `task_spec` schema invalid | JSON schema validation | Return `INVALID_TASK_SPEC` immediately. No Vault query, no agent interaction. |
| Duplicate `task_id` received | Dedup check in Memory Interface | Return `DUPLICATE_TASK` with current task status. Do not spawn agent. |

### 14.2 Runtime Failures

| Failure Scenario | Detection | Response |
|---|---|---|
| Task timeout exceeded | Task Monitor `timeout_at` tick | Signal Recovery Manager → `agent_terminate` → credentials revoked → `TIMED_OUT` returned to User I/O. |
| Agent enters RECOVERING state | `agent_status_update` event | Recovery Manager: check retries → retrieve state from Memory → re-validate Vault scope → re-dispatch. Increment `retry_count`. |
| Orchestrator node crash | Process termination | On restart: Memory Interface rehydrates active task map. Task Monitor resumes timeout tracking. In-flight agents continue via Agents Component. |
| Vault revocation fails | Revocation HTTP error | Log `REVOCATION_FAILED` critical event. Schedule retry with exponential backoff (max 5 attempts). Do not block task termination. |
| NATS message delivery failure | Comms Gateway NACK or timeout | Apply NATS at-least-once retry. Deduplicate on receiver side using `message_id`. Dead-letter after `max_redelivery`. |

---

## 15. Observability Design

The Orchestrator is designed to be **fully observable without requiring access to task payload content**.

### 15.1 Structured Logging

Every log line emitted by the Orchestrator is structured JSON. Required fields: `timestamp`, `level`, `component`, `module`, `orchestrator_task_ref` (when applicable), `node_id`, `message`, `duration_ms` (for timed operations).

> **Credential values and task payloads MUST NEVER appear in log output.**

### 15.2 Metrics (emitted to `aegis.orchestrator.metrics`)

| Metric Name | Type | Description |
|---|---|---|
| `orchestrator_tasks_received_total` | Counter | Total tasks received since startup. |
| `orchestrator_tasks_completed_total` | Counter | Tasks that reached COMPLETED state. |
| `orchestrator_tasks_failed_total` | Counter | Tasks that reached any terminal failure state. |
| `orchestrator_policy_violations_total` | Counter | Tasks denied by Policy Enforcer. |
| `orchestrator_recovery_attempts_total` | Counter | Number of recovery re-dispatches attempted. |
| `orchestrator_task_latency_seconds` | Histogram | End-to-end task duration from receipt to terminal outcome. |
| `orchestrator_policy_check_latency_ms` | Histogram | Vault round-trip time per policy check. |
| `orchestrator_active_tasks` | Gauge | Current number of tasks in non-terminal states. |
| `orchestrator_vault_available` | Gauge (0/1) | 1 = Vault reachable. 0 = Vault unreachable. |

### 15.3 Distributed Tracing

Each task generates a trace span tree compatible with OpenTelemetry (OTLP export).

Spans: (1) `task_received` → (2) `dedup_check` → (3) `policy_validation` → (4) `capability_query` → (5) `task_dispatch` → (6) `task_result_received` → (7) `result_delivery`

Recovery attempts create child spans of the parent task span.

---

# PART VII — Configuration, PoC & Open Questions

## 16. Configuration & Environment Variables

All Orchestrator configuration is injected via environment variables. No configuration is hard-coded.

| Variable | Type | Default | Description |
|---|---|---|---|
| `VAULT_ADDR` | URL | Required | OpenBao API endpoint. |
| `VAULT_FAILURE_MODE` | enum | `FAIL_CLOSED` | Behavior when Vault is unreachable. |
| `VAULT_POLICY_CACHE_TTL_SECONDS` | integer | 60 | TTL for cached Vault policy responses. |
| `NATS_URL` | URL | Required | NATS JetStream server URL. |
| `NATS_CREDS_PATH` | path | Required | Path to NATS mTLS credentials file. |
| `MEMORY_ENDPOINT` | URL | Required | Memory Component write/read API. |
| `MAX_TASK_RETRIES` | integer | 3 | Max recovery re-dispatches per task. |
| `TASK_DEDUP_WINDOW_SECONDS` | integer | 300 | Deduplication window for `task_id` reuse detection. |
| `HEALTH_CHECK_INTERVAL_SECONDS` | integer | 10 | Interval for dependency health checks. |
| `METRICS_EMIT_INTERVAL_SECONDS` | integer | 15 | How frequently metrics are published to NATS. |
| `QUEUE_HIGH_WATER_MARK` | integer | 500 | Pending task queue depth that triggers `queue_pressure` metric. |
| `MEMORY_WRITE_BUFFER_SECONDS` | integer | 30 | How long to buffer writes locally if Memory Component is unreachable. |
| `NODE_ID` | string | hostname | Unique identifier for this Orchestrator instance. Included in all audit events. |

---

## 17. Proof of Concept (PoC)

### 17.1 PoC Objective

The PoC demonstrates the Orchestrator's three hardest behaviors in a minimal but realistic environment:

1. **Policy-first task dispatch:** a task that fails policy validation never reaches the Agents Component.
2. **Self-healing recovery:** when an agent is killed mid-task, the Orchestrator detects the failure, retrieves state from Memory, and re-dispatches to a fresh agent without user intervention.
3. **Heartbeat-based failure detection:** the Orchestrator's health monitoring detects Vault or Memory unavailability and responds per configured policy.

### 17.2 PoC Scope

- A single Orchestrator instance (no clustering required for PoC).
- Mock Agents Component that accepts `task_spec` and returns simulated events.
- A **real** OpenBao (dev mode) instance for policy validation.
- A **real** Memory Component instance (backed by its team-selected database) or a contract-compatible mock adapter for PoC.
- NATS in standalone mode.
- Task: *'Monitor a GitHub repository for new issues over 24 hours and summarize them.'* The agent is manually killed at T+2 minutes to trigger the recovery flow.

### 17.3 Implementation: Core Orchestrator Loop (Go)

```go
// ─── Orchestrator: Main Structures ───────────────────────────────────────────
type TaskState struct {
    OrchestratorRef  string
    TaskID           string
    UserID           string
    State            string  // RECEIVED|POLICY_CHECK|DISPATCHED|RUNNING|RECOVERING|COMPLETED|FAILED|TIMED_OUT
    PolicyScope      PolicyScope
    AgentID          string
    RetryCount       int
    TimeoutAt        time.Time
    Payload          json.RawMessage
}

type Orchestrator struct {
    nats         *nats.Conn
    vault        VaultClient
    memory       MemoryInterface
    activeTasks  sync.Map  // map[string]*TaskState — goroutine-safe
    cfg          OrchestratorConfig
    nodeID       string
}

// ─── Startup: Rehydrate active tasks from Memory ─────────────────────────────
func (o *Orchestrator) RehydrateFromMemory() error {
    states, err := o.memory.Read(MemoryQuery{
        DataType: "task_state", Filter: map[string]string{"state": "not_terminal"},
    })
    if err != nil { return fmt.Errorf("rehydration failed: %w", err) }
    for _, s := range states {
        var ts TaskState
        json.Unmarshal(s.Payload, &ts)
        o.activeTasks.Store(ts.TaskID, &ts)
        go o.monitorTaskTimeout(&ts)  // Resume timeout tracking
    }
    return nil
}

// ─── Task Dispatch: Full validation + policy + dispatch pipeline ──────────────
func (o *Orchestrator) HandleInboundTask(raw []byte) {
    var task UserTask
    if err := json.Unmarshal(raw, &task); err != nil {
        o.publishError(task.CallbackTopic, "INVALID_TASK_SPEC", err.Error())
        return
    }
    if err := validateSchema(task); err != nil {
        o.publishError(task.CallbackTopic, "INVALID_TASK_SPEC", err.Error())
        return  // No Vault query, no agent interaction
    }

    // 1. Deduplication check
    if existing := o.dedupCheck(task.TaskID); existing != nil {
        o.publishStatus(task.CallbackTopic, "DUPLICATE_TASK", existing.State)
        return
    }

    // 2. Policy validation via Vault
    scope, err := o.vault.ValidateAndScope(task.UserID, task.RequiredSkillDomains)
    o.writeAuditEvent(task.TaskID, "policy_check", err == nil)
    if err != nil {
        o.publishError(task.CallbackTopic, "POLICY_VIOLATION",
            "Task requires resources outside your configured permissions.")
        return  // Agent Component NEVER contacted
    }

    // 3. Build scoped task_spec and dispatch
    ref := uuid.New().String()
    ts := &TaskState{
        OrchestratorRef: ref, TaskID: task.TaskID,
        UserID: task.UserID, PolicyScope: scope,
        State: "DISPATCHED", RetryCount: 0,
        TimeoutAt: time.Now().Add(time.Duration(task.TimeoutSeconds) * time.Second),
        Payload: task.Payload,
    }

    // Persist state BEFORE dispatching — no orphaned agents on Memory failure
    if err := o.memory.Write(taskStatePayload(ts)); err != nil {
        o.publishError(task.CallbackTopic, "STORAGE_UNAVAILABLE", "State persistence failed")
        return
    }

    o.activeTasks.Store(task.TaskID, ts)
    o.dispatchToAgents(ts)
    go o.monitorTaskTimeout(ts)
    o.publishAccepted(task.CallbackTopic, ts)
}

// ─── Recovery: Context-aware rejuvenation with Vault re-validation ────────────
func (o *Orchestrator) HandleAgentRecovering(agentID, taskID string) {
    tsRaw, ok := o.activeTasks.Load(taskID)
    if !ok { return }  // Unknown task; ignore
    ts := tsRaw.(*TaskState)
    ts.RetryCount++
    if ts.RetryCount > o.cfg.MaxTaskRetries {
        o.terminateTask(ts, "MAX_RETRIES_EXCEEDED")
        return
    }

    // Re-validate scope — scope CANNOT expand during recovery
    if err := o.vault.VerifyScopeStillValid(ts.PolicyScope); err != nil {
        o.terminateTask(ts, "SCOPE_EXPIRED")
        return
    }

    // Retrieve last persisted state for context-aware restart
    snapshot, err := o.memory.ReadLatest(taskID, "task_state")
    if err != nil {
        o.terminateTask(ts, "STATE_RECOVERY_FAILED")
        return
    }

    ts.State = "RECOVERING"
    o.memory.Write(taskStatePayload(ts))  // Log recovery attempt
    o.dispatchToAgentsWithContext(ts, snapshot.Progress)
}

// ─── Timeout enforcement ──────────────────────────────────────────────────────
func (o *Orchestrator) monitorTaskTimeout(ts *TaskState) {
    remaining := time.Until(ts.TimeoutAt)
    if remaining <= 0 {
        o.terminateTask(ts, "TIMED_OUT")
        return
    }
    select {
    case <-time.After(remaining):
        if tsRaw, ok := o.activeTasks.Load(ts.TaskID); ok {
            current := tsRaw.(*TaskState)
            if !isTerminal(current.State) {
                o.terminateTask(current, "TIMED_OUT")
            }
        }
    }
}

// ─── Terminal cleanup: revoke credentials, persist final state ────────────────
func (o *Orchestrator) terminateTask(ts *TaskState, reason string) {
    ts.State = reason
    o.activeTasks.Delete(ts.TaskID)

    // 1. Revoke credentials — ALWAYS, even if Vault is degraded
    if err := o.vault.Revoke(ts.OrchestratorRef); err != nil {
        o.writeAuditEvent(ts.TaskID, "REVOCATION_FAILED", false)
        o.scheduleRevocationRetry(ts.OrchestratorRef)
    }

    // 2. Send agent_terminate to Agents Component
    o.publishToAgents("aegis.agents.lifecycle.terminate",
        AgentTerminate{AgentID: ts.AgentID, Reason: reason})

    // 3. Persist final state — must not be lost
    o.memory.Write(taskStatePayload(ts))

    // 4. Notify User I/O
    o.publishError(ts.Payload /* callback_topic */, reason, humanReadableReason(reason))
}
```

### 17.4 PoC Validation Criteria

| Scenario | Expected Outcome | Pass Criterion |
|---|---|---|
| Submit task with valid skills and policy | `task_accepted` within 5s (existing agent) or 35s (new) | Timing measured and logged |
| Submit task with skill domain not in user policy | `POLICY_VIOLATION` returned. No agent touched. | Vault audit log shows DENY |
| Submit duplicate `task_id` within dedup window | `DUPLICATE_TASK` returned with current status. No second agent. | `activeTasks` map has 1 entry |
| Kill agent process manually at T+2 minutes | Recovery detects failure, retrieves state from Memory, re-dispatches within 10s | Task resumes, `retry_count=1` |
| Kill agent 3 times (max retries) | `MAX_RETRIES_EXCEEDED` returned. Credentials revoked. | Vault shows token revoked |
| Task `timeout_seconds=30`, task takes 45s | `TIMED_OUT` at T+30. Agent terminated. | Timeout fires within ±2s |
| Take Vault offline mid-operation | `VAULT_UNAVAILABLE` (FAIL_CLOSED). No new tasks dispatched. | In-flight tasks continue; new tasks rejected |
| Orchestrator crash and restart | On restart: active tasks rehydrated from Memory. Monitoring resumes. | No task state lost |

---

## 18. Open Questions

| ID | Question | Impact | Owner |
|---|---|---|---|
| OQ-01 | When the Orchestrator re-dispatches a recovering task, should it prefer the same agent (if recovered) or always request a new agent? | Affects recovery latency vs. agent warmth | Orchestrator + Agents Component teams |
| OQ-02 | Should the Orchestrator support task priority queue with preemption? | Significant design impact on Task Monitor and Agents Component coordination | Platform team |
| OQ-03 | What is the correct behavior when the Memory Component is down and a task reaches its terminal state? | Data loss risk on Memory unavailability | Orchestrator + Memory teams |
| OQ-04 | Should the Orchestrator expose a query API for task status (`GET /tasks/{task_id}/status`), or should all status delivery be push-based via NATS? | User I/O integration complexity | Orchestrator + User I/O teams |
| OQ-05 | Multi-agent tasks: if a task requires sequential delegation to multiple specialized agents, does the Orchestrator track each sub-task independently, or does it see only the top-level task? | Critical for multi-agent workflow design | Orchestrator + Agents Component + Platform teams |

---

## 19. Document Revision History

| Version | Date | Author | Description |
|---|---|---|---|
| 1.0 | February 2026 | Junyu Ding | Initial draft. High-level concepts. Missing NFRs, security detail, and PoC rigor. |
| 2.0 | February 2026 | Junyu Ding | Full EDD. Added: module inventory, full requirements (FR + NFR), complete sequence diagrams, data models, interface specs, heartbeat design, security model, error handling, observability, configuration table, complete PoC with Go implementation, and open questions. |
