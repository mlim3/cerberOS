# Aegis OS — Orchestrator Component
## Engineering Design Document (EDD)

| Field | Value |
|---|---|
| Document ID | EDD-AEGIS-ORC-002 |
| Version | 3.0 |
| Status | Draft — For Review |
| Date | April 2026 |
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

The Orchestrator is the central control plane of Aegis OS. It is the single point of authority for receiving user-originated tasks, enforcing security policy, coordinating task decomposition via a planner task executed by the Agents Component, coordinating agent provisioning through the Agents Component, and managing the full execution lifecycle of every task from receipt to result delivery.

The Orchestrator does **not** execute tasks itself. **It contains no LLM and performs no natural language understanding.** It is a **Policy Enforcement Point (PEP)** and a **deterministic coordinator**: every task that enters the system must pass through the Orchestrator's validation pipeline before any agent is activated, any credential is pre-authorized, or any result is delivered back to the user.

Four core concerns define the Orchestrator's responsibility:

- **Task Authority:** The Orchestrator is the exclusive entry point for all tasks from the User I/O Component. No agent can be provisioned without an Orchestrator-issued `task_spec`.
- **Policy Enforcement:** Before any task is dispatched, the Orchestrator validates it against Vault-held security policies and enforces permission boundaries. Tasks that fail policy checks are rejected — not silently dropped.
- **Task Decomposition Coordination:** The Orchestrator routes every incoming task to the Agents Component as a standard `task.inbound` request targeting the `general` skill domain with planner-specific instructions. The planner task returns a structured execution plan with subtasks, agent assignments, and dependencies. The Orchestrator then executes the plan by dispatching subtasks to the Agents Component.
- **Lifecycle Coordination:** The Orchestrator tracks every active task and subtask, monitors agent health via the Agents Component, and drives recovery when agents fail. It is the authority on whether a task is RUNNING, RECOVERING, COMPLETED, or FAILED.

> **ℹ NOTE:** The Orchestrator is a stateless coordinator. All task state, plan state, subtask state, and agent state is persisted via the Memory Component. This enables horizontal scaling, crash recovery, and node migration without data loss.

---

## 2. Design Context and Principles

### 2.1 Relationship to the Agents Component

The Orchestrator and Agents Component have a strict principal-agent relationship. The Orchestrator is the **principal**: it defines tasks, establishes security scope, requests decomposition, and expects results. The Agents Component is the **executor**: it builds agents (including the Planner Agent), manages their lifecycle, and returns outcomes. The Orchestrator never manages microVMs directly — that is the Agents Component's exclusive concern.

Communication between the Orchestrator and Agents Component is **exclusively via NATS** through the Communications layer. In production this is expected to be NATS JetStream. For local integration, the current Orchestrator implementation can connect directly to a local NATS broker while still using the same `aegis.agents.*` and `aegis.orchestrator.*` subjects as the Agents Component.

The Planner Agent is a logical role implemented as a standard agent task in the Agents Component. It has an LLM and is responsible for parsing raw user input and producing a structured execution plan. The Orchestrator treats the planner as just another agent task: it sends a standard `task.inbound` request with planner instructions and receives the plan through the normal `task.result` / `task.failed` subjects.

For local end-to-end integration, the Orchestrator may run in a hybrid mode: real NATS transport to the Agents Component, with mock Vault and Memory dependencies in-process. This allows planner round-trip testing without requiring the full platform stack.

### 2.2 Policy-First Design

Every task dispatched by the Orchestrator carries a validated policy scope. This scope is derived from the user's configured permissions and the current Vault policy set. An agent cannot request credentials outside its scope because the scope was established before the agent was created. **The Planner Agent operates under the same policy_scope as the parent task** — it cannot produce a plan whose subtasks require skill domains outside the authorized scope.

> **🔴 CRITICAL:** Policy enforcement is not advisory. A task that cannot be scoped safely is returned as `POLICY_VIOLATION` to the User I/O Component with a human-readable explanation. It is never silently dropped or partially executed.

### 2.3 Stateless Component Design

The Orchestrator is stateless by design. Any state it needs persisted is immediately written to the Memory Component. This enables horizontal scaling and crash recovery. On restart, the Orchestrator rehydrates its active task list and in-flight plan state from the Memory Component and resumes monitoring.

### 2.4 Idempotency and At-Least-Once Safety

All inbound task messages carry a `task_id`. The Orchestrator maintains a deduplication window (default 300 seconds) to prevent duplicate execution. Receiving the same `task_id` twice within the deduplication window is a no-op: the Orchestrator returns the status of the existing task without spawning a second agent or requesting a second decomposition.

### 2.5 LLM-Free Orchestrator Principle

The Orchestrator contains no LLM, no inference engine, and no natural language understanding capability. This is a deliberate architectural decision based on analysis of production orchestration systems:

- **Conductor (conductor.build):** Orchestrator is pure infrastructure — manages Git worktrees, agent lifecycle, and dashboards. Zero AI in the orchestration layer.
- **Superset (superset.sh):** Agent-agnostic parallelization tool. Orchestrator manages isolation and monitoring, not intelligence.
- **Conductor OSS (Netflix):** Deterministic state machine with task queues. Orchestrates billions of workflows with no embedded LLM.

By keeping the Orchestrator LLM-free: (1) it remains fully deterministic and testable, (2) LLM failures affect only the Planner Agent, not the coordination pipeline, (3) the Orchestrator scales horizontally without LLM throughput constraints, (4) the Orchestrator and Agent teams can work independently.

---

# PART II — Architecture

## 3. System Context

### 3.1 Context Diagram (Described)

The Orchestrator sits at the center of the Aegis OS control plane, interfacing with all five other components:

| External Component | Direction | Protocol | Data Exchanged |
|---|---|---|---|
| User I/O Component | Bidirectional | NATS / Comms Interface | Inbound: `user_task` (with raw NL input in payload). Outbound: `task_result`, `task_status_update`, `clarification_request`, `error_response` |
| Agents Component | Bidirectional | NATS / Comms Interface | Outbound: `task.inbound` (planner + subtasks), `capability_query`, `agent_terminate`. Inbound: `task.accepted`, `task.result`, `task.failed`, `agent.status`, `capability.response` |
| Vault (OpenBao) | Bidirectional | OpenBao HTTP API | Outbound: `policy_validation_request`, `token_revoke`. Inbound: `policy_result`, `scoped_token` |
| Memory Component | Bidirectional | Memory Interface abstraction | Outbound: tagged task state writes, plan state writes, subtask state writes, audit events. Inbound: task state reads for recovery and deduplication |
| Communications (NATS) | Bidirectional | NATS / JetStream | All inter-component messages routed through NATS subjects. Production uses JetStream-backed delivery; local integration can use a direct NATS connection against the same subject hierarchy. |

---

## 4. Internal Architecture

The Orchestrator consists of **seven internal modules**. Each module has a single, well-defined responsibility. Modules communicate through defined internal interfaces, never directly manipulating each other's data.

### 4.1 Module Inventory

#### M1: Communications Gateway

| | |
|---|---|
| **Responsibilities** | Single inbound/outbound gateway for all NATS messaging. Receives `user_task` from User I/O Component. Routes outbound messages (results, status, errors, planner tasks, subtask tasks) to User I/O and Agents Component. Enforces message envelope validation — rejects malformed messages before they enter the pipeline. Adapts internal Orchestrator structs to the real Agents Component wire schema. Manages NATS consumer ACK/NAK and dead-letter queue monitoring. |
| **Inputs** | `user_task` from User I/O (via NATS); `task.result`, `task.failed`, and `agent.status` from Agents Component (via NATS); internal messages from Task Dispatcher and Plan Executor |
| **Outputs** | Parsed `user_task` to Task Dispatcher; routed responses to User I/O and Agents Component; planner `task.inbound` to Agents Component; subtask `task.inbound` to Agents Component |
| **Interfaces With** | Task Dispatcher (internal), Plan Executor (internal), NATS / Communications Component (external) |

#### M2: Task Dispatcher

| | |
|---|---|
| **Responsibilities** | Central coordinator for all incoming task routing decisions. Validates `user_task` envelope; rejects invalid specs immediately. Performs deduplication check via Memory Component using `task_id`. Routes to Policy Enforcer for permission validation before any agent interaction. **After policy validation, dispatches a standard `task.inbound` request to the Agents Component targeting the `general` skill domain with planner-specific instructions.** When the planner task returns a JSON execution plan through the normal `task.result` path, the Task Dispatcher validates the plan and passes it to the Plan Executor (M7) for subtask-level dispatch. Tracks top-level task status and correlates plan results back to the originating `user_task`. Persists `DECOMPOSING` before publishing the planner task. |
| **Inputs** | Parsed `user_task` from Communications Gateway; `policy_result` from Policy Enforcer; planner `task.result` / `task.failed` from Comms Gateway (via Agents Component); `plan_completed`/`plan_failed` from Plan Executor; `dedup_result` from Memory Interface |
| **Outputs** | `policy_check_request` to Policy Enforcer; planner `task.inbound` to Communications Gateway; `execution_plan` to Plan Executor (M7); `task_accepted`/`task_failed`/`policy_violation` to Communications Gateway (for User I/O) |
| **Interfaces With** | Communications Gateway, Policy Enforcer, Plan Executor (M7), Memory Interface, Recovery Manager |

#### M3: Policy Enforcer

| | |
|---|---|
| **Responsibilities** | Validates every task against the current Vault policy set before dispatch. Queries OpenBao to confirm the user's permission scope. Derives and attaches a `policy_scope` to the validated task — this scope is the ceiling for all subtask `task_spec` dispatches and all agent credential requests. Rejects tasks requesting skills outside the user's configured permission set. Logs every policy decision (ALLOW/DENY) as a structured audit event. On Vault unavailability: applies configurable fail-open or fail-closed behavior (default: fail-closed). |
| **Inputs** | `policy_check_request` from Task Dispatcher (`task_id`, `user_id`); `policy_result` from OpenBao Vault |
| **Outputs** | `policy_result` to Task Dispatcher (ALLOWED + `policy_scope`, or DENIED + reason); `audit_event` to Memory Interface (every decision) |
| **Interfaces With** | Task Dispatcher, Vault (OpenBao), Memory Interface |

#### M4: Task Monitor

| | |
|---|---|
| **Responsibilities** | Tracks every active task and subtask from dispatch to completion or failure. Maintains an in-memory task state map and subtask state map (rehydrated from Memory Component on startup). Enforces task-level timeout: if a task exceeds `timeout_seconds`, signals Recovery Manager to terminate the running agents and return `TIMED_OUT`. Enforces per-subtask timeouts as specified in the execution plan. Subscribes to `agent_status_update` events from the Agents Component. Detects RECOVERING/TERMINATED events and escalates to Recovery Manager. Emits `task_progress` events when agents publish intermediate progress. Transitions RECOVERING subtasks into a monitored waiting state; timeout remains the safety net if self-recovery does not complete. |
| **Inputs** | Task dispatch confirmation from Task Dispatcher; subtask dispatch confirmation from Plan Executor; `agent_status_update` from Comms Gateway; `timeout_tick` from internal scheduler |
| **Outputs** | `timeout_signal` to Recovery Manager; `task_progress` to Communications Gateway; `state_write` to Memory Interface (on every state change) |
| **Interfaces With** | Task Dispatcher, Plan Executor, Communications Gateway, Recovery Manager, Memory Interface |

