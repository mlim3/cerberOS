# Integration Memo: Memory Service

This document serves as the technical integration guide for the Orchestrator, IO, and Vault teams to interact with the Memory Service.

## Base URL & Swagger Link

The Memory Service API is available at the following base URL (assuming local development):
**`http://localhost:8081/api/v1`**

Live API documentation is available via Swagger UI at:
**`http://localhost:8081/swagger/index.html`**

## Header Requirements

When interacting with the Memory Service, specific headers are required depending on the endpoint and the context of the request.

| Header Name          | Required For                                                              | Description                                                                                                                                                                          |
| :------------------- | :------------------------------------------------------------------------ | :----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `X-Internal-API-Key` | Vault endpoints (`/vault/*`) and Orchestrator endpoints (`/orchestrator/*`) | Guards access to internal-only surfaces. Must match the `INTERNAL_VAULT_API_KEY` defined in the Memory Service's environment configuration.                                         |
| `traceparent`        | All endpoints (optional but recommended)                                  | Preferred distributed tracing header. Memory reuses the W3C trace ID when present so downstream spans stay connected across IO, Orchestrator, Agents, and Memory.                  |
| `X-Trace-ID`         | Legacy callers only (optional)                                            | Some callers may still send this for compatibility, but the service's current trace resolution is based on `traceparent` with local fallback generation.                            |

## Conversation And Task Model

The Memory Service now treats conversations and tasks as distinct durable concepts:

- **Conversations** are the user-facing chat containers. Create one first (`POST /api/v1/conversations`), then attach tasks to it.
- **Tasks** represent individual execution runs (`POST /api/v1/tasks`). Each task belongs to exactly one conversation via `conversationId`.
- **Messages** are immutable and append-only under a conversation (`POST /api/v1/chat/{conversationId}/messages`).

This means `conversationId` and `taskId` are no longer interchangeable. The IO layer is responsible for maintaining both identifiers and passing them correctly to Memory.

Related routes:

- `GET /api/v1/conversations`
- `POST /api/v1/conversations`
- `POST /api/v1/tasks`
- `GET /api/v1/tasks/{taskId}`
- `POST /api/v1/chat/{conversationId}/messages`
- `GET /api/v1/chat/{conversationId}/messages?userId=...`

## Orchestrator Records

The Memory Service exposes an internal-only persistence surface for orchestrator state under `/api/v1/orchestrator/records`. All routes require `X-Internal-API-Key`.

Write semantics by `data_type`:

- `task_state`, `plan_state`, `subtask_state` — upsert/replace
- `audit_log`, `recovery_event`, `policy_event` — append-only, enforced at the DB layer

Key query features:

- `GET /api/v1/orchestrator/records` — filter by `data_type`, `task_id`, `orchestrator_task_ref`, time range, or `state_filter=not_terminal` for startup rehydration
- `GET /api/v1/orchestrator/records/latest` — fetch the latest record for a given task and data type

Payloads are capped at 256 KB.

## The Semantic Search Contract

The Memory Service provides a semantic search endpoint for retrieving personal information chunks relevant to a query.

**Endpoint:** `POST /api/v1/personal_info/{userId}/query`

**Response Interpretation:**
The response will include a list of relevant information chunks, each with a `similarityScore`.

- The `similarityScore` represents the cosine similarity between the query and the stored chunk.
- **Interpretation:** A score closer to **1.0** indicates a higher relevance/similarity. A score of 1.0 means an exact match, while lower scores indicate less similarity.

Related personal-info routes:

- `POST /api/v1/personal_info/{userId}/save`
- `POST /api/v1/personal_info/{userId}/query`
- `GET /api/v1/personal_info/{userId}/all`
- `PUT /api/v1/personal_info/{userId}/facts/{factId}`
- `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

## Fact Lifecycle

Facts support a full lifecycle beyond basic CRUD:

- `POST /api/v1/personal_info/{userId}/facts/{factId}/archive` — move a fact to the archive with a reason (`decayed | contradicted | superseded | manually_archived`)
- `POST /api/v1/personal_info/{userId}/facts/{factId}/supersede` — create a replacement fact and automatically archive the old one linked via `supersededByFactId`
- `GET /api/v1/personal_info/{userId}/all?includeArchived=true` — include archived facts in retrieval

Archived facts are excluded from all default retrieval paths.

## Scheduled Jobs

The Memory Service now exposes a scheduler surface for internal maintenance jobs and external dispatch-style work:

- `POST /api/v1/scheduled_jobs`
- `POST /api/v1/scheduled_jobs/run_due`
- `GET /api/v1/scheduled_jobs/{jobId}/runs`

Current note:

- due-job execution and run history are implemented
- external dispatch is still recorded as a dispatch-style result rather than a full orchestrator/BUS handoff

## Agent Execution Logs

Memory also stores per-task agent execution traces:

- `POST /api/v1/agent/{taskId}/executions`
- `GET /api/v1/agent/{taskId}/executions`

Legacy compatibility aliases also exist under:

- `/api/v1/agents/tasks/{taskId}/executions`

## Vault Policy

The Memory Service implements strict security measures for handling sensitive data.

**Crucial Policy:**

- The Memory Service is the **sole holder** of the `VAULT_MASTER_KEY`.
- All secrets are stored in the database securely encrypted using AES-256-GCM.
- The Orchestrator and other services **will only receive plaintext secrets after a successful and authorized decryption request** via the internal Vault endpoints. The Vault itself never exposes the raw encrypted bytes or the master key to external services.

For the complete current Memory specification, see [memory/README.md](../memory/README.md).
