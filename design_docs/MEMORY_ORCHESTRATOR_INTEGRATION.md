# Memory ↔ Orchestrator Integration Contract

| Field | Value |
|---|---|
| Document | MEMORY_ORCHESTRATOR_INTEGRATION |
| Version | 1.0 |
| Status | Draft — For Review |
| Date | April 2026 |
| Author | Junyu Ding |
| Consumers | Orchestrator Component, Memory Component |

---

## Overview

The Orchestrator is the **only component that writes task lifecycle state to the Memory Component**. It needs to persist task state, execution plans, subtask state, audit logs, recovery events, and policy events — and read them back on deduplication checks, recovery, and startup rehydration.

The Memory Component's existing API (`/api/v1/chat`, `/api/v1/agents`, `/api/v1/system/events`, etc.) was not designed for this use case. This document defines the **new endpoints the Memory team must add** under `/api/v1/orchestrator/` so the Orchestrator can replace its in-memory mock with a real persistent client.

The Orchestrator's Go interface that these endpoints must satisfy is `internal/interfaces/MemoryClient`:

```go
type MemoryClient interface {
    Write(payload OrchestratorMemoryWritePayload) error
    Read(query MemoryQuery) ([]MemoryRecord, error)
    ReadLatest(taskID string, dataType string) (*MemoryRecord, error)
    Ping() error
}
```

`Ping()` is already covered by the existing `GET /api/v1/healthz`. The three remaining methods require three new endpoints.

---

## Data Types

All writes are tagged with a `data_type`. The Memory API **must reject writes with unknown data types** with `400 Bad Request`.

| `data_type` | Description | Append-only? |
|---|---|---|
| `task_state` | Top-level task lifecycle record. Updated on every state transition. | No — upsert |
| `plan_state` | Execution plan returned by the Planner Agent. Written once per task decomposition. | No — upsert |
| `subtask_state` | Per-subtask tracking record. Updated on every subtask state transition. | No — upsert |
| `audit_log` | Immutable audit trail. One record per state transition and policy decision. | **Yes — append-only** |
| `recovery_event` | Written on every recovery re-dispatch attempt. | **Yes — append-only** |
| `policy_event` | Written on every policy check (allow or deny). | **Yes — append-only** |

> **CRITICAL — Append-only enforcement**: `audit_log`, `recovery_event`, and `policy_event` records **MUST NEVER be updated or deleted**. This must be enforced at the database layer via a trigger or row-level policy, not just at the application layer. If a `PUT` or `DELETE` is attempted on these record types, the DB must reject it. This is a security requirement (§13.4).

---

## Schema — Record Table

The Memory team should implement a single `orchestrator_records` table (or equivalent) with the following logical schema:

```sql
CREATE TABLE orchestrator_records (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    orchestrator_task_ref TEXT        NOT NULL,
    task_id               TEXT        NOT NULL,
    plan_id               TEXT,                         -- set for plan_state and subtask_state records
    subtask_id            TEXT,                         -- set for subtask_state records
    data_type             TEXT        NOT NULL,         -- one of the 6 data_type constants above
    timestamp             TIMESTAMPTZ NOT NULL,
    payload               JSONB       NOT NULL,         -- the full serialized object (see Payload Schemas below)
    ttl_seconds           INT         DEFAULT 0,        -- 0 = no expiry; for future TTL cleanup jobs
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for the primary query patterns:
CREATE INDEX idx_orch_records_task_id_type     ON orchestrator_records (task_id, data_type, timestamp DESC);
CREATE INDEX idx_orch_records_orch_ref_type    ON orchestrator_records (orchestrator_task_ref, data_type, timestamp DESC);
CREATE INDEX idx_orch_records_type_timestamp   ON orchestrator_records (data_type, timestamp DESC);

-- Append-only trigger for audit_log, recovery_event, policy_event:
CREATE OR REPLACE RULE no_update_append_only AS
    ON UPDATE TO orchestrator_records
    WHERE OLD.data_type IN ('audit_log', 'recovery_event', 'policy_event')
    DO INSTEAD NOTHING;

CREATE OR REPLACE RULE no_delete_append_only AS
    ON DELETE TO orchestrator_records
    WHERE OLD.data_type IN ('audit_log', 'recovery_event', 'policy_event')
    DO INSTEAD NOTHING;
```