#### M5: Recovery Manager

| | |
|---|---|
| **Responsibilities** | Responds to all non-nominal task and subtask events: agent failure, timeout, policy violation, decomposition failure, Vault/Memory unavailability. On subtask agent failure: determines recovery strategy based on failure count and failure type. If an agent is in `RECOVERING`, the Recovery Manager does not immediately re-dispatch; it trusts the existing agent to self-heal and relies on timeout or a later `TERMINATED` event as the safety net. If an agent is `TERMINATED`, coordinates with Memory Component to retrieve last valid subtask state before recovery attempt. Issues `agent_terminate` or `task_cancel` instructions to Communications Gateway. Escalates irrecoverable failures as `task_failed` messages. Manages retry budget: tracks per-subtask retry count; does not exceed configurable `max_retries`. Ensures credential revocation is triggered on Vault for any terminated agent. |
| **Inputs** | `timeout_signal` from Task Monitor; `agent_status_update` (RECOVERING/TERMINATED) from Comms Gateway; `component_failure` event from Memory Interface or Policy Enforcer; `decomposition_timeout` from Task Dispatcher |
| **Outputs** | `agent_terminate`/`task_cancel` to Comms Gateway; `task_failed` to Comms Gateway (for User I/O); `revoke_credentials` to Policy Enforcer; `recovery_audit_event` to Memory Interface |
| **Interfaces With** | Task Monitor, Communications Gateway, Policy Enforcer, Memory Interface |

#### M6: Memory Interface

| | |
|---|---|
| **Responsibilities** | Disciplined persistence gateway — the only module that writes to or reads from the Memory Component. Enforces structured, tagged write payloads: untagged writes are rejected with an error. Serves task state, plan state, and subtask state read requests for deduplication, recovery, and on-startup rehydration. Never accepts raw session transcripts — only structured, extracted state is persisted. Manages write retries (up to 3 attempts with exponential backoff) and escalates to Recovery Manager on persistent failure. |
| **Inputs** | Tagged write payloads from: Task Dispatcher, Plan Executor, Policy Enforcer, Task Monitor, Recovery Manager; read requests from: Task Dispatcher (dedup), Plan Executor (plan restore), Recovery Manager (state restore), Task Monitor (startup rehydration) |
| **Outputs** | Persisted state to Memory Component via the Memory Component API/adapter; retrieved state slices to requesting modules; `write_failure` event to Recovery Manager |
| **Interfaces With** | Task Dispatcher, Plan Executor, Policy Enforcer, Task Monitor, Recovery Manager, Memory Component |

#### M7: Plan Executor (NEW in v3.0)

| | |
|---|---|
| **Responsibilities** | Manages execution of structured plans returned by the Planner Agent. Receives an execution plan from the Task Dispatcher. Validates the plan's dependency graph (rejects circular dependencies and empty plans). Resolves subtask dependencies (DAG-based). Dispatches subtasks to the Agents Component in correct topological order via Communications Gateway. Tracks each subtask's state independently (`PENDING`, `DISPATCHED`, `RUNNING`, `COMPLETED`, `FAILED`, `BLOCKED`). Passes subtask output as input to dependent subtasks via `prior_results[]` injection. Aggregates final results when all subtasks complete. Signals Task Dispatcher on plan completion or failure. Supports up to `PLAN_EXECUTOR_MAX_PARALLEL` concurrent subtasks. |
| **Inputs** | `execution_plan` from Task Dispatcher; `task_result`/`task_failed` events from Communications Gateway (per subtask); `agent_status_update` events from Communications Gateway (per subtask agent) |
| **Outputs** | `task_spec` per subtask to Communications Gateway (for Agents Component); `plan_completed`/`plan_failed` to Task Dispatcher; `subtask_state_write` and `plan_state_write` to Memory Interface; `plan_progress` to Communications Gateway (for User I/O progress streaming) |
| **Interfaces With** | Task Dispatcher, Communications Gateway, Task Monitor, Memory Interface |

**Key Design Decisions for M7:**

1. **DAG-based dependency resolution:** Subtasks are ordered by `depends_on[]` fields. Subtasks with no dependencies start immediately. Subtasks with dependencies wait until all predecessors complete.
2. **Output piping:** When a subtask completes, its result is injected into dependent subtasks' `task_spec.payload` as `prior_results[]` before those subtasks are dispatched.
3. **Partial failure handling:** If a subtask fails and has no dependents, the plan may continue. If a failed subtask has dependents, those dependents are marked `BLOCKED` and the plan reports partial completion.
4. **Crash recovery:** Plan state is persisted to Memory Interface on every subtask transition. On Orchestrator restart, in-flight plans are rehydrated and execution resumes.
5. **Parallel dispatch:** Up to `PLAN_EXECUTOR_MAX_PARALLEL` subtasks may be dispatched simultaneously when their dependencies are satisfied.

---

# PART III — Functional & Non-Functional Requirements

## 5. Functional Requirements

### 5.1 Task Reception & Routing

| ID | Requirement | Priority |
|---|---|---|
| FR-TRK-01 | The Orchestrator SHALL be the exclusive entry point for all tasks from the User I/O Component. No task may reach the Agents Component without passing through the Orchestrator's validation pipeline. | MUST |
| FR-TRK-02 | On receiving a `user_task`, the Communications Gateway SHALL validate the message envelope schema before routing. Malformed messages SHALL be rejected with a structured error; they SHALL NOT enter the dispatch pipeline. | MUST |
| FR-TRK-03 | The Task Dispatcher SHALL perform a deduplication check against the Memory Component using `task_id` before initiating any processing. If the `task_id` is found within the deduplication window, the Orchestrator SHALL return the current task status without creating a new execution. | MUST |
| FR-TRK-04 | The Task Dispatcher SHALL accept `user_task` messages where `required_skill_domains[]` is empty or absent. The user's raw natural-language input is carried in the `payload.raw_input` field. All tasks are routed to the Planner Agent for decomposition. | MUST |
| FR-TRK-05 | Every task SHALL be assigned a unique `orchestrator_task_ref` that is distinct from the user-provided `task_id`. This internal reference is used for all Orchestrator-to-Agents communication. | MUST |
| FR-TRK-06 | The Task Dispatcher SHOULD track task queue depth and emit a `queue_pressure` metric when the pending queue exceeds a configurable high-water mark. | SHOULD |

### 5.2 Policy Enforcement

| ID | Requirement | Priority |
|---|---|---|
| FR-PE-01 | Every task SHALL be validated against the Vault (OpenBao) policy set before the planner `task.inbound` request is sent to the Planner Agent. Tasks that fail policy validation SHALL be returned as `POLICY_VIOLATION` with a human-readable reason. | MUST |
| FR-PE-02 | The Policy Enforcer SHALL derive a `policy_scope` from the Vault response. This scope defines the ceiling for all subtask dispatches during plan execution and SHALL constrain the planner `task.inbound` request and every `task_spec` sent to the Agents Component. | MUST |
| FR-PE-03 | Every policy decision — ALLOW or DENY — SHALL be written to the Memory Component as an `audit_event` with: `user_id`, `task_id`, outcome, timestamp, and `vault_policy_version`. | MUST |
| FR-PE-04 | If the Vault is unreachable and `vault_failure_mode` is `FAIL_CLOSED` (default), the Policy Enforcer SHALL deny the task and return `VAULT_UNAVAILABLE`. If `FAIL_OPEN`, it SHALL proceed with a cached policy (if within `cache_ttl_seconds`) or deny if no cache exists. | MUST |
| FR-PE-05 | The Policy Enforcer SHALL maintain a policy cache with a configurable TTL. Cached policies SHALL be invalidated on any Vault policy update event received via NATS. | SHOULD |

### 5.3 Task Decomposition

| ID | Requirement | Priority |
|---|---|---|
| FR-TD-01 | After policy validation, the Task Dispatcher SHALL send a standard `task.inbound` request to the Agents Component containing planner-specific `instructions`, `required_skills=["general"]`, and correlation metadata. The Orchestrator SHALL NOT attempt to interpret, classify, or decompose the task itself. | MUST |
| FR-TD-02 | The Orchestrator SHALL wait for the planner task's terminal response from the Agents Component within a configurable timeout (`DECOMPOSITION_TIMEOUT_SECONDS`, default: 30). On timeout, the task SHALL be marked `DECOMPOSITION_FAILED` and returned to User I/O. | MUST |
| FR-TD-03 | The planner task's successful `task.result` SHALL contain a structured execution plan: an ordered list of subtasks, each with a `subtask_id`, `required_skill_domains[]`, `action`, `instructions`, `params`, `depends_on[]`, and `timeout_seconds`. | MUST |
| FR-TD-04 | The Plan Executor (M7) SHALL validate the execution plan's dependency graph. Circular dependencies SHALL be rejected with `INVALID_PLAN`. Plans with zero subtasks SHALL be rejected with `EMPTY_PLAN`. Plans exceeding `MAX_SUBTASKS_PER_PLAN` SHALL be rejected with `PLAN_TOO_LARGE`. | MUST |
| FR-TD-05 | The Plan Executor SHALL dispatch subtasks in topological order based on `depends_on[]`. Subtasks with no unmet dependencies MAY be dispatched in parallel, up to `PLAN_EXECUTOR_MAX_PARALLEL`. Each subtask SHALL be dispatched as an independent `task_spec` to the Agents Component with its own `policy_scope` (inherited from the parent task, never expanded). | MUST |
| FR-TD-06 | When a subtask completes, the Plan Executor SHALL inject its result into the payload of all dependent subtasks as `prior_results[{subtask_id, result}]` before dispatching them. This enables result piping across the subtask chain. | MUST |
| FR-TD-07 | The Planner Agent SHALL only assign `required_skill_domains[]` values that are within the parent task's `policy_scope`. If the Planner Agent returns a plan with out-of-scope subtasks, the Plan Executor SHALL reject the plan with `SCOPE_VIOLATION`. | MUST |

### 5.4 Agent Lifecycle Coordination

| ID | Requirement | Priority |
|---|---|---|
| FR-ALC-01 | Before dispatching each subtask, the Plan Executor SHALL query the Agents Component's capability endpoint to determine if a capable agent exists, before requesting provisioning. | MUST |
| FR-ALC-02 | The Orchestrator SHALL NOT dictate how the Agents Component provisions agents. It SHALL only send a fully-formed `task_spec` (including `policy_scope`) and wait for a `task_accepted` or `provisioning_error` response. | MUST |
| FR-ALC-03 | The Orchestrator SHALL confirm `task_accepted` to the User I/O Component within **5 seconds** (existing agent path) or within **35 seconds** (new agent, full provisioning path, including decomposition). Confirmed tasks receive an `estimated_completion_at` field. | MUST |
| FR-ALC-04 | The Orchestrator SHALL respond to `capability_query` from the Agents Component on the `aegis.orchestrator.capability.query` topic within **500ms p99**. | MUST |
| FR-ALC-05 | The Orchestrator SHALL forward intermediate agent progress updates (per subtask) to the User I/O Component if a `user_context_id` is associated with the task. | SHOULD |

