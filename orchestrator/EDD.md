# Aegis OS — Orchestrator Component
## Engineering Design Document (EDD)

| Field | Value |
|---|---|
| Document ID | EDD-AEGIS-ORC-002 |
| Version | 3.4 |
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
11. [Interface Specifications](#11-interface-specifications) *(updated: confirmation request/response schemas added)*
12. [Heartbeat & Health Monitoring](#12-heartbeat--health-monitoring)
13. [Security Design](#13-security-design)
14. [Error Handling & Resilience](#14-error-handling--resilience)
15. [Observability Design](#15-observability-design) *(updated: structured logging, OTel tracing, Grafana stack, debug endpoint)*
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
- **Lifecycle Coordination:** The Orchestrator tracks every active parent task and delegates per-subtask state ownership to the Plan Executor. It monitors agent health via the Agents Component, drives parent-task recovery when agents fail, and remains the authority on whether a task is RUNNING, RECOVERING, COMPLETED, PARTIAL_COMPLETE, or FAILED.

> **ℹ NOTE:** The Orchestrator persists task state, plan state, and subtask state via the Memory Component. Current implementation rehydrates active parent tasks on startup. Plan/subtask records are persisted for audit, observability, and future restart-resume support; in-flight plan execution is currently kept in process memory.

---

## 2. Design Context and Principles

### 2.1 Relationship to the Agents Component

The Orchestrator and Agents Component have a strict principal-agent relationship. The Orchestrator is the **principal**: it defines tasks, establishes security scope, requests decomposition, and expects results. The Agents Component is the **executor**: it builds agents (including the Planner Agent), manages their lifecycle, and returns outcomes. The Orchestrator never manages microVMs directly — that is the Agents Component's exclusive concern.

Communication between the Orchestrator and Agents Component is **exclusively via NATS JetStream** through the Communications Component. No direct calls.

The Planner Agent is a logical role implemented as a standard agent task in the Agents Component. It has an LLM and is responsible for parsing raw user input and producing a structured execution plan. The Orchestrator treats the planner as just another agent task: it sends a standard `task.inbound` request with planner instructions and receives the plan through the normal `task.result` / `task.failed` subjects.

### 2.2 Policy-First Design

Every task dispatched by the Orchestrator carries a validated policy scope. This scope is derived from the user's configured permissions and the current Vault policy set. An agent cannot request credentials outside its scope because the scope was established before the agent was created. **The Planner Agent operates under the same policy_scope as the parent task** — it cannot produce a plan whose subtasks require skill domains outside the authorized scope.

> **🔴 CRITICAL:** Policy enforcement is not advisory. A task that cannot be scoped safely is returned as `POLICY_VIOLATION` to the User I/O Component with a human-readable explanation. It is never silently dropped or partially executed.

### 2.3 Stateless Component Design

The Orchestrator persists all durable task records through the Memory Component. On restart, the current implementation rehydrates its active parent task list and resumes parent-task timeout monitoring. In-flight plan/subtask execution state is written to Memory, but active `PlanExecutor` instances are not yet reconstructed after process restart.

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
| Agents Component | Bidirectional | NATS / Comms Interface | Outbound: `task.inbound` (planner + subtasks), `capability_query`, `agent_terminate`. Inbound: `task.accepted`, `task.result`, `task.failed`, `agent.status`, `capability.response`, `credential.request` |
| Vault (OpenBao) | Bidirectional | OpenBao HTTP API | Outbound: `policy_validation_request`, `token_revoke`. Inbound: `policy_result`, `scoped_token` |
| Memory Component | Bidirectional | Memory Interface abstraction | Outbound: tagged task state writes, plan state writes, subtask state writes, audit events. Inbound: task state reads for recovery and deduplication |
| Communications (NATS) | Bidirectional | NATS JetStream | All inter-component messages routed through NATS streams. Orchestrator publishes and subscribes via defined topic hierarchy. |
| IO Component | Bidirectional | HTTP (outbound) + NATS (inbound) | **Outbound (HTTP):** real-time task status, credential requests, plan preview cards, and subtask confirmation prompts pushed to `POST /api/orchestrator/stream-events`. Optional — disabled when `IO_API_BASE` is not set. **Inbound (NATS):** user plan approve/reject decisions on `aegis.orchestrator.plan.decision`; user subtask confirmation responses on `aegis.orchestrator.task.confirmation_response`. |

---

## 4. Internal Architecture

The Orchestrator consists of **seven internal modules**. Each module has a single, well-defined responsibility. Modules communicate through defined internal interfaces, never directly manipulating each other's data.

### 4.1 Module Inventory

#### M1: Communications Gateway

| | |
|---|---|
| **Responsibilities** | Single inbound/outbound gateway for all NATS messaging. Receives `user_task` from User I/O Component. Routes outbound messages (results, status, errors, planner tasks, subtask tasks) to User I/O and Agents Component. Enforces message envelope validation — rejects malformed messages before they enter the pipeline. Adapts internal Orchestrator structs to the real Agents Component wire schema. Manages NATS consumer ACK/NAK and dead-letter queue monitoring. Subscribes to `aegis.orchestrator.credential.request` and forwards `user_input` credential requests to the registered handler (IO Component bridge); vault `authorize`/`revoke` operations are filtered out and not forwarded. Subscribes to `aegis.orchestrator.plan.decision` and routes user plan approve/reject decisions to the Task Dispatcher's registered handler. Subscribes to `aegis.orchestrator.task.confirmation_response` and routes user subtask confirmation/rejection responses to the Plan Executor's registered handler. Exposes `PublishConfirmationRequest` — called by the Plan Executor to forward subtask confirmation prompts to the IO Component via the registered HTTP callback. |
| **Inputs** | `user_task` from User I/O (via NATS); `task.result`, `task.failed`, `agent.status`, `credential.request`, `plan.decision`, and `confirmation_response` from NATS; internal messages from Task Dispatcher and Plan Executor |
| **Outputs** | Parsed `user_task` to Task Dispatcher; routed responses to User I/O and Agents Component; planner `task.inbound` to Agents Component; subtask `task.inbound` to Agents Component; `credential_request` events forwarded to IO Component HTTP bridge; `plan_preview` events forwarded to IO Component HTTP bridge; `confirmation_request` events forwarded to IO Component HTTP bridge; `plan.decision` events routed to Task Dispatcher; `confirmation_response` events routed to Plan Executor |
| **Interfaces With** | Task Dispatcher (internal), Plan Executor (internal), NATS / Communications Component (external), IO Component (external — HTTP push + NATS receive) |

#### M2: Task Dispatcher

| | |
|---|---|
| **Responsibilities** | Central coordinator for all incoming task routing decisions. Validates `user_task` envelope; rejects invalid specs immediately. Performs deduplication check via Memory Component using `task_id` — a prior task at a terminal state is **not** treated as a duplicate; the same `task_id` may re-enter as a follow-up (chat-style continuations). Routes to Policy Enforcer for permission validation before any agent interaction. **After policy validation, dispatches a standard `task.inbound` request to the Agents Component targeting the `general` skill domain with planner-specific instructions.** Personalization: optionally fetches user facts from the Memory Component's `personal_info` store and prepends them to the planner prompt (best-effort; failure is non-fatal). When the planner task returns a JSON execution plan through the normal `task.result` path, the Task Dispatcher validates the plan. **Plan approval gate (NEW m3):** When `PLAN_APPROVAL_MODE` is `always`, or `multi` (default) and the plan has more than one subtask, the Dispatcher transitions the task to `AWAITING_APPROVAL`, pushes a `plan_preview` event to the IO Component, and waits for an explicit user approve/reject decision on `aegis.orchestrator.plan.decision`. On approval the plan is handed to Plan Executor (M7). On rejection the task is failed with `PLAN_REJECTED`. If no decision arrives within `PLAN_APPROVAL_TIMEOUT_SECONDS` the task is failed with `PLAN_APPROVAL_TIMEOUT`. Single-subtask plans and plans where mode is `off` bypass the gate and are dispatched immediately. Tracks top-level task status and correlates plan results back to the originating `user_task`. Persists `DECOMPOSING` before publishing the planner task. Publishes `task_accepted` early — right after policy validation — so the user sees acknowledgement before the planner LLM round-trip. **Pushes real-time status updates to the IO Component at key lifecycle transitions:** task received (DECOMPOSING → `working`), awaiting approval (`awaiting_feedback`), plan active (PLAN_ACTIVE → `working` with subtask count), task completed (`completed`), and task failed (`completed` with error detail). |
| **Inputs** | Parsed `user_task` from Communications Gateway; `policy_result` from Policy Enforcer; planner `task.result` / `task.failed` from Comms Gateway (via Agents Component); `plan_completed`/`plan_failed` from Plan Executor; `dedup_result` from Memory Interface |
| **Outputs** | `policy_check_request` to Policy Enforcer; planner `task.inbound` to Communications Gateway; `execution_plan` to Plan Executor (M7); `task_accepted`/`task_failed`/`policy_violation` to Communications Gateway (for User I/O); `status_update` events to IO Component HTTP bridge (fire-and-forget; failures logged but not fatal) |
| **Interfaces With** | Communications Gateway, Policy Enforcer, Plan Executor (M7), Memory Interface, Recovery Manager, IO Component (external — HTTP) |

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
| **Responsibilities** | Tracks every active task and subtask from dispatch to completion or failure. Maintains an in-memory task state map and subtask state map (rehydrated from Memory Component on startup). Enforces task-level timeout: if a task exceeds `timeout_seconds`, signals Recovery Manager to terminate the running agents and return `TIMED_OUT`. **Exception: tasks in `AWAITING_APPROVAL` are exempted from the global timeout while user review is pending** — the Dispatcher's `PLAN_APPROVAL_TIMEOUT_SECONDS` is the authoritative clock for that phase; the Monitor re-checks after a short interval and enforces the backstop once execution resumes. Enforces per-subtask timeouts as specified in the execution plan. Subscribes to `agent_status_update` events from the Agents Component. Detects RECOVERING/TERMINATED events and escalates to Recovery Manager. Emits `task_progress` events when agents publish intermediate progress. Transitions RECOVERING subtasks into a monitored waiting state; timeout remains the safety net if self-recovery does not complete. |
| **Inputs** | Task dispatch confirmation from Task Dispatcher; subtask dispatch confirmation from Plan Executor; `agent_status_update` from Comms Gateway; `timeout_tick` from internal scheduler |
| **Outputs** | `timeout_signal` to Recovery Manager; `task_progress` to Communications Gateway; `state_write` to Memory Interface (on every state change) |
| **Interfaces With** | Task Dispatcher, Plan Executor, Communications Gateway, Recovery Manager, Memory Interface |

#### M5: Recovery Manager

| | |
|---|---|
| **Responsibilities** | Responds to non-nominal parent task events: agent failure, parent-task timeout, policy violation, decomposition failure, Vault/Memory unavailability. If an agent is in `RECOVERING`, the Recovery Manager does not immediately re-dispatch; it trusts the existing agent to self-heal and relies on timeout or a later `TERMINATED` event as the safety net. If an agent is `TERMINATED`, coordinates with Memory Component to retrieve the latest parent `TaskState` before recovery attempt. Issues `agent_terminate`, `task_cancel`, or recovery `task_spec` instructions through Communications Gateway. Escalates irrecoverable failures as `task_failed` messages. Manages retry budget using parent task `retry_count`; does not exceed configurable `max_retries`. Ensures credential revocation is triggered on Vault for terminal recovery outcomes. |
| **Inputs** | `timeout_signal` from Task Monitor; `agent_status_update` (RECOVERING/TERMINATED) from Comms Gateway; `component_failure` event from Memory Interface or Policy Enforcer; `decomposition_timeout` from Task Dispatcher |
| **Outputs** | `agent_terminate`/`task_cancel` to Comms Gateway; `task_failed` to Comms Gateway (for User I/O); `revoke_credentials` to Policy Enforcer; `recovery_audit_event` to Memory Interface |
| **Interfaces With** | Task Monitor, Communications Gateway, Policy Enforcer, Memory Interface |

#### M6: Memory Interface

| | |
|---|---|
| **Responsibilities** | Disciplined persistence gateway — the only module that writes to or reads from the Memory Component. Enforces structured, tagged write payloads: untagged writes are rejected with an error. Serves task state, plan state, and subtask state read requests for deduplication, audit, recovery, and parent-task startup rehydration. Never accepts raw session transcripts — only structured, extracted state is persisted. Manages write retries (up to 3 attempts with exponential backoff) and escalates to Recovery Manager on persistent failure. |
| **Inputs** | Tagged write payloads from: Task Dispatcher, Plan Executor, Policy Enforcer, Task Monitor, Recovery Manager; read requests from: Task Dispatcher (dedup), Recovery Manager (parent task state restore), Task Monitor (parent task startup rehydration), observability/debug surfaces |
| **Outputs** | Persisted state to Memory Component via the Memory Component API/adapter; retrieved state slices to requesting modules; `write_failure` event to Recovery Manager |
| **Interfaces With** | Task Dispatcher, Plan Executor, Policy Enforcer, Task Monitor, Recovery Manager, Memory Component |

#### M7: Plan Executor (NEW in v3.0)

| | |
|---|---|
| **Responsibilities** | Manages execution of structured plans returned by the Planner Agent. Receives an execution plan from the Task Dispatcher. Validates the plan's dependency graph (rejects circular dependencies and empty plans). Resolves subtask dependencies (DAG-based). Dispatches subtasks to the Agents Component in correct topological order via Communications Gateway. Tracks each subtask's state independently (`PENDING`, `DISPATCHED`, `RUNNING`, `COMPLETED`, `FAILED`, `BLOCKED`, `AWAITING_CONFIRMATION`). Passes subtask output as input to dependent subtasks via `prior_results[]` injection. Aggregates final results when all subtasks complete. Signals Task Dispatcher on plan completion or failure. Supports up to `PLAN_EXECUTOR_MAX_PARALLEL` concurrent subtasks. **Multi-step confirmation (NEW v3.3):** When a subtask has `requires_confirmation=true` and its dependencies are met, the Plan Executor transitions it to `AWAITING_CONFIRMATION` and calls `PublishConfirmationRequest` on the Communications Gateway instead of dispatching it. Dispatch is suspended until `HandleConfirmationResponse` receives an explicit user approval via `aegis.orchestrator.task.confirmation_response`. On approval the subtask is reset to `PENDING` and dispatched normally. On rejection it is marked `FAILED` and its dependents are `BLOCKED`. |
| **Inputs** | `execution_plan` from Task Dispatcher; `task_result`/`task_failed` events from Communications Gateway (per subtask); `confirmation_response` events from Communications Gateway (per awaiting subtask) |
| **Outputs** | `task_spec` per subtask to Communications Gateway (for Agents Component); `confirmation_request` to Communications Gateway (for IO Component); `plan_completed`/`plan_failed` to Task Dispatcher; `subtask_state_write` to Memory Interface |
| **Interfaces With** | Task Dispatcher, Communications Gateway, Task Monitor, Memory Interface |

**Key Design Decisions for M7:**

1. **DAG-based dependency resolution:** Subtasks are ordered by `depends_on[]` fields. Subtasks with no dependencies start immediately. Subtasks with dependencies wait until all predecessors complete.
2. **Output piping:** When a subtask completes, its result is injected into dependent subtasks' `task_spec.payload` as `prior_results[]` before those subtasks are dispatched.
3. **Partial failure handling:** If a subtask fails and has no dependents, the plan may continue. If a failed subtask has dependents, those dependents are marked `BLOCKED` and the plan reports partial completion.
4. **Crash recovery boundary:** Plan and subtask state are persisted to Memory Interface on every transition. Current code does not yet rehydrate active `PlanExecutor` instances after restart; persisted records are used for audit/debugging and are the basis for future restart-resume support.
5. **Parallel dispatch:** Up to `PLAN_EXECUTOR_MAX_PARALLEL` subtasks may be dispatched simultaneously when their dependencies are satisfied.
6. **Multi-step confirmation:** Subtasks flagged `requires_confirmation=true` are suspended before dispatch, not before dependency resolution. Dependencies still execute in parallel; the confirmation gate applies only to the individual subtask that requires it.

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
| FR-PE-01 | Every task SHALL be validated against the Vault (OpenBao) policy set before the planner `task.inbound` message is sent to the Planner Agent. Tasks that fail policy validation SHALL be returned as `POLICY_VIOLATION` with a human-readable reason. | MUST |
| FR-PE-02 | The Policy Enforcer SHALL derive a `policy_scope` from the Vault response. This scope defines the ceiling for all subtask dispatches during plan execution and SHALL be attached to the planner `task.inbound` request and to every `task_spec` sent to the Agents Component. | MUST |
| FR-PE-03 | Every policy decision — ALLOW or DENY — SHALL be written to the Memory Component as an `audit_event` with: `user_id`, `task_id`, outcome, timestamp, and `vault_policy_version`. | MUST |
| FR-PE-04 | If the Vault is unreachable and `vault_failure_mode` is `FAIL_CLOSED` (default), the Policy Enforcer SHALL deny the task and return `VAULT_UNAVAILABLE`. If `FAIL_OPEN`, it SHALL proceed with a cached policy (if within `cache_ttl_seconds`) or deny if no cache exists. | MUST |
| FR-PE-05 | The Policy Enforcer SHALL maintain a policy cache with a configurable TTL. Cached policies SHALL be invalidated on any Vault policy update event received via NATS. | SHOULD |

### 5.3 Task Decomposition

| ID | Requirement | Priority |
|---|---|---|
| FR-TD-01 | After policy validation, the Task Dispatcher SHALL send a standard `task.inbound` request to the Agents Component containing planner-specific `instructions`, `required_skills=["general"]`, and correlation metadata. The Orchestrator SHALL NOT attempt to interpret, classify, or decompose the task itself. | MUST |
| FR-TD-02 | The Orchestrator SHALL wait for the planner task's terminal response from the Agents Component within a configurable timeout (`DECOMPOSITION_TIMEOUT_SECONDS`, default: 30). On timeout, the task SHALL be marked `DECOMPOSITION_FAILED` and returned to User I/O. | MUST |
| FR-TD-03 | The planner task's successful `task.result` SHALL contain a structured execution plan: an ordered list of subtasks, each with a `subtask_id`, `required_skill_domains[]`, `action`, `instructions`, `params`, `depends_on[]`, `timeout_seconds`, and optional `requires_confirmation` flag. | MUST |
| FR-TD-04 | The Plan Executor (M7) SHALL validate the execution plan's dependency graph. Circular dependencies SHALL be rejected with `INVALID_PLAN`. Plans with zero subtasks SHALL be rejected with `EMPTY_PLAN`. Plans exceeding `MAX_SUBTASKS_PER_PLAN` SHALL be rejected with `PLAN_TOO_LARGE`. | MUST |
| FR-TD-05 | The Plan Executor SHALL dispatch subtasks in topological order based on `depends_on[]`. Subtasks with no unmet dependencies MAY be dispatched in parallel, up to `PLAN_EXECUTOR_MAX_PARALLEL`. Each subtask SHALL be dispatched as an independent `task_spec` to the Agents Component with its own `policy_scope` (inherited from the parent task, never expanded). | MUST |
| FR-TD-06 | When a subtask completes, the Plan Executor SHALL inject its result into the payload of all dependent subtasks as `prior_results[{subtask_id, result}]` before dispatching them. This enables result piping across the subtask chain. | MUST |
| FR-TD-07 | The Planner Agent SHALL only assign `required_skill_domains[]` values that are within the parent task's `policy_scope`. If the Planner Agent returns a plan with out-of-scope subtasks, the Plan Executor SHALL reject the plan with `SCOPE_VIOLATION`. | MUST |

### 5.3.1 Multi-step Confirmation Requirements (NEW in v3.3)

| ID | Requirement | Priority |
|---|---|---|
| FR-MS-01 | The Planner Agent MAY set `requires_confirmation=true` on any subtask in the execution plan. When set, the Plan Executor SHALL NOT dispatch the subtask to the Agents Component until it receives an explicit user approval for that subtask. | MUST |
| FR-MS-02 | When a subtask with `requires_confirmation=true` has all its dependencies met, the Plan Executor SHALL transition it to `AWAITING_CONFIRMATION` and forward a `ConfirmationRequest` (`plan_id`, `subtask_id`, `task_id`, `action`, `prompt`) to the IO Component via the Communications Gateway. | MUST |
| FR-MS-03 | When the user approves, the IO Component SHALL publish a `ConfirmationResponse` (`confirmed=true`) on `aegis.orchestrator.task.confirmation_response`. The Plan Executor SHALL resume dispatch of the confirmed subtask via the normal `PENDING` → `DISPATCH_PENDING` → `DISPATCHED` path. | MUST |
| FR-MS-04 | When the user rejects, the IO Component SHALL publish a `ConfirmationResponse` (`confirmed=false`, optional `reason`). The Plan Executor SHALL mark the subtask `FAILED` with error code `USER_REJECTED` and block all dependents. | MUST |
| FR-MS-05 | A subtask in `AWAITING_CONFIRMATION` SHALL NOT count toward the `PLAN_EXECUTOR_MAX_PARALLEL` active dispatch limit. | MUST |
| FR-MS-06 | The `AWAITING_CONFIRMATION` state is **not a terminal state**. The task pipeline continues for subtasks that do not require confirmation. The parent task state remains `PLAN_ACTIVE` while any subtask is awaiting confirmation. | MUST |
| FR-MS-07 | If a confirmation request cannot be delivered because the IO bridge is disabled or the HTTP call fails, the Plan Executor SHALL mark that subtask `FAILED` with error code `CONFIRMATION_UNAVAILABLE` and block all dependents. | MUST |

### 5.3.2 Plan Approval Requirements (NEW in m3)

| ID | Requirement | Priority |
|---|---|---|
| FR-PA-01 | After plan validation and when `PLAN_APPROVAL_MODE` is `always`, or `multi` (default) and the plan contains more than one subtask, the Task Dispatcher SHALL transition the task to `AWAITING_APPROVAL` and push a `plan_preview` event to the IO Component before handing the plan to the Plan Executor. | MUST |
| FR-PA-02 | When the user approves via `aegis.orchestrator.plan.decision` (`approved=true`), the Task Dispatcher SHALL transition the task to `PLAN_ACTIVE` and hand the plan to the Plan Executor. | MUST |
| FR-PA-03 | When the user rejects (`approved=false`), the Task Dispatcher SHALL fail the task with error code `PLAN_REJECTED` and return a human-readable explanation to the User I/O Component. | MUST |
| FR-PA-04 | If no decision arrives within `PLAN_APPROVAL_TIMEOUT_SECONDS` (default: 300), the Task Dispatcher SHALL fail the task with error code `PLAN_APPROVAL_TIMEOUT`. | MUST |
| FR-PA-05 | The Task Monitor SHALL NOT enforce the global task timeout while the task is in `AWAITING_APPROVAL`. The `PLAN_APPROVAL_TIMEOUT_SECONDS` timer in the Dispatcher is the authoritative deadline for the approval phase. | MUST |
| FR-PA-06 | When `PLAN_APPROVAL_MODE` is `off`, or the plan has exactly one subtask and mode is `multi`, the Dispatcher SHALL bypass the approval gate and proceed directly to `PLAN_ACTIVE`. | MUST |

### 5.4 Agent Lifecycle Coordination

| ID | Requirement | Priority |
|---|---|---|
| FR-ALC-01 | Before dispatching each subtask, the Plan Executor SHALL query the Agents Component's capability endpoint to determine if a capable agent exists, before requesting provisioning. | MUST |
| FR-ALC-02 | The Orchestrator SHALL NOT dictate how the Agents Component provisions agents. It SHALL only send a fully-formed `task_spec` (including `policy_scope`) and wait for a `task_accepted` or `provisioning_error` response. | MUST |
| FR-ALC-03 | The Orchestrator SHALL confirm `task_accepted` to the User I/O Component within **5 seconds** (existing agent path) or within **35 seconds** (new agent, full provisioning path, including decomposition). Confirmed tasks receive an `estimated_completion_at` field. | MUST |
| FR-ALC-04 | The Orchestrator SHALL publish `capability_query` requests to the Agents Component on `aegis.agents.capability.query` and wait for responses on `aegis.orchestrator.capability.response` before dispatching subtasks. | MUST |
| FR-ALC-05 | The Orchestrator SHOULD forward intermediate agent progress updates (per subtask) to the User I/O Component if a `user_context_id` is associated with the task. Current implementation pushes parent-task milestone status updates; per-subtask progress streaming is future work. | SHOULD |

### 5.5 Self-Healing & Recovery

| ID | Requirement | Priority |
|---|---|---|
| FR-SH-01 | The Task Monitor SHALL enforce the parent task timeout. When the parent task's `timeout_seconds` is exceeded, the Task Monitor SHALL signal the Recovery Manager and the task SHALL be returned as `TIMED_OUT` when recovery terminates it. | MUST |
| FR-SH-02 | On receiving an `agent_status_update` with `state=RECOVERING`, the Recovery Manager SHALL keep monitoring the existing agent rather than immediately provisioning a duplicate agent. A later timeout or TERMINATED event is the safety net. | MUST |
| FR-SH-03 | On receiving an `agent_status_update` with `state=TERMINATED`, the Recovery Manager SHALL retrieve the latest parent `TaskState` from Memory, verify the original scope is still valid, increment parent `retry_count`, and re-dispatch within `max_task_retries` (default: 3). | MUST |
| FR-SH-04 | On terminal recovery outcomes, the Recovery Manager SHALL trigger credential revocation via the Policy Enforcer for the associated task. | MUST |
| FR-SH-05 | On Orchestrator startup, the Task Monitor SHALL rehydrate its active parent task map from `task_state` records in the Memory Component and resume parent-task timeout monitoring. Plan/subtask restart-resume is not implemented in the current code. | MUST |
| FR-SH-06 | Recovery decisions SHALL be policy-checked with the Vault before re-dispatching. A re-dispatched task cannot receive a broader scope than the original `policy_scope`. | MUST |

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
| NFR-04 | Performance | Capability query round-trip with Agents Component | Bounded by `CapabilityQueryTimeout` (3s in current code) |
| NFR-05 | Performance | Decomposition round-trip to Planner Agent | < 30 seconds |
| NFR-06 | Reliability | Task state must survive Orchestrator node failure without data loss | Zero data loss |
| NFR-07 | Reliability | Startup rehydration from Memory must complete before accepting new tasks | < 10 seconds |
| NFR-08 | Reliability | Recovery success rate for transient agent failures | > 95% |
| NFR-09 | Security | All policy decisions logged with 100% coverage | 100% audit coverage |
| NFR-10 | Security | Vault unreachable: default behavior | FAIL_CLOSED (no task dispatched) |
| NFR-11 | Security | The Orchestrator SHALL NOT contain any LLM or inference engine | 100% LLM-free |
| NFR-12 | Scalability | Concurrent tasks managed without race conditions | 1 to 10,000+ tasks |
| NFR-13 | Scalability | Orchestrator horizontal scale-out | Durable task records are persisted externally; active plan execution is process-local in current code and requires external routing/ownership before true multi-instance active-active operation |
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
3. Task Dispatcher publishes a standard `task.inbound` message to `aegis.agents.task.inbound` via the Communications Gateway. The payload uses `required_skills: ["general"]`, planner-specific `instructions`, and the `trace_id` carried from the inbound envelope/context.
4. The Agents Component provisions or reuses a general-purpose agent and runs the planner task.
5. The planner agent decomposes the task and returns the execution plan through the normal `aegis.orchestrator.task.result` subject. On provisioning / execution failure it returns `aegis.orchestrator.task.failed`.
6. Task Dispatcher parses the planner result into `ExecutionPlan` JSON, then validates the plan: required `plan_id`, matching `parent_task_id`, non-empty/size-bounded subtask list, unique and non-empty `subtask_id`, required `action`, required `instructions`, positive `timeout_seconds`, valid `depends_on[]` references, circular dependencies, and subtask scope violations.
7. If the plan is invalid, Task Dispatcher returns `DECOMPOSITION_FAILED` or `INVALID_PLAN` to User I/O.
8. If the plan is valid, Task Dispatcher passes it to the Plan Executor (M7) and persists the plan state (`PLAN_ACTIVE`) to Memory Interface.

### Flow 4: Plan Execution — Subtask Capability Query & Dispatch
1. Plan Executor identifies subtasks with no unmet dependencies (initially, those with empty `depends_on[]`).
2. For each ready subtask, Plan Executor sends `capability_query` to Agents Component on `aegis.agents.capability.query`.
3. Agents Component returns `capability_response`: `match | partial_match | no_match`.
4. Plan Executor assembles an internal `task_spec`, which the Communications Gateway translates into `task.inbound` and publishes to `aegis.agents.task.inbound`.
5. Agents Component responds with `task.accepted (agent_id, estimated_completion)` or `task.failed`.
6. Plan Executor persists subtask state (`DISPATCH_PENDING`) to Memory Interface **before** publishing `task_spec`. After successful publish, it persists `DISPATCHED`. If publish fails, it persists `DELIVERY_FAILED` for that subtask.

### Flow 5: Subtask Monitoring, Result Piping, and Completion
1. Communications Gateway subscribes to `agent.status` events on `aegis.orchestrator.agent.status` and routes them to Task Monitor for parent-task recovery decisions.
2. Per-subtask result handling is owned by Plan Executor. Status pushes to IO are emitted at parent-task milestones; fine-grained streaming of every intermediate agent progress update is not implemented in the current code.
3. On `task_result` from Agents Component for a subtask: Plan Executor writes COMPLETED state for that subtask to Memory, checks if any dependent subtasks are now ready, and injects the result into their `prior_results[]` before dispatching them.
4. When all subtasks reach terminal states, Plan Executor aggregates results and signals `plan_completed` to Task Dispatcher.
5. Task Dispatcher writes COMPLETED, PARTIAL_COMPLETE, or FAILED state for the parent task and delivers the aggregated result or error to User I/O via `callback_topic`.
6. On parent task timeout: Task Monitor signals Recovery Manager, which terminates/reports the parent task as `TIMED_OUT` through the existing recovery path.

### Flow 6.5: IO Component Status Push (NEW in v3.1)

The Orchestrator pushes real-time status events to the IO Component over HTTP so the web dashboard can display live task progress. This path is **optional and fire-and-forget** — IO unavailability does not affect task execution.

1. Task Dispatcher persists task state `DECOMPOSING` → pushes `{ type: "status", payload: { taskId, status: "working", lastUpdate: "Planning your task...", expectedNextInputMinutes: 2 } }` to `POST /api/orchestrator/stream-events` on the IO Component.
2. When the plan is validated and handed to the Plan Executor → Dispatcher pushes `{ status: "working", lastUpdate: "Executing N subtasks...", expectedNextInputMinutes: N/2 }`.
3. When `HandlePlanComplete` fires → Dispatcher pushes `{ status: "completed", lastUpdate: "Task complete", expectedNextInputMinutes: 0 }`.
4. When `HandlePlanFailed` or `failTask` fires → Dispatcher pushes `{ status: "completed", lastUpdate: "<human-readable error or partial-completion message>", expectedNextInputMinutes: 0 }`.
5. When a `credential.request` message with `operation: "user_input"` is received from the Agents Component via NATS → Communications Gateway forwards it to IO Component as `{ type: "credential_request", payload: { taskId, requestId, keyName, label } }`. The IO dashboard surfaces a credential input modal; the user's submitted value goes directly to the Memory vault — the Orchestrator never sees the plaintext credential.

**State → IO status mapping:**

| Orchestrator State | IO `status` value |
|---|---|
| DECOMPOSING, PLAN_ACTIVE | `working` |
| PLAN_ACTIVE (any subtask in `AWAITING_CONFIRMATION`) | `awaiting_feedback` — also triggers `confirmation_request` event |
| COMPLETED | `completed` |
| FAILED, PARTIAL_COMPLETE, DECOMPOSITION_FAILED, POLICY_VIOLATION, TIMED_OUT | `completed` (with error detail in `lastUpdate`) |

### Flow 6.6: Multi-step Confirmation Flow (NEW in v3.3)

When the Planner Agent marks a subtask `requires_confirmation=true`, the Plan Executor pauses dispatch and requests user approval via the IO Component before executing the action.

1. Plan Executor's `dispatchReadySubtasks` identifies a `PENDING` subtask with all dependencies met and `requires_confirmation=true` (and not yet confirmed in `confirmedSubtasks` map).
2. Plan Executor transitions subtask state to `AWAITING_CONFIRMATION` and persists to Memory Interface.
3. Plan Executor calls `gw.PublishConfirmationRequest(ctx, ConfirmationRequest{plan_id, subtask_id, task_id, action, prompt})`.
4. Communications Gateway calls the registered `ConfirmationRequestHandler` callback.
5. In `main.go`: handler calls `ioClient.PushStatus(taskID, "awaiting_feedback", ...)` then `ioClient.PushConfirmationRequest({planId, subtaskId, taskId, action, prompt})`.
6. IO Component receives `{"type": "confirmation_request", "payload": {...}}` via HTTP and displays a confirmation modal to the user.
7. **User approves:**
   - IO Component publishes `ConfirmationResponse{confirmed: true}` wrapped in a `MessageEnvelope` on `aegis.orchestrator.task.confirmation_response`.
   - Communications Gateway's `handleRawConfirmationResponse` deserializes it and calls `confirmationResponseHandler`.
   - Plan Executor `HandleConfirmationResponse`: marks subtask confirmed in `confirmedSubtasks`, resets state to `PENDING`, persists, and calls `dispatchReadySubtasks`.
   - Subtask is dispatched normally via capability query → `task.inbound`.
8. **User rejects:**
   - IO Component publishes `ConfirmationResponse{confirmed: false, reason: "..."}`.
   - Plan Executor marks subtask `FAILED` with `error_code: USER_REJECTED`. Dependent subtasks are marked `BLOCKED`. `checkPlanCompletion` runs.

**IO Component contract (what the IO team must implement):**
- Handle incoming `{"type": "confirmation_request"}` events from `POST /api/orchestrator/stream-events`.
- Show the user a modal with `action` (short label) and `prompt` (full explanation).
- On user decision, publish to NATS `aegis.orchestrator.task.confirmation_response` with:

```json
{
  "message_id": "<uuid>",
  "message_type": "confirmation_response",
  "source_component": "io",
  "correlation_id": "<subtask_id>",
  "timestamp": "<ISO8601>",
  "schema_version": "1.0",
  "payload": {
    "plan_id": "<plan_id>",
    "subtask_id": "<subtask_id>",
    "task_id": "<task_id>",
    "confirmed": true,
    "reason": ""
  }
}
```

### Flow 6: Parent Task Recovery Flow
1. Agent RECOVERING event received from Agents Component for a task.
2. Recovery Manager treats `RECOVERING` as a self-healing signal and does not immediately re-dispatch. The existing agent may return to `ACTIVE`; otherwise timeout remains the safety net.
3. If the agent later reports `TERMINATED`, Recovery Manager checks retry count. If count < `max_task_retries`:
   - Memory Interface read for the latest parent `TaskState`.
   - Vault checked (policy still valid for recovery scope).
   - Recovery `task_spec` re-dispatched to Agents Component using the same `policy_scope`.
4. If count >= `max_task_retries`: parent task is marked FAILED and an error is delivered to User I/O.

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

### 8.3 Self-Healing Recovery Flow (Parent Task)

*Triggered when: `agent_status_update {state: RECOVERING}` or later `agent_status_update {state: TERMINATED}` is received from Agents Component for an agent associated with the task*

1. Task Monitor receives `agent_status_update {agent_id, state: RECOVERING, task_id}`
2. Task Monitor → Recovery Manager: `recovery_signal {task_id, retry_count, policy_scope, reason: AGENT_RECOVERING}`
3. Recovery Manager logs the self-healing condition and does not immediately re-dispatch.
4. If the agent later reports `TERMINATED`, Task Monitor → Recovery Manager: `recovery_signal {task_id, retry_count, policy_scope, reason: AGENT_TERMINATED}`
5. Recovery Manager checks parent `retry_count`: if < `max_task_retries` → proceed; else → FAIL the parent task
6. Recovery Manager → Memory Interface: read latest `task_state` snapshot `{task_id, latest}`
7. Memory Interface → Recovery Manager: `task_state {policy_scope, retry_count, task payload metadata}`
8. Recovery Manager → Policy Enforcer: re-validate policy scope `{task_id, policy_scope}`
9. Policy Enforcer → OpenBao: verify scope still valid
10. OpenBao → Policy Enforcer: VALID (or EXPIRED → Recovery Manager escalates to FAIL)
11. Recovery Manager → Comms Gateway: re-dispatch recovery `task_spec` to `aegis.agents.task.inbound`
12. Recovery Manager → Memory Interface: write `recovery_event {task_id, attempt_n, timestamp}`
13. Agents Component responds with `task_accepted` — monitoring resumes at Task Monitor

> **🔴 CRITICAL:** On `max_retries` exceeded for the parent task: Recovery Manager issues termination/cancel as needed, writes FAILED state for the task, triggers credential revocation, and delivers a terminal error response.

### 8.4 Policy Violation Flow

*Triggered when: Policy Enforcer receives DENY from OpenBao*

1. Task Dispatcher → Policy Enforcer: `policy_check_request`
2. Policy Enforcer → OpenBao: token create request
3. OpenBao → Policy Enforcer: `{allowed: false, reason: 'user policy does not permit this operation'}`
4. Policy Enforcer → Memory Interface: write `audit_event {DENY, task_id, user_id, reason, timestamp}`
5. Policy Enforcer → Task Dispatcher: `policy_result {DENIED, reason}`
6. Task Dispatcher → Comms Gateway: `policy_violation_response {task_id, error_code: POLICY_VIOLATION, user_message: '...'}`
7. Comms Gateway → User I/O: `policy_violation` delivered to `callback_topic`

**No decomposition request sent. No agent is provisioned. No credential is issued. The `policy_scope` remains at zero-trust.**

### 8.5 Decomposition Failure Flow (NEW in v3.0)

*Triggered when: Planner Agent does not respond within `DECOMPOSITION_TIMEOUT_SECONDS`, OR Planner Agent returns an invalid plan*

1. Task Dispatcher publishes planner `task.inbound` at T=0.
2. Task Dispatcher starts timer for `DECOMPOSITION_TIMEOUT_SECONDS` (default 30s).
3. Timer fires without planner `task.result` received.
4. Task Dispatcher → Memory Interface: write `task_state {DECOMPOSITION_FAILED, reason: TIMEOUT}`
5. Task Dispatcher → Comms Gateway: `task_failed {task_id, error_code: DECOMPOSITION_TIMEOUT, user_message: '...'}`
6. Comms Gateway → User I/O: `task_failed` delivered to `callback_topic`

**Alternative path:** Planner Agent responds but plan is invalid:

4. Task Dispatcher receives planner `task.result`.
5. Plan validation detects circular deps / empty plan / scope violation.
6. Task Dispatcher → Memory Interface: write `task_state {DECOMPOSITION_FAILED, reason: INVALID_PLAN}`
7. Task Dispatcher → Comms Gateway: `task_failed {task_id, error_code: INVALID_PLAN, user_message: '...'}`

---

# PART V — Data Models & State Machine

## 9. Task Lifecycle State Machine

The Task Monitor owns parent task monitoring and parent task state transitions it performs. The Plan Executor owns subtask state transitions.

### 9.1 Top-Level Task States

| State | Description | Entry Condition | Valid Transitions |
|---|---|---|---|
| RECEIVED | Task arrived, schema validated | `user_task` passes envelope validation | → DEDUP_CHECK → POLICY_CHECK → REJECTED (schema fail) |
| POLICY_CHECK | Awaiting Vault policy validation | Passed dedup check; `policy_check_request` sent | → DECOMPOSING (ALLOW) → POLICY_VIOLATION (DENY) |
| DECOMPOSING | Awaiting plan from Planner Agent | `policy_result` ALLOWED; `task_decomposition_request` sent | → AWAITING_APPROVAL (plan valid, approval required) → PLAN_ACTIVE (plan valid, no approval needed) → DECOMPOSITION_FAILED |
| AWAITING_APPROVAL | Plan produced; waiting for user to approve or reject | Valid plan received AND `PLAN_APPROVAL_MODE` requires approval | → PLAN_ACTIVE (user approves) → FAILED (`PLAN_REJECTED`) → FAILED (`PLAN_APPROVAL_TIMEOUT`) |
| PLAN_ACTIVE | Plan Executor dispatching subtasks | Plan approved (or approval not required) | → COMPLETED (all subtasks done) → FAILED → TIMED_OUT → PARTIAL_COMPLETE |
| DECOMPOSITION_FAILED | Planner Agent timed out or returned invalid plan | Decomposition timeout OR plan validation failed | **Terminal.** No agents touched. |
| TIMED_OUT | Parent task exceeded `timeout_seconds` | Task Monitor `timeout_signal` fires | **Terminal.** All running subtask agents terminated; credentials revoked. |
| POLICY_VIOLATION | Task denied by policy validation | `policy_result` DENIED from Policy Enforcer | **Terminal.** No decomposition, no agent touched. |
| FAILED | All subtasks failed or blocking failure occurred | Plan Executor reports `plan_failed` | **Terminal.** Credentials revoked; audit written. |
| PARTIAL_COMPLETE | Some subtasks completed, some failed (no blocking) | Plan Executor reports partial success | **Terminal.** Partial results delivered to User I/O. |
| COMPLETED | All subtasks completed successfully | Plan Executor reports `plan_completed` | **Terminal.** Credentials revoked; aggregated result delivered. |

### 9.2 Subtask States (Managed by Plan Executor)

| State | Description | Entry Condition | Valid Transitions |
|---|---|---|---|
| PENDING | Subtask waiting for dependencies to complete | Subtask has unmet `depends_on[]` | → AWAITING_CONFIRMATION (requires_confirmation=true, deps met) → DISPATCH_PENDING (deps met, no confirmation needed) → BLOCKED (dep failed) |
| AWAITING_CONFIRMATION | Subtask suspended pending explicit user approval | `requires_confirmation=true` and all dependencies are COMPLETED | → PENDING (user confirms) → FAILED (user rejects, `USER_REJECTED`; request delivery fails, `CONFIRMATION_UNAVAILABLE`) |
| DISPATCH_PENDING | Subtask state persisted before agent publish | Dependencies met (and confirmed if required); pre-dispatch persistence succeeded | → DISPATCHED → DELIVERY_FAILED |
| DISPATCHED | `task_spec` successfully published; `task_accepted` confirmed | Agent publish succeeded | → RUNNING → RECOVERING → TIMED_OUT |
| RUNNING | Agent actively executing the subtask | Agent ACTIVE confirmed by Agents Component | → COMPLETED → RECOVERING → TIMED_OUT |
| RECOVERING | Agent self-healing; Orchestrator monitoring | `agent_status_update` RECOVERING received | → RUNNING → FAILED → TIMED_OUT |
| COMPLETED | Subtask result received | `task_result` received from Agents Component | **Terminal.** Result piped into dependents. |
| FAILED | Max retries exceeded, provisioning failure, or user rejection | Recovery Manager gives up or `confirmed=false` received | **Terminal.** Credentials revoked. Dependents → BLOCKED. |
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

Returned by the Planner Agent in the normal `task.result` payload. Stored as `plan_state` in Memory Component.

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
| `action` | string | Human-readable action label (e.g., `"search_flights"`, `"find_hotels"`). Also shown as the modal title in the IO confirmation UI. |
| `instructions` | string | Natural-language instruction for the executing agent. Written by the Planner Agent. Also used as the confirmation prompt text shown to the user. |
| `params` | object | Subtask-specific parameters extracted by the Planner Agent. |
| `depends_on` | string[] | List of `subtask_ids` that must complete before this subtask can start. Empty = no dependencies. |
| `timeout_seconds` | integer | Per-subtask timeout. Must not exceed parent task's remaining time. |
| `requires_confirmation` | boolean | **NEW v3.3.** Optional. Default `false`. When `true`, the Plan Executor pauses dispatch and requests explicit user approval from the IO Component before executing this subtask. Use for high-impact, irreversible actions (e.g., `send_email`, `delete_file`, `make_payment`). |

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

### 11.3.1 Inbound: Orchestrator ← IO Component (NEW in v3.3)

| Message Type | NATS Topic | Trigger |
|---|---|---|
| `confirmation_response` | `aegis.orchestrator.task.confirmation_response` | User approves or rejects a subtask confirmation prompt in the IO dashboard |

**Payload (wrapped in standard `MessageEnvelope`):**

| Field | Type | Description |
|---|---|---|
| `plan_id` | UUID | Plan containing the subtask awaiting confirmation. |
| `subtask_id` | string | The subtask the user is responding to. |
| `task_id` | UUID | Parent user task. |
| `confirmed` | boolean | `true` = approved; `false` = rejected. |
| `reason` | string | Optional. Human-readable rejection reason from the user. |

### 11.4 Planner `task.inbound` Payload Schema

The planner call uses the same agents-component wire schema as every other task:

| Field | Type | Description |
|---|---|---|
| `task_id` | UUID | The orchestrator-generated planner task ID. In implementation this is the top-level `orchestrator_task_ref`. |
| `required_skills` | string[] | Always `["general"]` for decomposition. |
| `instructions` | string | Planner-specific prompt containing the raw user task, the required `ExecutionPlan` JSON schema, and scope constraints. |
| `metadata` | object | Optional flat string map. Includes `orchestrator_task_ref`, `user_id`, `callback_topic`, and planner markers. |
| `trace_id` | string | Distributed trace ID propagated from the inbound envelope/context. The gateway prefers explicit `TaskSpec.TraceID`; otherwise it uses the trace ID already present on the context. |
| `user_context_id` | UUID \| null | Optional user context identifier. |

### 11.5 Planner Result Handling

The planner returns through the normal agents-component terminal subjects:

| Subject | Behavior |
|---|---|
| `aegis.orchestrator.task.result` | `payload.result` contains the execution plan as JSON (or a JSON string). The Dispatcher parses it into `ExecutionPlan` and validates it. |
| `aegis.orchestrator.task.failed` | Planner provisioning / execution failed. The Dispatcher marks the top-level task `DECOMPOSITION_FAILED` / `AGENTS_UNAVAILABLE`. |

### 11.6 Outbound: Orchestrator → IO Component (NEW in v3.1)

The Orchestrator pushes events to the IO Component over HTTP when `IO_API_BASE` is configured. Status updates and credential requests are best-effort: when `IO_API_BASE` is not set, those calls are no-ops and the Orchestrator continues without a connected UI.

Confirmation requests are different because they gate whether a subtask may be dispatched. If a subtask requires confirmation and the IO bridge is disabled or the HTTP call fails, the Plan Executor marks that subtask `FAILED` with `error_code=CONFIRMATION_UNAVAILABLE` and blocks dependents.

**Endpoint:** `POST {IO_API_BASE}/api/orchestrator/stream-events`

**Content-Type:** `application/json`

**Event envelope — status update:**

```json
{
  "type": "status",
  "payload": {
    "taskId": "<user task_id>",
    "status": "working | awaiting_feedback | completed",
    "lastUpdate": "<human-readable progress description>",
    "expectedNextInputMinutes": 2,
    "timestamp": 1712345678901
  }
}
```

**Event envelope — credential request:**

```json
{
  "type": "credential_request",
  "payload": {
    "taskId": "<user task_id>",
    "requestId": "<unique request ID from agent>",
    "keyName": "<vault key name>",
    "label": "<human-readable label for the UI modal>"
  }
}
```

**Event envelope — confirmation request (NEW v3.3):**

```json
{
  "type": "confirmation_request",
  "payload": {
    "planId": "<plan_id>",
    "subtaskId": "<subtask_id>",
    "taskId": "<user task_id>",
    "action": "<action label, e.g. send_email>",
    "prompt": "<full instructions text shown to user>"
  }
}
```

When this event is received, the IO Component MUST display a confirmation modal with an **Approve** and a **Reject** button. On decision, the IO Component publishes the response to NATS (see §11.3 and §11.9).

**Error handling:** Status and credential HTTP errors are logged but not propagated. Confirmation-request HTTP errors are propagated to Plan Executor because the user decision is required before dispatch. The 5-second HTTP timeout prevents IO unavailability from blocking indefinitely.

**Credential routing rule:** Only `credential.request` messages from the Agents Component with `operation: "user_input"` are forwarded to IO. Vault pre-authorization (`authorize`) and revocation (`revoke`) operations are handled internally and never surface in the IO UI.

### 11.7 Outbound: Policy Enforcer → Vault (OpenBao)

**Pre-authorization call:** `POST /v1/auth/token/create`
- Policies: derived from the user's permission profile
- Token TTL: `task timeout_seconds + 300 seconds` buffer. Hard max: 86,700 seconds.
- Token type: service token (supports revocation).
- Metadata attached: `{ user_id, task_id, orchestrator_task_ref, issued_at }`.

**Revocation call:** `POST /v1/auth/token/revoke`
- Triggered by Recovery Manager on every terminal task and subtask outcome.
- On Vault unavailability: Recovery Manager logs `REVOCATION_FAILED` critical event and schedules retry. Does not block task termination.

### 11.8 Outbound: Memory Interface → Memory Component

All interactions are mediated by M6 (Memory Interface). **Direct Memory Component database calls from other modules are prohibited.**

- **Write:** via the Memory Component's write API/adapter — body: `OrchestratorMemoryWritePayload {orchestrator_task_ref, task_id, plan_id, subtask_id, data_type, timestamp, payload, ttl_seconds}`
- **Read:** via the Memory Component's read API/adapter — query params: `orchestrator_task_ref OR task_id OR plan_id OR subtask_id`, `data_type`, `from_timestamp`, `to_timestamp`
- **Read returns:** array of matching payload objects, ordered by timestamp ascending
- **Supported `data_type` values:** `task_state | plan_state | subtask_state | audit_log | recovery_event | policy_event`

### 11.9 Communications Component (NATS) — Topic Hierarchy

| Topic | Direction | Delivery | Max Payload |
|---|---|---|---|
| `aegis.orchestrator.tasks.inbound` | INBOUND | At-least-once | 1 MB |
| `aegis.orchestrator.task.accepted` | INBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.task.result` | INBOUND | At-least-once | 2 MB |
| `aegis.orchestrator.task.failed` | INBOUND | At-least-once | 32 KB |
| `aegis.orchestrator.agent.status` | INBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.capability.response` | INBOUND | At-most-once | 16 KB |
| `aegis.orchestrator.credential.request` | INBOUND | At-least-once | 8 KB |
| `aegis.orchestrator.task.confirmation_response` | INBOUND | At-least-once | 8 KB |
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
| Agents Component unreachable | NATS publish failure or timeout on `capability_query` / planner task response | Current implementation makes a single request and returns `AGENTS_UNAVAILABLE` / decomposition failure on timeout or publish error. Retry/backoff is not implemented for these paths. |
| Memory write fails on dispatch | Memory Interface returns error | Retry up to 3 times. On persistent failure: abort dispatch, return `STORAGE_UNAVAILABLE`. Ensure no orphaned agent exists. |
| `user_task` schema invalid | JSON schema validation | Return `INVALID_TASK_SPEC` immediately. No Vault query, no decomposition, no agent interaction. |
| Duplicate `task_id` received | Dedup check in Memory Interface | Return `DUPLICATE_TASK` with current task status. Do not spawn agent. |
| User rejects a subtask confirmation prompt | `confirmation_response.confirmed=false` received by Plan Executor | Subtask marked `FAILED` with `error_code: USER_REJECTED`. Dependent subtasks marked `BLOCKED`. Plan reports partial or failed. |
| Subtask confirmation request not delivered (IO down or handler unregistered) | `PushConfirmationRequest` returns error | Subtask immediately failed with `CONFIRMATION_UNAVAILABLE`. Dependents `BLOCKED`. Plan proceeds to partial/failed evaluation without waiting for a response that can never arrive. |
| User rejects an entire plan | `plan.decision.approved=false` received by Task Dispatcher | Task failed with `PLAN_REJECTED`. Human-readable message returned to User I/O. No agent is ever dispatched. |
| Plan approval timed out | `PLAN_APPROVAL_TIMEOUT_SECONDS` elapsed with no `plan.decision` | Task failed with `PLAN_APPROVAL_TIMEOUT`. User is prompted to resubmit. |

### 14.2 Runtime Failures

| Failure Scenario | Detection | Response |
|---|---|---|
| Parent task timeout exceeded | Task Monitor `timeout_at` tick | Signal Recovery Manager → terminate/report parent task → credentials revoked on terminal recovery path → `TIMED_OUT` returned to User I/O. |
| Subtask timeout exceeded | Subtask `timeout_at` persisted by Plan Executor | Per-subtask timeout enforcement is not implemented in the current Task Monitor. Parent task timeout remains the active safety net. |
| Agent enters RECOVERING | `agent_status_update` event | Recovery Manager trusts existing agent self-healing and keeps monitoring. A later TERMINATED event or parent timeout triggers recovery/termination. |
| Agent TERMINATED | `agent_status_update` event | Recovery Manager reads latest parent `TaskState`, verifies scope, increments parent `retry_count`, and re-dispatches if below `max_task_retries`; otherwise marks the parent task FAILED. |
| Orchestrator node crash | Process termination | On restart: Task Monitor rehydrates active parent tasks from Memory and resumes parent timeout tracking. Persisted plan/subtask records remain available for audit/debugging, but active plan execution is not reconstructed in current code. |
| Vault revocation fails | Revocation HTTP error | Log `REVOCATION_FAILED` critical event. Schedule retry with exponential backoff (max 5 attempts). Do not block task termination. |
| NATS message delivery failure | Comms Gateway NACK or timeout | Apply NATS at-least-once retry. Deduplicate on receiver side using `message_id`. Dead-letter after `max_redelivery`. |

---

## 15. Observability Design

The Orchestrator is designed to be **fully observable without requiring access to task payload content**.

### 15.1 Structured Logging

Every log line emitted by the Orchestrator is structured JSON written to stdout via Go's `log/slog` package. Promtail scrapes Docker container stdout and ships log lines to Loki.

**Required fields on every log line:**

| Field | Source | Notes |
|---|---|---|
| `timestamp` | slog (automatic) | RFC3339Nano UTC |
| `level` | slog (automatic) | `debug`, `info`, `warn`, `error` |
| `component` | always `"orchestrator"` | constant |
| `node_id` | `NODE_ID` env var | identifies the instance |
| `module` | context (`WithModule`) | e.g. `task_dispatcher`, `plan_executor` |
| `trace_id` | context (`WithTraceID`) | present on all task-scoped log lines |
| `task_id` | context (`WithTaskID`) | present when processing a task |
| `plan_id` | context (`WithPlanID`) | present during plan execution |
| `subtask_id` | context (`WithSubtaskID`) | present during subtask dispatch |
| `message` | slog (automatic) | human-readable description |

All context fields are injected via `observability.LogFromContext(ctx)`, which reads the context keys set by `observability.WithTraceID`, `WithTaskID`, `WithPlanID`, `WithSubtaskID`, and `WithModule`.

> **The following MUST NEVER appear in any log line or span attribute:** raw user input (`payload.raw_input`), credential values, task result payloads, planner output, or policy scope tokens.

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
| `orchestrator_decomposition_latency_seconds` | Histogram | Time from planner `task.inbound` sent to response received. |
| `orchestrator_policy_check_latency_ms` | Histogram | Vault round-trip time per policy check. |
| `orchestrator_active_tasks` | Gauge | Current number of tasks in non-terminal states. |
| `orchestrator_active_plans` | Gauge | Current number of plans being executed. |
| `orchestrator_active_subtasks` | Gauge | Current number of subtasks in non-terminal states. |
| `orchestrator_vault_available` | Gauge (0/1) | 1 = Vault reachable. 0 = Vault unreachable. |

### 15.3 Distributed Tracing

Each task generates a trace span tree using the OpenTelemetry Go SDK (`go.opentelemetry.io/otel`). Spans are exported via OTLP gRPC to Grafana Tempo. The `trace_id` is stamped on every outbound NATS `MessageEnvelope` and extracted from every inbound envelope so the trace is continuous across component boundaries.

**Span tree per task:**

```
task_received          (Gateway — root span, created on inbound task receipt)
  dedup_check          (Dispatcher — Memory read for deduplication)
  policy_validation    (Dispatcher — Vault policy check via Policy Enforcer)
  decomposition        (Dispatcher — planner task.inbound publish)
  plan_execution       (Executor — parent span for full plan)
    subtask_dispatch   (Executor — one child span per subtask dispatched)
  result_delivery      (Dispatcher — final result push to User I/O / IO Component)
  recovery_attempt     (Recovery Manager — child span when a task recovery re-dispatch is attempted)
```

**Span attributes set on key spans:**

| Span | Attributes |
|---|---|
| `task_received` | `task_id`, `user_id` |
| `dedup_check` | `task_id` |
| `policy_validation` | `task_id`, `user_id` |
| `decomposition` | `task_id`, `user_id` |
| `plan_execution` | `task_id`, `user_id` |
| `subtask_dispatch` | `task_id`, `user_id` |
| `result_delivery` | `task_id`, `user_id` |
| `recovery_attempt` | `task_id`, `user_id` |

Errors are recorded via `span.RecordError(err)` + `span.SetStatus(codes.Error, ...)`. Raw user input, credential values, and task payloads are never set as span attributes.

The `InitTracer` function in `internal/observability/tracing.go` configures the OTLP gRPC exporter with retry buffering. If Tempo is unreachable at startup, the Orchestrator continues — the exporter retries in the background.

### 15.4 Observability Stack

The following services are included in the repository root `docker-compose.yml` and start alongside the Orchestrator:

| Service | Image | Port | Role |
|---|---|---|---|
| Loki | `grafana/loki:3.4.2` | 3100 | Log aggregation backend |
| Promtail | `grafana/promtail:3.4.2` | no host port; container scrapes Docker stdout | Scrapes Docker container stdout → Loki |
| Tempo | `grafana/tempo:2.4.1` | 4317 (OTLP gRPC), 3200 (HTTP) | Distributed trace backend |
| Grafana | `grafana/grafana:latest` | 3000 | Unified UI — Loki + Tempo with log→trace linking |

Grafana is auto-provisioned (no manual configuration required) with:
- Loki data source as default, with a derived field that links `trace_id` values in log lines to the matching Tempo trace waterfall.
- Tempo data source with traces-to-logs linking (±5 min window around each span).

Promtail uses Docker service discovery and a JSON pipeline stage that promotes orchestrator log fields such as `level`, `component`, `module`, `trace_id`, and `task_id` for querying in Loki/Grafana.

### 15.5 Debug Trace Endpoint

`GET /debug/trace/{trace_id}` — available on the Orchestrator's HTTP server (`:8080`).

Queries Loki `query_range` for all log lines tagged with the given `trace_id` in the last hour, flattens the Loki streams into a single chronological list, and returns:

```json
{
  "trace_id": "<id>",
  "count": 12,
  "entries": [
    { "timestamp": "...", "level": "info", "module": "comms_gateway", "message": "user task received" },
    ...
  ]
}
```

This endpoint is intended for demo and development use. It does not require Grafana access.

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
| `NATS_CREDS_PATH` | path | _(empty)_ | Path to NATS credentials file. Optional — omit when NATS does not require authentication. |
| `MEMORY_ENDPOINT` | URL | Required | Memory Component write/read API. |
| `MAX_TASK_RETRIES` | integer | 3 | Max recovery re-dispatches per parent task. |
| `TASK_DEDUP_WINDOW_SECONDS` | integer | 300 | Deduplication window for `task_id` reuse detection. |
| `DECOMPOSITION_TIMEOUT_SECONDS` | integer | 30 | Max time to wait for Planner Agent's decomposition response. |
| `MAX_SUBTASKS_PER_PLAN` | integer | 20 | Maximum subtasks allowed in a single execution plan. |
| `PLAN_EXECUTOR_MAX_PARALLEL` | integer | 5 | Max subtasks the Plan Executor may dispatch simultaneously. |
| `HEALTH_CHECK_INTERVAL_SECONDS` | integer | 10 | Interval for dependency health checks. |
| `METRICS_EMIT_INTERVAL_SECONDS` | integer | 15 | How frequently metrics are published to NATS. |
| `QUEUE_HIGH_WATER_MARK` | integer | 500 | Pending task queue depth that triggers `queue_pressure` metric. |
| `MEMORY_WRITE_BUFFER_SECONDS` | integer | 30 | How long to buffer writes locally if Memory Component is unreachable. |
| `NODE_ID` | string | hostname | Unique identifier for this Orchestrator instance. Included in all audit events. |
| `IO_API_BASE` | URL | _(empty — disabled)_ | Base URL of the IO Component HTTP server (e.g. `http://localhost:3001`). When set, the Orchestrator pushes real-time status, credential requests, plan previews, and confirmation prompts to the IO dashboard. When empty, status and credential pushes are no-ops; confirmation-gated subtasks and approval-gated plans fail immediately with `CONFIRMATION_UNAVAILABLE` / `PLAN_REJECTED`. |
| `PLAN_APPROVAL_MODE` | enum | `multi` | Controls when the Dispatcher gates plan execution on user approval. `off` — never require approval; `multi` (default) — require approval only for plans with more than one subtask; `always` — require approval for every plan. |
| `PLAN_APPROVAL_TIMEOUT_SECONDS` | integer | 300 | How long the Dispatcher waits for a user approve/reject decision before failing the task with `PLAN_APPROVAL_TIMEOUT`. |
| `LOG_LEVEL` | enum | `info` | Log verbosity: `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | enum | `json` | Log output format: `json` (production / Loki ingestion) or `text` (local dev). |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | host:port | `tempo:4317` | OTLP gRPC endpoint for OpenTelemetry trace export. If unreachable, the Orchestrator starts normally and retries in the background. |
| `LOKI_URL` | URL | `http://loki:3100` | Loki base URL used by the `/debug/trace/{trace_id}` endpoint to query log timelines. |

---

## 17. Proof of Concept (PoC)

### 17.1 PoC Objective

The PoC demonstrates the Orchestrator's four hardest behaviors in a minimal but realistic environment:

1. **Policy-first task dispatch:** a task that fails policy validation never reaches the Planner Agent or any other agent.
2. **Task decomposition via Planner Agent:** a multi-step natural-language task is sent to the Planner Agent, which returns a structured plan. The Orchestrator executes the plan's subtasks in dependency order with result piping.
3. **Self-healing recovery:** when an agent reports TERMINATED for a parent task, the Orchestrator retrieves the latest parent task state from Memory, verifies scope, and re-dispatches within the configured retry budget.
4. **Heartbeat-based failure detection:** the Orchestrator's health monitoring detects Vault or Memory unavailability and responds per configured policy.

### 17.2 PoC Scope

- A single Orchestrator instance (no clustering required for PoC).
- Mock Agents Component that accepts planner and subtask `task.inbound` messages, and returns simulated events.
- A mock Planner Agent (can be a scripted stub returning predefined plans, OR a real LLM-backed agent).
- A **real** OpenBao (dev mode) instance for policy validation.
- A **real** Memory Component instance (backed by its team-selected database) or a contract-compatible mock adapter for PoC.
- NATS in standalone mode.
- Task: *"Book a flight from NYC to LA for next Friday, returning Sunday, and find a hotel near the airport with a pool."* The Planner Agent should return a 3-subtask plan (search_flights → find_hotels → present_options). A task agent can be manually terminated to trigger the current parent-task recovery flow.

### 17.3 Historical Implementation Sketch (Go)

> **NOTE:** This section is retained as an early design sketch, not literal current source code. The current implementation is split across `internal/gateway`, `internal/dispatcher`, `internal/executor`, `internal/monitor`, `internal/recovery`, `internal/memory`, and `internal/io`. Important differences from this sketch: active plan/subtask execution is not reconstructed on restart; Task Monitor rehydrates parent tasks only; Recovery Manager retries parent tasks, while Plan Executor owns subtask state and dependency blocking.

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
        o.publishToAgents("aegis.agents.task.inbound", spec)

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
    o.publishToAgents("aegis.agents.task.inbound", spec)
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
| Submit task with skill domain not in user policy | `POLICY_VIOLATION` returned. No decomposition request sent, no agent touched. | Vault audit log shows DENY before any decomposition event. |
| Submit duplicate `task_id` within dedup window | `DUPLICATE_TASK` returned with current status. No second decomposition, no second agent. | `activeTasks` map has 1 entry. |
| Planner Agent timeout | If Planner doesn't respond within `DECOMPOSITION_TIMEOUT_SECONDS`, task marked `DECOMPOSITION_FAILED`. | User I/O receives error within 35s. |
| Agent reports TERMINATED for a task | Recovery reads latest parent `TaskState`, verifies scope, increments `retry_count`, and re-dispatches if under the retry budget. | Parent task recovery attempted and `recovery_event` written. |
| Agent reports TERMINATED after max retries | Parent task marked FAILED. Credentials revoked. | User I/O receives terminal failure. |
| Subtask failure mid-plan | If s1 fails, s2 and s3 are marked `BLOCKED`. User I/O notified with partial results. | No orphaned agents. Credentials revoked. |
| Task `timeout_seconds=30`, task takes 45s | Parent task timeout triggers Recovery Manager and terminal `TIMED_OUT` handling. | Timeout fires within ±2s. |
| Take Vault offline mid-operation | `VAULT_UNAVAILABLE` (FAIL_CLOSED). No new tasks dispatched. | In-flight tasks continue; new tasks rejected. |
| Orchestrator crash and restart | On restart: active parent tasks are rehydrated from Memory and timeout monitoring resumes. Persisted plan/subtask records remain queryable, but active plan execution is not reconstructed. | Parent task state is not lost; plan restart-resume remains future work. |

---

## 18. Open Questions

| ID | Question | Impact | Owner |
|---|---|---|---|
| OQ-01 | When the Orchestrator re-dispatches a recovering task, should it prefer the same agent (if recovered) or always request a new agent? | Affects recovery latency vs. agent warmth | Orchestrator + Agents Component teams |
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
| 3.4 | April 2026 | Junyu Ding | **Plan Approval Gate & Personalization (Milestone 3 — continued).** Added: `AWAITING_APPROVAL` task state — after plan validation the Dispatcher optionally gates execution on explicit user approve/reject before handing off to the Plan Executor; `plan_preview` HTTP event pushed to IO Component with full subtask summary and expiry; `aegis.orchestrator.plan.decision` NATS inbound topic for user decisions; `HandlePlanDecision` on Task Dispatcher; `PLAN_REJECTED` and `PLAN_APPROVAL_TIMEOUT` error codes; `PLAN_APPROVAL_MODE` (`off`/`multi`/`always`) and `PLAN_APPROVAL_TIMEOUT_SECONDS` config vars; Task Monitor `AWAITING_APPROVAL` reprieve (approval phase is governed by Dispatcher timer, not global task timeout). Also: personalization client fetches user facts from Memory `personal_info` and prepends to planner prompt (best-effort); early `task_accepted` published right after policy check; dedup re-entry from terminal states allowed for follow-up tasks; `CONFIRMATION_UNAVAILABLE` error replaces silent hang on IO-down confirmation delivery failure. §3.1, §4.1 M1/M2/M4, §5.3.2 (new FR-PA-01–06), §9.1, §14.1, §16 updated. |
| 3.3 | April 2026 | Junyu Ding | **Multi-step Prompting & Confirmation (Milestone 3).** Added: `requires_confirmation` flag on `Subtask` — when `true`, Plan Executor (M7) suspends dispatch and transitions subtask to new `AWAITING_CONFIRMATION` state; `PublishConfirmationRequest` on Communications Gateway (M1) forwards prompt to IO Component via HTTP; new NATS inbound topic `aegis.orchestrator.task.confirmation_response` for user approval/rejection; `HandleConfirmationResponse` on Plan Executor resumes (confirm) or fails+blocks (reject) the waiting subtask; `ErrCodeUserRejected` and `ErrCodeConfirmationUnavailable` error codes; IO Component contract documented in new §11.3.1 and §11.6; §3.1, §4.1 M1/M7, §5.3, §9.2, §10.3, §11.6, §11.9, §14.1 all updated to reflect confirmation flow and current implementation boundaries. |
| 3.2 | April 2026 | Junyu Ding | **Centralized Logging & Distributed Tracing (Milestone 2).** Added: `internal/observability` package (`logger.go`, `context.go`, `tracing.go`) — structured JSON logging via `log/slog` with context-propagated `trace_id`/`task_id`/`plan_id`/`subtask_id`/`module` fields; `trace_id` generated at Gateway entry point and stamped on every outbound `MessageEnvelope`; extracted from every inbound envelope to continue traces across component boundaries; OpenTelemetry span tree (`task_received` → `dedup_check` → `policy_validation` → `decomposition` → `plan_execution` → `subtask_dispatch` / `result_delivery` / `recovery_attempt`) exported via OTLP gRPC to Tempo; Grafana observability stack (Loki + Promtail + Tempo + Grafana) added to `docker-compose.yml` with auto-provisioned log→trace linking; `GET /debug/trace/{trace_id}` HTTP endpoint; §15 fully rewritten to reflect implementation; four new env vars added to §16 (`LOG_LEVEL`, `LOG_FORMAT`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `LOKI_URL`); `NATS_CREDS_PATH` corrected to optional. |
| 3.1 | April 2026 | Junyu Ding | **IO Component Integration.** Added: IO Component to system context (§3.1); M1 now subscribes to `aegis.orchestrator.credential.request` and routes `user_input` operations to IO (vault authorize/revoke filtered out); M2 now pushes real-time status updates to IO at DECOMPOSING, PLAN_ACTIVE, COMPLETED, and FAILED transitions; new `internal/io/client.go` package (HTTP client, disabled when `IO_API_BASE` unset); new data flow §6.5 (IO Component Status Push) with state→status mapping table; new interface spec §11.6 (Orchestrator → IO Component HTTP bridge) with full event envelope schemas and error-handling contract; `aegis.orchestrator.credential.request` added to NATS topic table (§11.9); `IO_API_BASE` added to configuration table (§16). |
| 3.0 | April 2026 | Junyu Ding | **Task Decomposition via Planner Agent.** Added: M7 Plan Executor module; LLM-Free Orchestrator Principle (§2.5); task decomposition coordination as 4th core concern; new functional requirements FR-TD-01 through FR-TD-07; new task states DECOMPOSING, PLAN_ACTIVE, DECOMPOSITION_FAILED, PARTIAL_COMPLETE; new subtask state machine (§9.2); Execution Plan Schema (§10.3); Subtask State Record Schema (§10.4); new NATS topics for decomposition; Flow 3.5 (Task Decomposition); Sequence Diagram 8.5 (Decomposition Failure); new env vars (DECOMPOSITION_TIMEOUT_SECONDS, MAX_SUBTASKS_PER_PLAN, PLAN_EXECUTOR_MAX_PARALLEL); updated PoC with decomposition scenarios; subtask-level metrics; Planner Agent scope enforcement. FR-TRK-04 updated to accept empty `required_skill_domains[]`. Resolved OQ-05. New OQ-06 through OQ-08. Design rationale based on Conductor/Superset/Netflix Conductor research: the Orchestrator remains LLM-free; intelligence lives in the Planner Agent. |