---

## Endpoints

### 1. Write a Record

```
POST /api/v1/orchestrator/records
```

Persists one record. For `task_state`, `plan_state`, and `subtask_state`, the behavior is **upsert** — if a record already exists for the same `(task_id, data_type)` (or `(task_id, subtask_id, data_type)` for subtask records), replace it. For `audit_log`, `recovery_event`, and `policy_event`, **always insert a new row** (never replace).

#### Request Body

```json
{
  "orchestrator_task_ref": "a1b2c3d4-...",
  "task_id": "user-provided-uuid",
  "plan_id": "plan-uuid",
  "subtask_id": "subtask-uuid",
  "data_type": "task_state",
  "timestamp": "2026-04-16T10:00:00.000Z",
  "payload": { ... },
  "ttl_seconds": 0
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `orchestrator_task_ref` | string (UUID) | Yes | Orchestrator-internal correlation key |
| `task_id` | string (UUID) | Yes | User-supplied deduplication key |
| `plan_id` | string (UUID) | No | Required when `data_type` is `plan_state` or `subtask_state` |
| `subtask_id` | string | No | Required when `data_type` is `subtask_state` |
| `data_type` | string | Yes | Must be one of the 6 valid values |
| `timestamp` | string (RFC3339) | Yes | Event timestamp set by Orchestrator |
| `payload` | object | Yes | The full serialized state object (see Payload Schemas below) |
| `ttl_seconds` | integer | No | `0` = no expiry. Reserved for future cleanup. Default `0`. |

#### Responses

| Status | Meaning |
|---|---|
| `201 Created` | Record written successfully |
| `400 Bad Request` | Missing required field or invalid `data_type` |
| `500 Internal Server Error` | Database write failure |

#### Response Body (201)

```json
{
  "id": "generated-record-uuid"
}
```

#### Upsert vs Insert logic

| `data_type` | Behavior | Upsert key |
|---|---|---|
| `task_state` | Upsert | `(task_id, data_type)` |
| `plan_state` | Upsert | `(task_id, plan_id, data_type)` |
| `subtask_state` | Upsert | `(task_id, subtask_id, data_type)` |
| `audit_log` | Always insert | — |
| `recovery_event` | Always insert | — |
| `policy_event` | Always insert | — |

---

### 2. Query Records

```
GET /api/v1/orchestrator/records
```

Returns all records matching the query, ordered by `timestamp` ascending.

#### Query Parameters

| Parameter | Type | Required | Notes |
|---|---|---|---|
| `data_type` | string | Yes | Must be one of the 6 valid values |
| `task_id` | string | Conditional | Required if `orchestrator_task_ref` is not provided |
| `orchestrator_task_ref` | string | Conditional | Required if `task_id` is not provided |
| `from_timestamp` | string (RFC3339) | No | Inclusive lower bound on `timestamp` |
| `to_timestamp` | string (RFC3339) | No | Inclusive upper bound on `timestamp` |
| `state_filter` | string | No | When set to `not_terminal`, only returns `task_state` records whose `payload.state` is not in the terminal set (see below) |

**Terminal task states** (used by `state_filter=not_terminal`):
```
COMPLETED, FAILED, DELIVERY_FAILED, TIMED_OUT, POLICY_VIOLATION, DECOMPOSITION_FAILED, PARTIAL_COMPLETE
```

> The `state_filter=not_terminal` query is used at startup by the Orchestrator to rehydrate all active in-flight tasks. It must filter server-side against `payload->>'state'` in the DB.

#### Example Requests

```
GET /api/v1/orchestrator/records?data_type=task_state&task_id=abc-123
GET /api/v1/orchestrator/records?data_type=audit_log&orchestrator_task_ref=xyz-456
GET /api/v1/orchestrator/records?data_type=task_state&state_filter=not_terminal
GET /api/v1/orchestrator/records?data_type=task_state&task_id=abc-123&from_timestamp=2026-04-15T00:00:00Z
```

#### Response Body (200)

```json
[
  {
    "orchestrator_task_ref": "a1b2c3d4-...",
    "task_id": "user-provided-uuid",
    "data_type": "task_state",
    "timestamp": "2026-04-16T10:00:00.000Z",
    "payload": { ... }
  }
]
```

Returns an empty array `[]` when no records match (not a 404).

#### Responses

| Status | Meaning |
|---|---|
| `200 OK` | Query executed (may return empty array) |
| `400 Bad Request` | Missing `data_type`, or both `task_id` and `orchestrator_task_ref` are absent without `state_filter` |
| `500 Internal Server Error` | Database read failure |

---

### 3. Read Latest Record

```
GET /api/v1/orchestrator/records/latest?task_id={task_id}&data_type={data_type}
```

Returns the **single most recent record** for a given `task_id` and `data_type`, ordered by `timestamp DESC LIMIT 1`.

Used by the Recovery Manager to restore the last valid task state before re-dispatching a failed agent.

#### Query Parameters

| Parameter | Type | Required |
|---|---|---|
| `task_id` | string | Yes |
| `data_type` | string | Yes |

#### Example Request

```
GET /api/v1/orchestrator/records/latest?task_id=abc-123&data_type=task_state
```

#### Response Body (200)

```json
{
  "orchestrator_task_ref": "a1b2c3d4-...",
  "task_id": "abc-123",
  "data_type": "task_state",
  "timestamp": "2026-04-16T10:05:00.000Z",
  "payload": { ... }
}
```

#### Responses

| Status | Meaning |
|---|---|
| `200 OK` | Record found |
| `404 Not Found` | No records exist for this `task_id` + `data_type` combination |
| `400 Bad Request` | Missing `task_id` or `data_type` |
| `500 Internal Server Error` | Database read failure |

---

### 4. Health Check (existing)

```
GET /api/v1/healthz
```

Already implemented. The Orchestrator calls this every 10 seconds (configurable via `HEALTH_CHECK_INTERVAL_SECONDS`) to verify Memory Component reachability.

Expected response: `200 OK` when healthy.

---

## Payload Schemas

The `payload` field in every record is a serialized JSON object. The Orchestrator writes these types directly — the Memory API treats `payload` as opaque JSONB and never parses it except for the `state_filter` query on `task_state` records.

### `task_state` payload

```json
{
  "orchestrator_task_ref": "a1b2c3d4-e5f6-...",
  "task_id": "user-task-uuid",
  "user_id": "user-123",
  "state": "PLAN_ACTIVE",
  "required_skill_domains": ["web", "calendar"],
  "policy_scope": {
    "domains": ["web", "calendar"],
    "token_ref": "vault-token-accessor",
    "issued_at": "2026-04-16T10:00:00Z",
    "expires_at": "2026-04-16T10:35:00Z",
    "metadata": {}
  },
  "plan_id": "plan-uuid",
  "agent_id": "",
  "retry_count": 0,
  "dispatched_at": "2026-04-16T10:00:01Z",
  "timeout_at": "2026-04-16T10:05:00Z",
  "completed_at": null,
  "error_code": "",
  "state_history": [
    { "state": "RECEIVED", "timestamp": "2026-04-16T10:00:00Z", "node_id": "node-1" },
    { "state": "POLICY_CHECK", "timestamp": "2026-04-16T10:00:00.1Z", "node_id": "node-1" },
    { "state": "PLAN_ACTIVE", "timestamp": "2026-04-16T10:00:01Z", "node_id": "node-1" }
  ],
  "callback_topic": "aegis.user.results.user-123",
  "idempotency_window_seconds": 300,
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736"
}
```

**Valid `state` values:**
`RECEIVED`, `POLICY_CHECK`, `DISPATCH_PENDING`, `DISPATCHED`, `DECOMPOSING`, `PLAN_ACTIVE`, `RUNNING`, `RECOVERING`, `COMPLETED`, `FAILED`, `DELIVERY_FAILED`, `TIMED_OUT`, `POLICY_VIOLATION`, `DECOMPOSITION_FAILED`, `PARTIAL_COMPLETE`

**Terminal states** (used by `state_filter=not_terminal`):
`COMPLETED`, `FAILED`, `DELIVERY_FAILED`, `TIMED_OUT`, `POLICY_VIOLATION`, `DECOMPOSITION_FAILED`, `PARTIAL_COMPLETE`

---

### `plan_state` payload

```json
{
  "plan_id": "plan-uuid",
  "parent_task_id": "user-task-uuid",
  "subtasks": [
    {
      "subtask_id": "s1",
      "required_skill_domains": ["web"],
      "action": "search_flights",
      "instructions": "Search for flights from NYC to LA",
      "params": {},
      "depends_on": [],
      "timeout_seconds": 60
    },
    {
      "subtask_id": "s2",
      "required_skill_domains": ["web"],
      "action": "find_hotels",
      "instructions": "Find hotels near LAX",
      "params": {},
      "depends_on": ["s1"],
      "timeout_seconds": 60
    }
  ],
  "created_at": "2026-04-16T10:00:02Z"
}
```

---

### `subtask_state` payload

```json
{
  "subtask_id": "s1",
  "plan_id": "plan-uuid",
  "task_id": "user-task-uuid",
  "orchestrator_task_ref": "orch-ref-uuid",
  "state": "COMPLETED",
  "agent_id": "agent-abc",
  "required_skill_domains": ["web"],
  "depends_on": [],
  "prior_results": [],
  "retry_count": 0,
  "dispatched_at": "2026-04-16T10:00:03Z",
  "timeout_at": "2026-04-16T10:01:03Z",
  "completed_at": "2026-04-16T10:00:45Z",
  "result": { "flights": ["UA123", "AA456"] },
  "error_code": ""
}
```

**Valid subtask `state` values:**
`PENDING`, `DISPATCH_PENDING`, `DISPATCHED`, `RUNNING`, `RECOVERING`, `COMPLETED`, `FAILED`, `BLOCKED`, `TIMED_OUT`, `DELIVERY_FAILED`

---

### `audit_log` payload

```json
{
  "log_id": "audit-uuid",
  "orchestrator_task_ref": "orch-ref-uuid",
  "event_type": "policy_allow",
  "initiating_module": "PolicyEnforcer",
  "outcome": "success",
  "event_detail": {
    "domains": ["web", "calendar"],
    "vault_token_ref": "accessor-abc"
  },
  "timestamp": "2026-04-16T10:00:00.1Z",
  "node_id": "node-1",
  "task_id": "user-task-uuid",
  "user_id": "user-123"
}
```

**Valid `event_type` values:**
`task_received`, `policy_allow`, `policy_deny`, `task_dispatched`, `task_completed`, `task_failed`, `recovery_attempt`, `credential_revoked`, `vault_unavailable`, `component_failure`, `revocation_failed`

**Valid `outcome` values:** `success`, `denied`, `failed`, `partial`, `recovered`

**Valid `initiating_module` values:** `CommunicationsGateway`, `TaskDispatcher`, `PolicyEnforcer`, `TaskMonitor`, `RecoveryManager`, `MemoryInterface`

> **Security**: `event_detail` must NEVER contain raw user input, credential values, task payloads, or policy tokens.

---

### `recovery_event` payload

```json
{
  "orchestrator_task_ref": "orch-ref-uuid",
  "task_id": "user-task-uuid",
  "attempt_number": 2,
  "reason": "AGENT_TERMINATED",
  "timestamp": "2026-04-16T10:02:00Z",
  "node_id": "node-1"
}
```

**Valid `reason` values:** `AGENT_RECOVERING`, `AGENT_TERMINATED`, `TIMEOUT`

---

### `policy_event` payload

Policy events follow the same structure as `audit_log` with `event_type` values `policy_allow` or `policy_deny`.

---

## How the Orchestrator Uses These Endpoints

This section shows which operation triggers which API call, so the Memory team understands the read/write patterns and load expectations.

### Write patterns

| When | `data_type` written | Frequency |
|---|---|---|
| Task received from User I/O | `task_state` (state=RECEIVED) | Once per task |
| Policy check completes | `task_state` + `audit_log` + `policy_event` | Once per task |
| Task sent to Planner Agent | `task_state` (state=DECOMPOSING) | Once per task (decomposed tasks) |
| Execution plan received | `task_state` (state=PLAN_ACTIVE) + `plan_state` | Once per task (decomposed tasks) |
| Each subtask dispatched | `subtask_state` (state=DISPATCHED) | N times per plan (N = subtask count) |
| Each subtask result received | `subtask_state` (state=COMPLETED/FAILED) | N times per plan |
| Task reaches terminal state | `task_state` (terminal state) + `audit_log` | Once per task |
| Recovery triggered | `task_state` (state=RECOVERING) + `recovery_event` | Up to `MAX_TASK_RETRIES` times (default 3) |
| Credential revoked | `audit_log` (event=credential_revoked) | On task completion/failure |

### Read patterns

| When | Operation | Endpoint used |
|---|---|---|
| New task arrives — deduplication check | Read by `task_id` + `data_type=task_state` | `GET /records?data_type=task_state&task_id=...` |
| Orchestrator startup — rehydrate active tasks | Read all non-terminal task states | `GET /records?data_type=task_state&state_filter=not_terminal` |
| Recovery triggered — restore last known state | Read latest `task_state` for the task | `GET /records/latest?task_id=...&data_type=task_state` |
| Recovery triggered — restore subtask states | Read latest `subtask_state` for each subtask | `GET /records/latest?task_id=...&data_type=subtask_state` |

---

## Authentication

The Orchestrator will include any required auth headers set in its environment. The Memory team should document the expected auth header (e.g., `Authorization: Bearer <token>` or `X-API-KEY`) so the Orchestrator's HTTP client can be configured to send it via `MEMORY_API_KEY` env var.

If the existing Memory Component auth pattern (e.g., `X-API-KEY`) applies to the new `/api/v1/orchestrator/` routes, document that here.

---

## Environment Variables (Orchestrator side)

Once the Memory API is ready, the Orchestrator needs `MEMORY_ENDPOINT` set to the full base URL:

```
MEMORY_ENDPOINT=http://memory-api:8080
```

The Orchestrator constructs all endpoint paths relative to this base:
- `POST {MEMORY_ENDPOINT}/api/v1/orchestrator/records`
- `GET  {MEMORY_ENDPOINT}/api/v1/orchestrator/records`
- `GET  {MEMORY_ENDPOINT}/api/v1/orchestrator/records/latest`
- `GET  {MEMORY_ENDPOINT}/api/v1/healthz`

When `MEMORY_ENDPOINT` is `mock://memory` or unset, the Orchestrator continues using its in-process mock (no behavior change for local dev without Memory running).