### 5.5 Self-Healing & Recovery

| ID | Requirement | Priority |
|---|---|---|
| FR-SH-01 | The Task Monitor SHALL enforce a per-task timeout and per-subtask timeouts. When the parent task's `timeout_seconds` is exceeded, the Task Monitor SHALL signal the Recovery Manager to terminate all running subtask agents and return `TIMED_OUT` to the User I/O Component. | MUST |
| FR-SH-02 | On receiving an `agent_status_update` with `state=RECOVERING`, the Recovery Manager SHALL retrieve the last persisted subtask state from the Memory Component and re-submit the task context to the Agents Component for agent respawn. | MUST |
| FR-SH-03 | The Recovery Manager SHALL enforce a configurable `max_task_retries` (default: 3) per subtask. If retries are exhausted for a subtask, it SHALL be marked `FAILED`. If the failed subtask has dependents, those dependents are marked `BLOCKED`. | MUST |
| FR-SH-04 | On any terminal subtask outcome (COMPLETED, FAILED, TIMED_OUT) the Recovery Manager SHALL trigger credential revocation via the Policy Enforcer for the associated agent. | MUST |
| FR-SH-05 | On Orchestrator startup, the Task Monitor SHALL rehydrate its active task map, plan state, and subtask state map from the Memory Component and resume monitoring any tasks that were DECOMPOSING, RUNNING, or RECOVERING at the time of shutdown. | MUST |
| FR-SH-06 | Recovery decisions SHALL be policy-checked with the Vault before re-dispatching. A re-dispatched subtask cannot receive a broader scope than the original `policy_scope`. | MUST |

### 5.6 Observability & Audit

| ID | Requirement | Priority |
|---|---|---|
| FR-OBS-01 | Every state transition of every task, plan, and subtask SHALL produce a structured audit event written to the Memory Component via Memory Interface. Events SHALL include: `orchestrator_task_ref`, `task_id`, `plan_id` (if any), `subtask_id` (if any), `user_context_id` (if any), `state`, `previous_state`, `timestamp`, `node_id`, `initiating_module`. | MUST |
| FR-OBS-02 | The Orchestrator SHALL expose a `/health` endpoint returning: component status, active task count, active plan count, queue depth, Memory Component reachability, Vault reachability. | MUST |
| FR-OBS-03 | The Orchestrator SHALL emit structured metrics to the `aegis.orchestrator.metrics` NATS subject at a configurable interval (default: 15 seconds). | MUST |
| FR-OBS-04 | Audit log records SHALL be append-only. The Memory Interface must reject UPDATE or DELETE operations on orchestrator audit records. | MUST |
| FR-OBS-05 | The Orchestrator SHOULD produce distributed trace spans (compatible with OpenTelemetry) for every task, including decomposition and per-subtask spans. | SHOULD |

---

## 6. Non-Functional Requirements

| ID | Category | Requirement | Target |
|---|---|---|---|
| NFR-01 | Performance | Task receipt to `task_accepted` (existing agent path, after decomposition) | < 5 seconds |
| NFR-02 | Performance | Task receipt to `task_accepted` (new agent, full provisioning + decomposition) | < 35 seconds |
| NFR-03 | Performance | Policy validation round-trip (Vault available) | < 200ms p99 |
| NFR-04 | Performance | Capability query response to Agents Component | < 500ms p99 |
| NFR-05 | Performance | Decomposition round-trip to Planner Agent | < 30 seconds |
| NFR-06 | Reliability | Task state must survive Orchestrator node failure without data loss | Zero data loss |
| NFR-07 | Reliability | Startup rehydration from Memory must complete before accepting new tasks | < 10 seconds |
| NFR-08 | Reliability | Recovery success rate for transient agent failures | > 95% |
| NFR-09 | Security | All policy decisions logged with 100% coverage | 100% audit coverage |
| NFR-10 | Security | Vault unreachable: default behavior | FAIL_CLOSED (no task dispatched) |
| NFR-11 | Security | The Orchestrator SHALL NOT contain any LLM or inference engine | 100% LLM-free |
| NFR-12 | Scalability | Concurrent tasks managed without race conditions | 1 to 10,000+ tasks |
| NFR-13 | Scalability | Orchestrator horizontal scale-out without coordination protocol | Stateless; N instances |
| NFR-14 | Observability | All task, plan, and subtask state transitions produce structured audit events | 100% coverage |

---

# PART IV — Data Flows & Sequence Diagrams

## 7. Primary Data Flows