---

## Implementation Checklist (Memory team)

- [ ] Create `orchestrator_records` table (or equivalent) with schema above
- [ ] Add append-only DB trigger/rule for `audit_log`, `recovery_event`, `policy_event`
- [ ] Add indexes for `(task_id, data_type, timestamp DESC)` and `(orchestrator_task_ref, data_type, timestamp DESC)`
- [ ] `POST /api/v1/orchestrator/records` — write with upsert/insert logic per data_type
- [ ] `GET /api/v1/orchestrator/records` — query with all filter params including `state_filter=not_terminal`
- [ ] `GET /api/v1/orchestrator/records/latest` — single latest record lookup
- [ ] Validate `data_type` on write — reject unknown values with 400
- [ ] Confirm existing `GET /api/v1/healthz` returns 200 when DB is healthy (already done)
- [ ] Document auth header requirement for the new routes

---

## Open Questions

| # | Question | Owner |
|---|---|---|
| OQ-1 | What auth header should the Orchestrator's HTTP client send to `/api/v1/orchestrator/*`? Is it the same `X-API-KEY` pattern as the Vault endpoints? | Memory team |
| OQ-2 | Should the Memory team own the `orchestrator_records` table migration, or does the Orchestrator call a `POST /api/v1/orchestrator/migrate` endpoint at startup? Currently `MigrateSchema()` in the Orchestrator is a no-op TODO. | Both teams |
| OQ-3 | What is the Memory Component's retention policy for orchestrator records? Should `ttl_seconds` trigger automatic expiry (e.g., via a background cleanup job), or is it advisory only? | Memory team |
| OQ-4 | Is there a request size limit on the `payload` field? The Orchestrator's `task_state` payload includes `StateHistory` which grows with each transition. Max payload size estimate: ~50KB for a task with 20 subtasks and 10 state transitions each. | Memory team |