### Flow 1: Task Reception and Schema Validation
1. User I/O Component publishes a `user_task` to `aegis.orchestrator.tasks.inbound` over NATS.
2. `user_task` includes: `task_id`, `user_id`, `priority`, `timeout_seconds`, `payload` (containing `raw_input` — the user's natural-language text), `callback_topic`, `user_context_id` (optional).
3. Communications Gateway validates the message envelope schema. Malformed messages return a structured error to `aegis.orchestrator.errors` without entering the pipeline.
4. Communications Gateway dispatches the validated `user_task` to Task Dispatcher.

### Flow 2: Deduplication Check
1. Task Dispatcher queries Memory Interface: `read({ data_type: 'task_state', filter: { task_id } })`.
2. If a record is found within `idempotency_window_seconds`, Task Dispatcher returns the current task status — no new execution is initiated.
3. If no record is found, Task Dispatcher proceeds to policy validation.

### Flow 3: Policy Validation
1. Task Dispatcher sends a `policy_check_request` to Policy Enforcer: `{ task_id, user_id }`.
2. Policy Enforcer queries OpenBao to retrieve the user's policy set.
3. OpenBao returns: `ALLOW + effective_scope`, or `DENY + reason`.
4. Policy Enforcer writes an `audit_event` to Memory Interface **regardless of outcome**.
5. On ALLOW: Policy Enforcer returns `policy_result (ALLOWED, policy_scope)` to Task Dispatcher.
6. On DENY: Policy Enforcer returns `policy_result (DENIED, reason)`. Task Dispatcher returns `POLICY_VIOLATION` to User I/O. **No planner task is sent. No agent is touched.**

### Flow 3.5: Task Decomposition (NEW in v3.0)
1. Policy validation completes with ALLOW. Task Dispatcher has the validated `policy_scope`.
2. Task Dispatcher persists task state as `DECOMPOSING` to Memory Interface.
3. Task Dispatcher publishes a standard `task.inbound` message to `aegis.agents.task.inbound` via the Communications Gateway. The payload uses `required_skills: ["general"]`, planner-specific `instructions`, and `trace_id = orchestrator_task_ref`.
4. The planner prompt constrains the output to `ExecutionPlan` JSON only, forces `parent_task_id` to equal the original top-level `task_id`, and restricts every `required_skill_domains[]` entry to the validated policy scope. If the policy scope is empty, the implementation currently falls back to `["general"]` for local integration.
5. The Agents Component provisions or reuses a general-purpose agent and runs the planner task.
6. The planner agent decomposes the task and returns the execution plan through the normal `aegis.orchestrator.task.result` subject. On provisioning / execution failure it returns `aegis.orchestrator.task.failed`.
7. Task Dispatcher parses the planner result into `ExecutionPlan` JSON, then validates the plan: checks for circular dependencies, empty plan, plan size, and subtask scope violations.
8. If the plan is invalid, Task Dispatcher returns `DECOMPOSITION_FAILED` or `INVALID_PLAN` to User I/O.
9. If the plan is valid, Task Dispatcher passes it to the Plan Executor (M7), persists the plan state (`PLAN_ACTIVE`) to Memory Interface, and immediately begins dispatching ready subtasks.

### Flow 4: Plan Execution — Subtask Capability Query & Dispatch
1. Plan Executor identifies subtasks with no unmet dependencies (initially, those with empty `depends_on[]`).
2. For each ready subtask, Plan Executor sends `capability_query` to Agents Component on `aegis.agents.capability.query`.
3. Agents Component returns `capability_response`: `match | partial_match | no_match`.
4. Plan Executor assembles an internal `task_spec`, which the Communications Gateway translates into `task.inbound` and publishes to `aegis.agents.task.inbound`.
5. The translated subtask request includes orchestrator metadata such as `task_kind=subtask`, `parent_task_id`, `plan_id`, `subtask_id`, and `action`.
6. Agents Component responds with `task.accepted (agent_id, estimated_completion)` or `task.failed`.
7. Plan Executor persists subtask state (`DISPATCH_PENDING`) to Memory Interface **before** publishing `task_spec`. After successful publish, it persists `DISPATCHED`. If publish fails, it persists `DELIVERY_FAILED` for that subtask.

### Flow 5: Subtask Monitoring, Result Piping, and Completion
1. Task Monitor subscribes to `agent.status` events on `aegis.orchestrator.agent.status`.
2. As agents progress, intermediate updates are forwarded to User I/O Component if `user_context_id` is present.
3. On `task_result` from Agents Component for a subtask: Plan Executor writes COMPLETED state for that subtask to Memory, checks if any dependent subtasks are now ready, and injects the result into their `prior_results[]` before dispatching them.
4. When all subtasks reach terminal states, Plan Executor aggregates results and signals `plan_completed` to Task Dispatcher.
5. Task Dispatcher writes COMPLETED state for the parent task, delivers aggregated result to User I/O via `callback_topic`. Recovery Manager triggers credential revocation for all subtask agents.
6. On parent task timeout: Task Monitor signals Recovery Manager → `agent_terminate` issued for all running subtask agents → TIMED_OUT returned to User I/O.

### Flow 6: Subtask Recovery Flow
1. Agent RECOVERING event received from Agents Component for a specific subtask.
2. Recovery Manager treats `RECOVERING` as a self-healing signal and does not immediately re-dispatch. The existing agent may return to `ACTIVE`; otherwise timeout remains the safety net.
3. If the agent later reports `TERMINATED`, Recovery Manager checks retry count. If count < `max_task_retries`:
   - Memory Interface read for last subtask state snapshot.
   - Vault checked (policy still valid for recovery scope).
   - Updated `task_spec` re-dispatched to Agents Component (same `policy_scope`, same `prior_results[]`).
4. If count >= `max_task_retries`: subtask marked FAILED. If the subtask has dependents, those dependents are marked BLOCKED. Plan Executor reports partial completion or plan_failed as appropriate.

---

## 8. Sequence Diagrams

### 8.1 New Task — Full Provisioning Path (Updated)

*Participants: User I/O → Comms Gateway → Task Dispatcher → Policy Enforcer → OpenBao → Planner Agent (via Agents Component) → Plan Executor → Agents Component → Memory Interface → User I/O*

1. User I/O → Comms Gateway: `user_task` (`aegis.orchestrator.tasks.inbound`) with raw NL input in `payload.raw_input`
2. Comms Gateway validates envelope → Task Dispatcher: parsed `user_task`
3. Task Dispatcher → Memory Interface: read dedup check (`task_id`)
4. Memory Interface → Task Dispatcher: `NOT_FOUND` (proceed)
5. Task Dispatcher → Policy Enforcer: `policy_check_request {task_id, user_id}`
6. Policy Enforcer → OpenBao (HTTP): `POST /v1/auth/token/create`
7. OpenBao → Policy Enforcer: `{allowed: true, policy_scope: {...}, token_accessor: '...'}`
8. Policy Enforcer → Memory Interface: write `audit_event {ALLOW, task_id, user_id, timestamp}`
9. Policy Enforcer → Task Dispatcher: `policy_result {ALLOWED, policy_scope}`
10. Task Dispatcher → Memory Interface: write `task_state {DECOMPOSING, task_id, timestamp}`
11. Task Dispatcher → Comms Gateway: planner `task.inbound` (`aegis.agents.task.inbound`) with `required_skills=["general"]`
12. Agents Component runs the planner task on a general-purpose agent.
13. Agents Component → Comms Gateway: planner `task.result` with execution plan JSON (`aegis.orchestrator.task.result`)
14. Task Dispatcher validates plan → Plan Executor: `execution_plan`
15. Plan Executor → Memory Interface: write `plan_state {PLAN_ACTIVE, plan_id, task_id}`
16. Plan Executor identifies subtask `s1` (no dependencies) → dispatch immediately
17. Plan Executor → Agents Component: `capability_query` for s1 (`aegis.agents.capability.query`)
18. Agents Component → Plan Executor: `capability_response {no_match, provisioning_estimate: 28s}`
19. Plan Executor → Memory Interface: write `subtask_state {DISPATCH_PENDING, s1, timestamp}`
20. Plan Executor → Comms Gateway → Agents Component: translated `task.inbound` for s1 (`aegis.agents.task.inbound`)
21. Plan Executor → Memory Interface: write `subtask_state {DISPATCHED, s1, timestamp}`
22. Task Dispatcher → User I/O: `task_accepted {task_id, plan_id, estimated_completion_at}`
23. *(agent provisions, executes, returns result for s1)*
24. Agents Component → Comms Gateway: `task_result` for s1
25. Plan Executor receives s1 result. Subtask `s2` depends on s1 → inject s1 result into s2's payload as `prior_results[]`. Dispatch s2.
26. *(repeat for remaining subtasks)*
27. All subtasks complete → Plan Executor → Task Dispatcher: `plan_completed` with aggregated results
28. Task Dispatcher → Memory Interface: write `task_state {COMPLETED}`
29. Task Dispatcher → Policy Enforcer: trigger credential revocation for all subtask agents
30. Policy Enforcer → OpenBao (HTTP): `POST /v1/auth/token/revoke` for each agent
31. Comms Gateway → User I/O: `task_result` delivered to `callback_topic`

### 8.2 Simple Task — Single Subtask Path

For a simple task like "set a reminder for 3pm", the Planner Agent returns a 1-subtask plan:

```json
{
  "plan_id": "plan-xyz",
  "parent_task_id": "task-abc",
  "subtasks": [
    {
      "subtask_id": "s1",
      "required_skill_domains": ["calendar"],
      "action": "set_reminder",
      "instructions": "Set a reminder for 3pm today.",
      "params": {"time": "3pm"},
      "depends_on": [],
      "timeout_seconds": 30
    }
  ]
}
```

Steps 1–15 identical to 8.1 (schema validation, dedup, policy check, decomposition).

16. Plan Executor dispatches s1 immediately (no dependencies).
17. Existing capable agent found → fast activation path, no new provisioning.
18. Agent executes s1, returns result.
19. Plan Executor → Task Dispatcher: `plan_completed`.
20. Task Dispatcher → User I/O: `task_result` delivered. End-to-end < 10 seconds including decomposition.

### 8.3 Self-Healing Recovery Flow (Per Subtask)

*Triggered when: `agent_status_update {state: RECOVERING}` or later `agent_status_update {state: TERMINATED}` is received from Agents Component for a subtask's agent*

1. Task Monitor receives `agent_status_update {agent_id, state: RECOVERING, task_id, subtask_id}`
2. Task Monitor → Recovery Manager: `recovery_signal {task_id, subtask_id, retry_count, policy_scope, reason: AGENT_RECOVERING}`
3. Recovery Manager logs the self-healing condition and does not immediately re-dispatch.
4. If the agent later reports `TERMINATED`, Task Monitor → Recovery Manager: `recovery_signal {task_id, subtask_id, retry_count, policy_scope, reason: AGENT_TERMINATED}`
5. Recovery Manager checks `retry_count` for the subtask: if < `max_task_retries` → proceed; else → FAIL the subtask
6. Recovery Manager → Memory Interface: read `subtask_state` snapshot `{subtask_id, latest}`
7. Memory Interface → Recovery Manager: `subtask_state {progress_summary, policy_scope, original_task_spec, prior_results}`
8. Recovery Manager → Policy Enforcer: re-validate policy scope `{task_id, policy_scope}`
9. Policy Enforcer → OpenBao: verify scope still valid
10. OpenBao → Policy Enforcer: VALID (or EXPIRED → Recovery Manager escalates to FAIL)
11. Recovery Manager → Comms Gateway: re-dispatch `task_spec` for subtask to `aegis.agents.tasks.inbound`
12. Recovery Manager → Memory Interface: write `recovery_event {subtask_id, attempt_n, timestamp}`
13. Agents Component responds with `task_accepted` — monitoring resumes at Task Monitor

> **🔴 CRITICAL:** On `max_retries` exceeded for a subtask: Recovery Manager issues `agent_terminate`, writes FAILED state for the subtask, triggers credential revocation. If the failed subtask has dependents, those are marked BLOCKED. Plan Executor reports partial completion or `plan_failed` to Task Dispatcher.

### 8.4 Policy Violation Flow

*Triggered when: Policy Enforcer receives DENY from OpenBao*

1. Task Dispatcher → Policy Enforcer: `policy_check_request`
2. Policy Enforcer → OpenBao: token create request
3. OpenBao → Policy Enforcer: `{allowed: false, reason: 'user policy does not permit this operation'}`
4. Policy Enforcer → Memory Interface: write `audit_event {DENY, task_id, user_id, reason, timestamp}`
5. Policy Enforcer → Task Dispatcher: `policy_result {DENIED, reason}`
6. Task Dispatcher → Comms Gateway: `policy_violation_response {task_id, error_code: POLICY_VIOLATION, user_message: '...'}`
7. Comms Gateway → User I/O: `policy_violation` delivered to `callback_topic`

**No planner `task.inbound` sent. No agent is provisioned. No credential is issued. The `policy_scope` remains at zero-trust.**

### 8.5 Decomposition Failure Flow (NEW in v3.0)

*Triggered when: Planner Agent does not respond within `DECOMPOSITION_TIMEOUT_SECONDS`, OR Planner Agent returns an invalid plan*

1. Task Dispatcher publishes planner `task.inbound` to `aegis.agents.task.inbound` at T=0.
2. Task Dispatcher starts timer for `DECOMPOSITION_TIMEOUT_SECONDS` (default 30s).
3. Timer fires without planner `task.result` / `task.failed` received.
4. Task Dispatcher → Memory Interface: write `task_state {DECOMPOSITION_FAILED, reason: TIMEOUT}`
5. Task Dispatcher → Comms Gateway: `task_failed {task_id, error_code: DECOMPOSITION_TIMEOUT, user_message: '...'}`
6. Comms Gateway → User I/O: `task_failed` delivered to `callback_topic`

**Alternative path:** Planner Agent responds but plan is invalid:

4. Task Dispatcher receives `task_decomposition_response`.
5. Plan validation detects circular deps / empty plan / scope violation.
6. Task Dispatcher → Memory Interface: write `task_state {DECOMPOSITION_FAILED, reason: INVALID_PLAN}`
7. Task Dispatcher → Comms Gateway: `task_failed {task_id, error_code: INVALID_PLAN, user_message: '...'}`

---

# PART V — Data Models & State Machine

## 9. Task Lifecycle State Machine

The Task Monitor is the **sole authority** for task and subtask state transitions.

### 9.1 Top-Level Task States

| State | Description | Entry Condition | Valid Transitions |
|---|---|---|---|
| RECEIVED | Task arrived, schema validated | `user_task` passes envelope validation | → DEDUP_CHECK → POLICY_CHECK → REJECTED (schema fail) |
| POLICY_CHECK | Awaiting Vault policy validation | Passed dedup check; `policy_check_request` sent | → DECOMPOSING (ALLOW) → POLICY_VIOLATION (DENY) |
| DECOMPOSING | Awaiting plan from Planner Agent | `policy_result` ALLOWED; planner `task.inbound` sent to `aegis.agents.task.inbound` | → PLAN_ACTIVE (valid plan received) → DECOMPOSITION_FAILED |
| PLAN_ACTIVE | Plan Executor dispatching subtasks | Valid execution plan received from Planner Agent | → COMPLETED (all subtasks done) → FAILED → TIMED_OUT → PARTIAL_COMPLETE |
| DECOMPOSITION_FAILED | Planner Agent timed out or returned invalid plan | Decomposition timeout OR plan validation failed | **Terminal.** No agents touched. |
| TIMED_OUT | Parent task exceeded `timeout_seconds` | Task Monitor `timeout_signal` fires | **Terminal.** All running subtask agents terminated; credentials revoked. |
| POLICY_VIOLATION | Task denied by policy validation | `policy_result` DENIED from Policy Enforcer | **Terminal.** No decomposition, no agent touched. |
| FAILED | All subtasks failed or blocking failure occurred | Plan Executor reports `plan_failed` | **Terminal.** Credentials revoked; audit written. |
| PARTIAL_COMPLETE | Some subtasks completed, some failed (no blocking) | Plan Executor reports partial success | **Terminal.** Partial results delivered to User I/O. |
| COMPLETED | All subtasks completed successfully | Plan Executor reports `plan_completed` | **Terminal.** Credentials revoked; aggregated result delivered. |

### 9.2 Subtask States (Managed by Plan Executor)

| State | Description | Entry Condition | Valid Transitions |
|---|---|---|---|
| PENDING | Subtask waiting for dependencies to complete | Subtask has unmet `depends_on[]` | → DISPATCH_PENDING (deps met) → BLOCKED (dep failed) |
| DISPATCH_PENDING | Subtask state persisted before agent publish | Dependencies met; pre-dispatch persistence succeeded | → DISPATCHED → DELIVERY_FAILED |
| DISPATCHED | `task_spec` successfully published; `task_accepted` confirmed | Agent publish succeeded | → RUNNING → RECOVERING → TIMED_OUT |
| RUNNING | Agent actively executing the subtask | Agent ACTIVE confirmed by Agents Component | → COMPLETED → RECOVERING → TIMED_OUT |
| RECOVERING | Agent self-healing; Orchestrator monitoring | `agent_status_update` RECOVERING received | → RUNNING → FAILED → TIMED_OUT |
| COMPLETED | Subtask result received | `task_result` received from Agents Component | **Terminal.** Result piped into dependents. |
| FAILED | Max retries exceeded or provisioning failure | Recovery Manager gives up | **Terminal.** Credentials revoked. Dependents → BLOCKED. |
| BLOCKED | A dependency failed, this subtask cannot run | Ancestor subtask reached FAILED | **Terminal.** No agent dispatched. |
| TIMED_OUT | Subtask exceeded its own `timeout_seconds` | Task Monitor timeout for subtask | **Terminal.** Agent terminated; credentials revoked. |
| DELIVERY_FAILED | Subtask could not be published | `task_spec` publish failed | **Terminal.** Cleanup performed. |

---

## 10. Data Models

### 10.1 Task State Record Schema

Stored in the Memory Component via Memory Interface. One record per top-level task, updated on every state transition.

| Field | Type | Description |
|---|---|---|
| `orchestrator_task_ref` | UUID (string) | Orchestrator-internal reference. Distinct from user-provided `task_id`. |
| `task_id` | UUID (string) | User-provided task identifier. Used for deduplication and correlation. |
| `user_id` | string | Identifier of the user or session that submitted the task. |
| `state` | enum | See §9.1 |
| `policy_scope` | object | Effective permission scope derived from Vault. Attached to decomposition request and all subtask `task_spec`s. Never expanded during execution. |
| `plan_id` | UUID \| null | ID of the execution plan returned by the Planner Agent. Null until DECOMPOSING → PLAN_ACTIVE. |
| `raw_input` | string | The user's natural-language input from `payload.raw_input`. |
| `decomposition_started_at` | ISO 8601 \| null | When the decomposition request was sent. |
| `decomposition_completed_at` | ISO 8601 \| null | When the plan was received from the Planner Agent. |
| `timeout_at` | ISO 8601 | Computed as `received_at + timeout_seconds`. Parent task hard deadline. |
| `completed_at` | ISO 8601 \| null | Timestamp of terminal outcome. Null while task is in progress. |
| `error_code` | string \| null | Set on any non-COMPLETED terminal state. E.g., `POLICY_VIOLATION`, `DECOMPOSITION_TIMEOUT`, `INVALID_PLAN`, `MAX_RETRIES_EXCEEDED`, `TIMED_OUT`. |
| `state_history` | StateEvent[] | Ordered list of `{state, timestamp, reason, node_id}`. Append-only. |

### 10.2 User Task Schema (Inbound from User I/O)

| Field | Type | Required | Description / Constraints |
|---|---|---|---|
| `task_id` | UUID string | YES | Globally unique. Deduplication key. |
| `user_id` | string | YES | User or session identifier. Used for policy lookup. |
| `priority` | integer | YES | 1 (lowest) to 10 (highest). Used to prioritize dispatch queue. |
| `timeout_seconds` | integer | YES | Min 30, max 86400. Overall task timeout (covers decomposition + all subtasks). |
| `payload` | object | YES | Task-specific data. For raw NL tasks: `{ "raw_input": "user's natural language text" }`. Max 1MB serialized. |
| `callback_topic` | string | YES | Valid NATS topic. All results for this task published here. |
| `user_context_id` | UUID \| null | NO | Optional user session reference. Enables progress streaming and clarification requests. |
| `idempotency_window_seconds` | integer | NO | Default 300. How long to remember this `task_id` for deduplication. |
| `required_skill_domains` | string[] | NO | **OPTIONAL.** If provided, may be used as a hint by the Planner Agent. If absent, the Planner Agent determines required domains from `raw_input`. |

### 10.3 Execution Plan Schema (NEW in v3.0)

Returned by the Planner Agent in `task_decomposition_response`. Stored as `plan_state` in Memory Component.

#### ExecutionPlan

| Field | Type | Description |
|---|---|---|
| `plan_id` | UUID | Unique identifier for this plan. Generated by the Planner Agent. |
| `parent_task_id` | UUID | The original `user_task`'s `task_id`. Used for correlation. |
| `subtasks` | Subtask[] | Ordered list of subtasks. Min 1, max `MAX_SUBTASKS_PER_PLAN`. |
| `created_at` | ISO 8601 | When the plan was generated by the Planner Agent. |

#### Subtask

| Field | Type | Description |
|---|---|---|
| `subtask_id` | string | Unique within the plan (e.g., `"s1"`, `"s2"`). |
| `required_skill_domains` | string[] | Skill domains this subtask requires. Populated by the Planner Agent. Must be within parent `policy_scope`. |
| `action` | string | Human-readable action label (e.g., `"search_flights"`, `"find_hotels"`). |
| `instructions` | string | Natural-language instruction for the executing agent. Written by the Planner Agent. |
| `params` | object | Subtask-specific parameters extracted by the Planner Agent. |
| `depends_on` | string[] | List of `subtask_ids` that must complete before this subtask can start. Empty = no dependencies. |
| `timeout_seconds` | integer | Per-subtask timeout. Must not exceed parent task's remaining time. |

### 10.4 Subtask State Record Schema (NEW in v3.0)

Stored in the Memory Component via Memory Interface. One record per subtask, updated on every state transition.

| Field | Type | Description |
|---|---|---|
| `subtask_id` | string | Plan-scoped unique identifier (e.g., `"s1"`). |
| `plan_id` | UUID | Parent plan's ID. |
| `task_id` | UUID | Parent user task's ID. |
| `state` | enum | See §9.2 |
| `agent_id` | UUID \| null | Assigned agent ID from Agents Component. Null until DISPATCHED. |
| `required_skill_domains` | string[] | From plan definition. |
| `depends_on` | string[] | From plan definition. |
| `prior_results` | object[] | Results from completed predecessor subtasks, piped into payload at dispatch. |
| `retry_count` | integer | Number of recovery attempts for this subtask. |
| `dispatched_at` | ISO 8601 \| null | When `task_spec` was sent to Agents Component. |
| `timeout_at` | ISO 8601 | Computed from subtask's `timeout_seconds`. |
| `completed_at` | ISO 8601 \| null | Timestamp of terminal outcome. |
| `result` | object \| null | Subtask output on COMPLETED. Used for piping into dependents. |
| `error_code` | string \| null | Set on any non-COMPLETED terminal state. |

### 10.5 Orchestrator Audit Log Schema

Stored in Memory Component, **append-only**. Every state transition and policy decision writes one record.

| Field | Type | Description |
|---|---|---|
| `log_id` | UUID | Unique log entry identifier. |
| `orchestrator_task_ref` | UUID | Associated task reference. |
| `plan_id` | UUID \| null | Associated plan ID, if applicable. |
| `subtask_id` | string \| null | Associated subtask ID, if applicable. |
| `event_type` | enum | `task_received \| policy_allow \| policy_deny \| decomposition_requested \| decomposition_completed \| decomposition_failed \| plan_active \| subtask_dispatched \| subtask_completed \| subtask_failed \| subtask_blocked \| task_completed \| task_failed \| recovery_attempt \| credential_revoked \| vault_unavailable \| component_failure` |
| `initiating_module` | string | `CommunicationsGateway \| TaskDispatcher \| PolicyEnforcer \| TaskMonitor \| RecoveryManager \| MemoryInterface \| PlanExecutor` |
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
| `task.inbound` (planner) | `aegis.agents.task.inbound` | After policy validation; sent to a general-purpose agent with planner instructions |
| `task.inbound` (subtask) | `aegis.agents.task.inbound` | Per subtask, when dependencies satisfied |
| `capability.query` | `aegis.agents.capability.query` | Before every subtask dispatch |
| `agent_terminate` | `aegis.agents.lifecycle.terminate` | Timeout exceeded or max retries reached |
| `task_cancel` | `aegis.agents.tasks.cancel` | Policy violation or irrecoverable failure |

### 11.3 Inbound: Orchestrator ← Agents Component

| Message Type | NATS Topic | Trigger |
|---|---|---|
| `task.accepted` | `aegis.orchestrator.task.accepted` | Agent confirms receipt of planner or subtask work |
| `task.result` | `aegis.orchestrator.task.result` | Planner task returns plan JSON, or subtask execution completes |
| `task.failed` | `aegis.orchestrator.task.failed` | Planner or subtask provisioning / execution failed before a result could be produced |
| `agent.status` | `aegis.orchestrator.agent.status` | Agent lifecycle state change |
| `capability.response` | `aegis.orchestrator.capability.response` | Response to capability query |

### 11.4 Planner `task.inbound` Payload Schema

The planner call uses the same agents-component wire schema as every other task:

| Field | Type | Description |
|---|---|---|
| `task_id` | UUID | The orchestrator-generated planner task ID. In implementation this is the top-level `orchestrator_task_ref`. |
| `required_skills` | string[] | Always `["general"]` for decomposition. |
| `instructions` | string | Planner-specific prompt containing the raw user task, the required `ExecutionPlan` JSON schema, `parent_task_id` equality constraints, and allowed-domain constraints derived from `policy_scope`. |
| `metadata` | object | Optional flat string map. Includes `orchestrator_task_ref`, `user_id`, `callback_topic`, and planner markers. |
| `trace_id` | UUID | Set to the top-level `orchestrator_task_ref` for correlation. |
| `user_context_id` | UUID \| null | Optional user context identifier. |

### 11.5 Planner Result Handling

The planner returns through the normal agents-component terminal subjects:

| Subject | Behavior |
|---|---|
| `aegis.orchestrator.task.result` | `payload.result` or `payload.output` contains the execution plan as JSON, a JSON string, or fenced JSON. The Dispatcher normalizes and parses it into `ExecutionPlan`, then validates it. |
| `aegis.orchestrator.task.failed` | Planner provisioning / execution failed. The Dispatcher marks the top-level task `DECOMPOSITION_FAILED` / `AGENTS_UNAVAILABLE`. |

### 11.6 Outbound: Policy Enforcer → Vault (OpenBao)

**Pre-authorization call:** `POST /v1/auth/token/create`
- Policies: derived from the user's permission profile
- Token TTL: `task timeout_seconds + 300 seconds` buffer. Hard max: 86,700 seconds.
- Token type: service token (supports revocation).
- Metadata attached: `{ user_id, task_id, orchestrator_task_ref, issued_at }`.

**Revocation call:** `POST /v1/auth/token/revoke`
- Triggered by Recovery Manager on every terminal task and subtask outcome.
- On Vault unavailability: Recovery Manager logs `REVOCATION_FAILED` critical event and schedules retry. Does not block task termination.

### 11.7 Outbound: Memory Interface → Memory Component

All interactions are mediated by M6 (Memory Interface). **Direct Memory Component database calls from other modules are prohibited.**

- **Write:** via the Memory Component's write API/adapter — body: `OrchestratorMemoryWritePayload {orchestrator_task_ref, task_id, plan_id, subtask_id, data_type, timestamp, payload, ttl_seconds}`
- **Read:** via the Memory Component's read API/adapter — query params: `orchestrator_task_ref OR task_id OR plan_id OR subtask_id`, `data_type`, `from_timestamp`, `to_timestamp`
- **Read returns:** array of matching payload objects, ordered by timestamp ascending
- **Supported `data_type` values:** `task_state | plan_state | subtask_state | audit_log | recovery_event | policy_event`

### 11.8 Communications Component (NATS) — Topic Hierarchy

| Topic | Direction | Delivery | Max Payload |
|---|---|---|---|
| `aegis.orchestrator.tasks.inbound` | INBOUND | At-least-once | 1 MB |
| `aegis.orchestrator.task.accepted` | INBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.task.result` | INBOUND | At-least-once | 2 MB |
| `aegis.orchestrator.task.failed` | INBOUND | At-least-once | 32 KB |
| `aegis.orchestrator.agent.status` | INBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.capability.response` | INBOUND | At-most-once | 16 KB |
| `aegis.agents.task.inbound` | OUTBOUND | At-least-once | 1 MB |
| `aegis.agents.capability.query` | OUTBOUND | At-most-once | 8 KB |
| `aegis.agents.lifecycle.terminate` | OUTBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.status.events` | OUTBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.errors` | OUTBOUND | At-least-once | 64 KB |
| `aegis.orchestrator.audit.events` | OUTBOUND | Persistent (no TTL) | 16 KB |
| `aegis.orchestrator.metrics` | OUTBOUND | At-most-once | 8 KB |
| `aegis.orchestrator.tasks.deadletter` | OUTBOUND | Persistent | 1 MB |

> **🔴 CRITICAL:** All NATS connections MUST use mutual TLS (mTLS). The Orchestrator's NATS credentials MUST only permit publish/subscribe on authorized topics. Cross-component topic access requires explicit authorization.

---

## 12. Heartbeat & Health Monitoring

The Orchestrator monitors the health of the **components it depends on** (not agent heartbeats — that is the Agents Component's responsibility).

### 12.1 Dependency Health Checks

| Dependency | Check Method | Interval | On Failure |
|---|---|---|---|
| Vault (OpenBao) | `GET /v1/sys/health` | Every 10 seconds | Apply `vault_failure_mode` (FAIL_CLOSED default). Emit `vault_unavailable` audit event. |
| Memory Component | `ping()` via Memory Interface | Every 10 seconds | Queue writes locally for up to `memory_write_buffer_seconds`. Escalate to critical alert if exceeded. |
| NATS / Comms | NATS server PING | Every 5 seconds | Reconnect with exponential backoff. No messages accepted or published during reconnect. |
| Agents Component | `capability_query` probe | Every 30 seconds | Log `agents_component_unavailable`. New task dispatches and decomposition requests paused. In-flight tasks continue. |

### 12.2 Orchestrator `/health` Endpoint

Exposed on internal network. Returns JSON:

```json
{
  "status": "healthy|degraded|unhealthy",
  "active_tasks": 42,
  "active_plans": 38,
  "active_subtasks": 87,
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

The `policy_scope` attached to a task by the Policy Enforcer is **immutable during task execution**. The Planner Agent must produce subtasks whose skill domains fall within this scope. The Agents Component's Credential Broker cannot issue credentials outside this scope, and the Orchestrator's Recovery Manager cannot expand the scope during re-dispatch. Any attempt to expand scope is treated as a `SCOPE_VIOLATION` event.

### 13.3 Credential Lifecycle

- **Pre-authorization:** Policy Enforcer requests a scoped Vault token at dispatch time. The token reference (not the token itself) is passed to the Agents Component via `policy_scope`.
- **Revocation:** On every terminal task or subtask outcome — COMPLETED, FAILED, TIMED_OUT, POLICY_VIOLATION, BLOCKED — the Recovery Manager triggers credential revocation via the Policy Enforcer. This is **non-optional**.
- **Revocation on failure:** If Vault is unavailable during revocation, the event is logged as `REVOCATION_FAILED` and queued for retry. Termination proceeds without waiting for revocation confirmation.

### 13.4 Audit Trail Immutability

All orchestrator audit records in the Memory Component are append-only. The Memory Interface MUST reject UPDATE and DELETE operations on records with `data_type = audit_log` or `data_type = policy_event`. This is enforced at the **storage layer**, not just the application layer.

### 13.5 Message Envelope Security

All messages published by the Orchestrator include a signed envelope:

```json
{
  "message_id": "uuid",
  "message_type": "task.inbound",
  "source_component": "orchestrator",
  "correlation_id": "task_uuid",
  "timestamp": "ISO8601",
  "schema_version": "1.0",
  "payload": {}
}
```

Messages without a valid envelope are rejected by the Communications Gateway before entering the pipeline.

### 13.6 Planner Agent Scope Enforcement

The planner task receives the parent task's effective scope constraints in its generated instructions and associated metadata. It is constrained to produce subtasks whose `required_skill_domains` fall entirely within this scope. If the planner returns a plan with any subtask requiring out-of-scope domains, the Plan Executor rejects the plan with `SCOPE_VIOLATION` and the task is terminated.

### 13.7 Security Classification

| Data Element | Classification | Controls |
|---|---|---|
| `task payload` / `raw_input` | SENSITIVE | mTLS in transit. AES-256 at rest. Not logged at transport layer. |
| `policy_scope` | SENSITIVE | Never exposed to User I/O. Passed to Planner Agent and Agents Component as opaque token reference. |
| `execution_plan` | INTERNAL — SENSITIVE | Persisted in Memory. May contain extracted user intent. Not exposed to User I/O except as final result. |
| `audit_log records` | AUDIT — PROTECTED | AES-256 at rest + write-once integrity hash. Read only by Security/Audit role. |
| `user_id` | INTERNAL — SENSITIVE | Logged in audit records. Never in NATS payload headers. mTLS required. |
| `task_result payload` | SENSITIVE | Delivered only to `callback_topic`. Not persisted in NATS beyond 7 days. |

---

## 14. Error Handling & Resilience

### 14.1 Provisioning / Dispatch Failures

| Failure Scenario | Detection | Response |
|---|---|---|
| Vault unavailable at policy check | HTTP timeout or 503 | `FAIL_CLOSED`: return `VAULT_UNAVAILABLE`. `FAIL_OPEN`: use cached policy if within TTL. Log `vault_unavailable` event. |
| Planner Agent timeout | `DECOMPOSITION_TIMEOUT_SECONDS` elapsed | Return `DECOMPOSITION_TIMEOUT` to User I/O. Log event. |
| Planner Agent returns invalid plan | Plan validation fails (circular deps, empty, scope violation, too large) | Return `INVALID_PLAN` to User I/O. Log validation failure details. |
| Agents Component unreachable | NATS timeout on `capability_query` or planner / subtask `task.inbound` dispatch | Retry up to 3 times with 1s backoff. On persistent failure, return `AGENTS_UNAVAILABLE`. |
| Memory write fails on dispatch | Memory Interface returns error | Retry up to 3 times. On persistent failure: abort dispatch, return `STORAGE_UNAVAILABLE`. Ensure no orphaned agent exists. |
| `user_task` schema invalid | JSON schema validation | Return `INVALID_TASK_SPEC` immediately. No Vault query, no decomposition, no agent interaction. |
| Duplicate `task_id` received | Dedup check in Memory Interface | Return `DUPLICATE_TASK` with current task status. Do not spawn agent. |

### 14.2 Runtime Failures

| Failure Scenario | Detection | Response |
|---|---|---|
| Parent task timeout exceeded | Task Monitor `timeout_at` tick | Signal Recovery Manager → terminate all running subtask agents → credentials revoked → `TIMED_OUT` returned to User I/O. |
| Subtask timeout exceeded | Task Monitor per-subtask `timeout_at` | Signal Recovery Manager → terminate subtask agent → mark subtask TIMED_OUT → BLOCK dependent subtasks → report plan status. |
| Subtask agent enters RECOVERING | `agent_status_update` event | Recovery Manager: check retries → retrieve state from Memory → re-validate Vault scope → re-dispatch. Increment `retry_count`. |
| Subtask agent TERMINATED | `agent_status_update` event | Recovery Manager: if retry_count < max, re-dispatch; else mark subtask FAILED, BLOCK dependents. |
| Orchestrator node crash | Process termination | On restart: Memory Interface rehydrates active task map, plan state, subtask state. Task Monitor resumes timeout tracking. In-flight agents continue via Agents Component. |
| Vault revocation fails | Revocation HTTP error | Log `REVOCATION_FAILED` critical event. Schedule retry with exponential backoff (max 5 attempts). Do not block task termination. |
| NATS message delivery failure | Comms Gateway NACK or timeout | Apply NATS at-least-once retry. Deduplicate on receiver side using `message_id`. Dead-letter after `max_redelivery`. |

---

## 15. Observability Design

The Orchestrator is designed to be **fully observable without requiring access to task payload content**.

### 15.1 Structured Logging

Every log line emitted by the Orchestrator is structured JSON. Required fields: `timestamp`, `level`, `component`, `module`, `orchestrator_task_ref` (when applicable), `plan_id` (when applicable), `subtask_id` (when applicable), `node_id`, `message`, `duration_ms` (for timed operations).

> **Credential values, task payloads, and raw user input MUST NEVER appear in log output.**

### 15.2 Metrics (emitted to `aegis.orchestrator.metrics`)

| Metric Name | Type | Description |
|---|---|---|
| `orchestrator_tasks_received_total` | Counter | Total tasks received since startup. |
| `orchestrator_tasks_completed_total` | Counter | Tasks that reached COMPLETED state. |
| `orchestrator_tasks_failed_total` | Counter | Tasks that reached any terminal failure state. |
| `orchestrator_tasks_partial_total` | Counter | Tasks that reached PARTIAL_COMPLETE state. |
| `orchestrator_policy_violations_total` | Counter | Tasks denied by Policy Enforcer. |
| `orchestrator_decomposition_failures_total` | Counter | Tasks that failed decomposition (timeout or invalid plan). |
| `orchestrator_recovery_attempts_total` | Counter | Number of recovery re-dispatches attempted. |
| `orchestrator_subtasks_dispatched_total` | Counter | Total subtasks dispatched across all plans. |
| `orchestrator_subtasks_completed_total` | Counter | Subtasks that reached COMPLETED state. |
| `orchestrator_subtasks_blocked_total` | Counter | Subtasks that reached BLOCKED state. |
| `orchestrator_task_latency_seconds` | Histogram | End-to-end task duration from receipt to terminal outcome. |
| `orchestrator_decomposition_latency_seconds` | Histogram | Time from planner `task.inbound` published to planner response received. |
| `orchestrator_policy_check_latency_ms` | Histogram | Vault round-trip time per policy check. |
| `orchestrator_active_tasks` | Gauge | Current number of tasks in non-terminal states. |
| `orchestrator_active_plans` | Gauge | Current number of plans being executed. |
| `orchestrator_active_subtasks` | Gauge | Current number of subtasks in non-terminal states. |
| `orchestrator_vault_available` | Gauge (0/1) | 1 = Vault reachable. 0 = Vault unreachable. |

### 15.3 Distributed Tracing

Each task generates a trace span tree compatible with OpenTelemetry (OTLP export).

Top-level spans: (1) `task_received` → (2) `dedup_check` → (3) `policy_validation` → (4) `decomposition` → (5) `plan_execution` → (6) `result_delivery`

Under `plan_execution`, each subtask creates a child span tree: `capability_query` → `subtask_dispatch` → `subtask_result_received`. Recovery attempts create child spans of the subtask span.

---

# PART VII — Configuration, PoC & Open Questions

## 16. Configuration & Environment Variables

All Orchestrator configuration is injected via environment variables. No configuration is hard-coded.

Current implementation note: if required production dependencies such as `VAULT_ADDR` or `MEMORY_ENDPOINT` are omitted at startup, the binary falls back to demo defaults for those subsystems. If `NATS_URL` is still explicitly provided, the process runs in a hybrid local-integration mode: real NATS transport, in-process mock Vault, and in-process mock Memory.

| Variable | Type | Default | Description |
|---|---|---|---|
| `VAULT_ADDR` | URL | Required | OpenBao API endpoint. |
| `VAULT_FAILURE_MODE` | enum | `FAIL_CLOSED` | Behavior when Vault is unreachable. |
| `VAULT_POLICY_CACHE_TTL_SECONDS` | integer | 60 | TTL for cached Vault policy responses. |
| `NATS_URL` | URL | Required | NATS server URL. Production expects JetStream-enabled NATS. Local integration commonly uses `nats://localhost:4222`. |
| `NATS_CREDS_PATH` | path | Optional | Path to NATS credentials / mTLS file. Leave empty for local unsecured integration. |
| `MEMORY_ENDPOINT` | URL | Required | Memory Component write/read API. |
| `MAX_TASK_RETRIES` | integer | 3 | Max recovery re-dispatches per subtask. |
| `TASK_DEDUP_WINDOW_SECONDS` | integer | 300 | Deduplication window for `task_id` reuse detection. |
| `DECOMPOSITION_TIMEOUT_SECONDS` | integer | 30 | Max time to wait for Planner Agent's decomposition response. |
| `MAX_SUBTASKS_PER_PLAN` | integer | 20 | Maximum subtasks allowed in a single execution plan. |
| `PLAN_EXECUTOR_MAX_PARALLEL` | integer | 5 | Max subtasks the Plan Executor may dispatch simultaneously. |
| `HEALTH_CHECK_INTERVAL_SECONDS` | integer | 10 | Interval for dependency health checks. |
| `METRICS_EMIT_INTERVAL_SECONDS` | integer | 15 | How frequently metrics are published to NATS. |
| `QUEUE_HIGH_WATER_MARK` | integer | 500 | Pending task queue depth that triggers `queue_pressure` metric. |
| `MEMORY_WRITE_BUFFER_SECONDS` | integer | 30 | How long to buffer writes locally if Memory Component is unreachable. |
| `NODE_ID` | string | hostname | Unique identifier for this Orchestrator instance. Included in all audit events. |

---

## 17. Proof of Concept (PoC)

### 17.1 PoC Objective

The PoC demonstrates the Orchestrator's four hardest behaviors in a minimal but realistic environment:

1. **Policy-first task dispatch:** a task that fails policy validation never reaches the Planner Agent or any other agent.
2. **Task decomposition via Planner Agent:** a multi-step natural-language task is sent to the Planner Agent, which returns a structured plan. The Orchestrator executes the plan's subtasks in dependency order with result piping.
3. **Self-healing recovery:** when a subtask agent is killed mid-task, the Orchestrator detects the failure, retrieves state from Memory, and re-dispatches to a fresh agent without user intervention.
4. **Heartbeat-based failure detection:** the Orchestrator's health monitoring detects Vault or Memory unavailability and responds per configured policy.

The current codebase additionally supports a practical local integration path where the Orchestrator uses a real NATS connection to a live `agents-component` deployment while continuing to use in-process mock Vault and Memory services.

### 17.2 PoC Scope

- A single Orchestrator instance (no clustering required for PoC).
- A single Orchestrator instance (no clustering required for PoC).
- A live `agents-component` process connected to the same local NATS broker.
- NATS in standalone local mode at `nats://localhost:4222`.
- In-process mock Vault and mock Memory inside the Orchestrator runtime are acceptable for the current end-to-end integration path.
- Planner dispatch uses the real agents-component contract: `aegis.agents.task.inbound` with `required_skills=["general"]`, `instructions`, `metadata`, `trace_id`, and `user_context_id`.
- Task example for local validation: *"make a sandwich"*. Expected planner output is a valid multi-subtask `ExecutionPlan` using only allowed domains such as `["general"]`.
- Stretch scenario: manually kill a subtask agent to exercise recovery after the planner round-trip is already verified.

### 17.3 Implementation: Core Orchestrator Loop (Go)

```go
// ─── Orchestrator: Main Structures ───────────────────────────────────────────
type TaskState struct {
    OrchestratorRef  string
    TaskID           string
    UserID           string
    State            string  // RECEIVED|POLICY_CHECK|DECOMPOSING|PLAN_ACTIVE|COMPLETED|FAILED|...
    PolicyScope      PolicyScope
    PlanID           string
    RawInput         string
    TimeoutAt        time.Time
    CallbackTopic    string
}

type SubtaskState struct {
    SubtaskID        string
    PlanID           string
    TaskID           string
    State            string  // PENDING|DISPATCH_PENDING|DISPATCHED|RUNNING|COMPLETED|FAILED|BLOCKED|...
    AgentID          string
    RequiredDomains  []string
    DependsOn        []string
    PriorResults     []PriorResult
    RetryCount       int
    TimeoutAt        time.Time
    Result           json.RawMessage
}

type ExecutionPlan struct {
    PlanID        string
    ParentTaskID  string
    Subtasks      []Subtask
}

type Orchestrator struct {
    nats         *nats.Conn
    vault        VaultClient
    memory       MemoryInterface
    activeTasks  sync.Map  // map[string]*TaskState
    activeSubs   sync.Map  // map[string]*SubtaskState — key: "planID:subtaskID"
    cfg          OrchestratorConfig
    nodeID       string
}

// ─── Startup: Rehydrate active tasks and plans from Memory ───────────────────
func (o *Orchestrator) RehydrateFromMemory() error {
    states, err := o.memory.Read(MemoryQuery{
        DataType: "task_state", Filter: map[string]string{"state": "not_terminal"},
    })
    if err != nil { return fmt.Errorf("rehydration failed: %w", err) }
    for _, s := range states {
        var ts TaskState
        json.Unmarshal(s.Payload, &ts)
        o.activeTasks.Store(ts.TaskID, &ts)
        go o.monitorTaskTimeout(&ts)
    }
    // Also rehydrate subtask states for in-flight plans
    subStates, _ := o.memory.Read(MemoryQuery{
        DataType: "subtask_state", Filter: map[string]string{"state": "not_terminal"},
    })
    for _, s := range subStates {
        var ss SubtaskState
        json.Unmarshal(s.Payload, &ss)
        key := fmt.Sprintf("%s:%s", ss.PlanID, ss.SubtaskID)
        o.activeSubs.Store(key, &ss)
    }
    return nil
}

// ─── Task Ingress: validate + dedup + policy + decompose ─────────────────────
func (o *Orchestrator) HandleInboundTask(raw []byte) {
    var task UserTask
    if err := json.Unmarshal(raw, &task); err != nil {
        o.publishError(task.CallbackTopic, "INVALID_TASK_SPEC", err.Error())
        return
    }
    if err := validateSchema(task); err != nil {
        o.publishError(task.CallbackTopic, "INVALID_TASK_SPEC", err.Error())
        return
    }

    // 1. Deduplication check
    if existing := o.dedupCheck(task.TaskID); existing != nil {
        o.publishStatus(task.CallbackTopic, "DUPLICATE_TASK", existing.State)
        return
    }

    // 2. Policy validation via Vault
    scope, err := o.vault.ValidateAndScope(task.UserID)
    o.writeAuditEvent(task.TaskID, "policy_check", err == nil)
    if err != nil {
        o.publishError(task.CallbackTopic, "POLICY_VIOLATION",
            "Task requires resources outside your configured permissions.")
        return  // No decomposition, no agent contact
    }

    // 3. Persist DECOMPOSING state and send decomposition request
    ref := uuid.New().String()
    ts := &TaskState{
        OrchestratorRef: ref, TaskID: task.TaskID, UserID: task.UserID,
        PolicyScope: scope, State: "DECOMPOSING",
        RawInput: task.Payload.RawInput,
        TimeoutAt: time.Now().Add(time.Duration(task.TimeoutSeconds) * time.Second),
        CallbackTopic: task.CallbackTopic,
    }
    if err := o.memory.Write(taskStatePayload(ts)); err != nil {
        o.publishError(task.CallbackTopic, "STORAGE_UNAVAILABLE", "State persistence failed")
        return
    }
    o.activeTasks.Store(task.TaskID, ts)
    go o.monitorTaskTimeout(ts)

    // 4. Send decomposition request to Planner Agent
    o.sendDecompositionRequest(ts)
}

// ─── Decomposition Request ───────────────────────────────────────────────────
func (o *Orchestrator) sendDecompositionRequest(ts *TaskState) {
    req := DecompositionRequest{
        TaskID: ts.TaskID, OrchestratorTaskRef: ts.OrchestratorRef,
        UserID: ts.UserID, RawInput: ts.RawInput,
        PolicyScope: ts.PolicyScope,
    }
    o.publishToAgents("aegis.agents.decomposition.request", req)

    // Start decomposition timeout
    go func() {
        select {
        case <-time.After(time.Duration(o.cfg.DecompositionTimeoutSeconds) * time.Second):
            if tsRaw, ok := o.activeTasks.Load(ts.TaskID); ok {
                current := tsRaw.(*TaskState)
                if current.State == "DECOMPOSING" {
                    o.failTask(current, "DECOMPOSITION_TIMEOUT")
                }
            }
        }
    }()
}

// ─── Handle Decomposition Response ───────────────────────────────────────────
func (o *Orchestrator) HandleDecompositionResponse(raw []byte) {
    var resp DecompositionResponse
    if err := json.Unmarshal(raw, &resp); err != nil { return }

    tsRaw, ok := o.activeTasks.Load(resp.TaskID)
    if !ok { return }  // Unknown task
    ts := tsRaw.(*TaskState)

    // Validate plan
    if err := o.validatePlan(resp.Plan, ts.PolicyScope); err != nil {
        o.failTask(ts, "INVALID_PLAN")
        return
    }

    // Persist plan and update task state
    ts.State = "PLAN_ACTIVE"
    ts.PlanID = resp.Plan.PlanID
    o.memory.Write(taskStatePayload(ts))
    o.memory.Write(planStatePayload(resp.Plan, ts.TaskID))

    // Hand off to Plan Executor
    o.planExecutor.Execute(resp.Plan, ts)
}

// ─── Plan Execution: dispatch subtasks in topological order ──────────────────
func (o *Orchestrator) Execute(plan ExecutionPlan, ts *TaskState) {
    // Initialize all subtask states as PENDING
    for _, st := range plan.Subtasks {
        sub := &SubtaskState{
            SubtaskID: st.SubtaskID, PlanID: plan.PlanID, TaskID: ts.TaskID,
            State: "PENDING", RequiredDomains: st.RequiredSkillDomains,
            DependsOn: st.DependsOn, RetryCount: 0,
            TimeoutAt: time.Now().Add(time.Duration(st.TimeoutSeconds) * time.Second),
        }
        key := fmt.Sprintf("%s:%s", plan.PlanID, st.SubtaskID)
        o.activeSubs.Store(key, sub)
        o.memory.Write(subtaskStatePayload(sub))
    }

    // Dispatch subtasks with no dependencies
    o.dispatchReadySubtasks(plan, ts)
}

// ─── Dispatch subtasks whose dependencies are all met ────────────────────────
func (o *Orchestrator) dispatchReadySubtasks(plan ExecutionPlan, ts *TaskState) {
    for _, st := range plan.Subtasks {
        key := fmt.Sprintf("%s:%s", plan.PlanID, st.SubtaskID)
        subRaw, _ := o.activeSubs.Load(key)
        sub := subRaw.(*SubtaskState)
        if sub.State != "PENDING" { continue }

        // Check if all dependencies are COMPLETED
        if !o.allDependenciesMet(plan.PlanID, sub.DependsOn) { continue }

        // Collect prior_results from dependencies
        priorResults := o.collectPriorResults(plan.PlanID, sub.DependsOn)
        sub.PriorResults = priorResults
        sub.State = "DISPATCH_PENDING"
        o.memory.Write(subtaskStatePayload(sub))

        // Dispatch subtask as a task_spec
        spec := o.buildTaskSpec(st, sub, ts.PolicyScope, priorResults)
        o.publishToAgents("aegis.agents.tasks.inbound", spec)

        sub.State = "DISPATCHED"
        o.memory.Write(subtaskStatePayload(sub))
    }
}

// ─── Handle Subtask Result ───────────────────────────────────────────────────
func (o *Orchestrator) HandleSubtaskResult(raw []byte) {
    var result SubtaskResult
    json.Unmarshal(raw, &result)

    key := fmt.Sprintf("%s:%s", result.PlanID, result.SubtaskID)
    subRaw, ok := o.activeSubs.Load(key)
    if !ok { return }
    sub := subRaw.(*SubtaskState)

    sub.State = "COMPLETED"
    sub.Result = result.Output
    o.memory.Write(subtaskStatePayload(sub))

    // Revoke credentials for this subtask's agent
    o.vault.Revoke(sub.AgentID)

    // Load plan and check if more subtasks can be dispatched
    plan := o.loadPlan(result.PlanID)
    tsRaw, _ := o.activeTasks.Load(sub.TaskID)
    ts := tsRaw.(*TaskState)

    o.dispatchReadySubtasks(plan, ts)

    // Check if plan is complete
    if o.isPlanComplete(plan.PlanID) {
        o.completePlan(plan, ts)
    }
}

// ─── Recovery: Context-aware rejuvenation with Vault re-validation ───────────
func (o *Orchestrator) HandleSubtaskAgentRecovering(agentID, planID, subtaskID string) {
    key := fmt.Sprintf("%s:%s", planID, subtaskID)
    subRaw, ok := o.activeSubs.Load(key)
    if !ok { return }
    sub := subRaw.(*SubtaskState)
    sub.RetryCount++
    if sub.RetryCount > o.cfg.MaxTaskRetries {
        o.failSubtask(sub, "MAX_RETRIES_EXCEEDED")
        return
    }

    // Re-validate scope — scope CANNOT expand during recovery
    tsRaw, _ := o.activeTasks.Load(sub.TaskID)
    ts := tsRaw.(*TaskState)
    if err := o.vault.VerifyScopeStillValid(ts.PolicyScope); err != nil {
        o.failSubtask(sub, "SCOPE_EXPIRED")
        return
    }

    sub.State = "RECOVERING"
    o.memory.Write(subtaskStatePayload(sub))
    // Re-dispatch with preserved prior_results
    plan := o.loadPlan(sub.PlanID)
    st := o.findSubtaskInPlan(plan, sub.SubtaskID)
    spec := o.buildTaskSpec(st, sub, ts.PolicyScope, sub.PriorResults)
    o.publishToAgents("aegis.agents.tasks.inbound", spec)
}

// ─── Timeout enforcement ─────────────────────────────────────────────────────
func (o *Orchestrator) monitorTaskTimeout(ts *TaskState) {
    remaining := time.Until(ts.TimeoutAt)
    if remaining <= 0 { o.failTask(ts, "TIMED_OUT"); return }
    select {
    case <-time.After(remaining):
        if tsRaw, ok := o.activeTasks.Load(ts.TaskID); ok {
            current := tsRaw.(*TaskState)
            if !isTerminal(current.State) {
                o.failTask(current, "TIMED_OUT")
            }
        }
    }
}

// ─── Terminal cleanup: revoke credentials, persist final state ───────────────
func (o *Orchestrator) failTask(ts *TaskState, reason string) {
    ts.State = reason
    o.activeTasks.Delete(ts.TaskID)

    // 1. Terminate all subtask agents for this task
    o.terminateAllSubtaskAgents(ts.TaskID)

    // 2. Revoke credentials
    o.vault.RevokeForTask(ts.OrchestratorRef)

    // 3. Persist final state
    o.memory.Write(taskStatePayload(ts))

    // 4. Notify User I/O
    o.publishError(ts.CallbackTopic, reason, humanReadableReason(reason))
}
```

### 17.4 PoC Validation Criteria

| Scenario | Expected Outcome | Pass Criterion |
|---|---|---|
| Submit multi-step NL task | Planner Agent returns structured plan with 3 subtasks and correct dependencies. | Plan has s1→s2→s3 dependency chain. Decomposition < 30s. |
| Plan Executor dispatches subtasks in order | s1 dispatched first. s2 dispatched only after s1 completes. s3 after both. | Memory audit log shows correct ordering. |
| Subtask result piping | s2's `task_spec.payload` contains s1's result in `prior_results[]`. | s2 agent receives s1 output. |
| Simple task ("set reminder") | Planner returns 1-subtask plan. Plan Executor dispatches directly. | End-to-end < 10s including decomposition. |
| Submit task with skill domain not in user policy | `POLICY_VIOLATION` returned. No planner `task.inbound` sent, no agent touched. | Vault audit log shows DENY before any planner dispatch event. |
| Submit duplicate `task_id` within dedup window | `DUPLICATE_TASK` returned with current status. No second decomposition, no second agent. | `activeTasks` map has 1 entry. |
| Planner Agent timeout | If Planner doesn't respond within `DECOMPOSITION_TIMEOUT_SECONDS`, task marked `DECOMPOSITION_FAILED`. | User I/O receives error within 35s. |
| Kill subtask agent process manually | Recovery detects failure, retrieves state from Memory, re-dispatches within 10s. | Subtask resumes, `retry_count=1`. |
| Kill subtask agent 3 times (max retries) | Subtask marked FAILED. Dependent subtasks marked BLOCKED. Credentials revoked. | Plan reports partial or failed. |
| Subtask failure mid-plan | If s1 fails, s2 and s3 are marked `BLOCKED`. User I/O notified with partial results. | No orphaned agents. Credentials revoked. |
| Task `timeout_seconds=30`, task takes 45s | `TIMED_OUT` at T+30. All running subtask agents terminated. | Timeout fires within ±2s. |
| Take Vault offline mid-operation | `VAULT_UNAVAILABLE` (FAIL_CLOSED). No new tasks dispatched. | In-flight tasks continue; new tasks rejected. |
| Orchestrator crash and restart | On restart: active tasks, plans, and subtasks rehydrated from Memory. Monitoring resumes. | No task state lost. |

---

## 18. Open Questions

| ID | Question | Impact | Owner |
|---|---|---|---|
| OQ-01 | When the Orchestrator re-dispatches a recovering subtask, should it prefer the same agent (if recovered) or always request a new agent? | Affects recovery latency vs. agent warmth | Orchestrator + Agents Component teams |
| OQ-02 | Should the Orchestrator support task priority queue with preemption? | Significant design impact on Task Monitor and Agents Component coordination | Platform team |
| OQ-03 | What is the correct behavior when the Memory Component is down and a task reaches its terminal state? | Data loss risk on Memory unavailability | Orchestrator + Memory teams |
| OQ-04 | Should the Orchestrator expose a query API for task status (`GET /tasks/{task_id}/status`), or should all status delivery be push-based via NATS? | User I/O integration complexity | Orchestrator + User I/O teams |
| OQ-05 | ✅ **RESOLVED in v3.0:** Multi-agent tasks are supported via the Plan Executor. The Orchestrator tracks each subtask independently with its own lifecycle. | Resolved | Orchestrator team |
| OQ-06 | Should the Planner Agent be a persistent long-lived agent or provisioned fresh per decomposition request? Persistent = faster (no spawn overhead). Fresh = cleaner context. | Affects decomposition latency | Orchestrator + Agents teams |
| OQ-07 | How does the Planner Agent know the available skill domains? Options: (a) hardcoded list in Planner Agent's system prompt, (b) Planner Agent queries the Skill Hierarchy Manager at decomposition time, (c) Orchestrator passes the available domains in the decomposition request. | Affects plan quality | Agents team |
| OQ-08 | Should the Plan Executor support re-planning? If a subtask fails, should the Orchestrator ask the Planner Agent to produce a revised plan? Or just fail the blocked subtasks? | Affects resilience for complex tasks | Orchestrator + Agents teams |

---

## 19. Document Revision History

| Version | Date | Author | Description |
|---|---|---|---|
| 1.0 | February 2026 | Junyu Ding | Initial draft. High-level concepts. Missing NFRs, security detail, and PoC rigor. |
| 2.0 | February 2026 | Junyu Ding | Full EDD. Added: module inventory, full requirements (FR + NFR), complete sequence diagrams, data models, interface specs, heartbeat design, security model, error handling, observability, configuration table, complete PoC with Go implementation, and open questions. |
| 3.0 | April 2026 | Junyu Ding | **Task Decomposition via Planner Agent.** Added: M7 Plan Executor module; LLM-Free Orchestrator Principle (§2.5); task decomposition coordination as 4th core concern; new functional requirements FR-TD-01 through FR-TD-07; new task states DECOMPOSING, PLAN_ACTIVE, DECOMPOSITION_FAILED, PARTIAL_COMPLETE; new subtask state machine (§9.2); Execution Plan Schema (§10.3); Subtask State Record Schema (§10.4); new NATS topics for decomposition; Flow 3.5 (Task Decomposition); Sequence Diagram 8.5 (Decomposition Failure); new env vars (DECOMPOSITION_TIMEOUT_SECONDS, MAX_SUBTASKS_PER_PLAN, PLAN_EXECUTOR_MAX_PARALLEL); updated PoC with decomposition scenarios; subtask-level metrics; Planner Agent scope enforcement. FR-TRK-04 updated to accept empty `required_skill_domains[]`. Resolved OQ-05. New OQ-06 through OQ-08. Design rationale based on Conductor/Superset/Netflix Conductor research: the Orchestrator remains LLM-free; intelligence lives in the Planner Agent. |
