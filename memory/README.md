# Memory Service API And Current Specification

The memory service is the persistence layer for CerberOS chat state, personal memory, internal vault secrets, orchestrator records, scheduled jobs, system events, and agent execution logs.

This document is intentionally comprehensive. It preserves the specification-style detail that used to live here, but it has been rewritten to match the service that exists today instead of the older changelog-era contract.

Open follow-up work that is still not implemented lives in [to_do.md](./to_do.md).

## 1. Service Overview

### Purpose

The memory service provides:

- user-owned conversations, tasks, and immutable chat messages
- personal-info chunk storage and semantic retrieval
- fact CRUD plus archive and supersession lifecycle
- encrypted per-user secret storage for internal services
- agent task execution logs
- orchestrator record persistence for internal workflows
- scheduled-job storage and run history
- system event logging

### Architecture

- implementation language: Go
- primary store: PostgreSQL
- vector search: `pgvector`
- docs: Swagger artifacts under `memory/docs/`
- metrics: Prometheus endpoint at `/internal/metrics`

### Internal-only surfaces

The following route families require `X-Internal-API-Key`:

- `/api/v1/vault/...`
- `/api/v1/orchestrator/...`

### Current embedding behavior

- if `OPENAI_API_KEY` is set, the service uses OpenAI embeddings with `text-embedding-3-small`
- otherwise it uses a deterministic local embedder intended for local development, demos, and tests

### Current extraction behavior

- `personal_info/save` can create facts when `extractFacts=true`
- that extraction path is still placeholder behavior today, not a production fact extractor

## 2. API Conventions

### Base path

All routes are served under `/api/v1/` except for:

- `/internal/metrics`
- `/swagger/index.html`

### Content type

- requests and responses use JSON

### Time format

- timestamps are RFC3339

### IDs

- service-generated IDs are typically UUIDv7
- callers may also supply IDs on some create flows where the route supports idempotent or caller-owned identifiers

### Response envelope

Successful response:

```json
{
  "ok": true,
  "data": {},
  "error": null
}
```

Error response:

```json
{
  "ok": false,
  "data": null,
  "error": {
    "code": "invalid_argument | not_found | conflict | internal",
    "message": "Human readable error",
    "details": null
  }
}
```

## 3. Data Model Summary

### Chat domain

- `conversations` are explicit user-owned chat containers
- `tasks` belong to conversations and optionally carry orchestrator metadata
- `messages` are append-only chat entries under a conversation

### Personal info domain

- `chunks` store raw text plus embeddings for retrieval
- `facts` store structured user facts
- `source references` link chunks and facts back to their source material
- archived facts move to `user_facts_archive` and are excluded from default retrieval

### Vault domain

- secrets are stored encrypted at rest
- decrypted values are only returned from the internal API

### Orchestrator domain

- records are stored by typed contract
- task-state records behave like upserts
- audit-log-style records append

### Scheduler domain

- scheduled jobs represent internal or external work that should run later
- scheduled job runs record execution history

## 4. Endpoint Specification

### A. Health

#### `GET /api/v1/healthz`

Purpose:

- report service and database health

Success data:

- `status`: `healthy` or `degraded`
- `database`: `connected` or `disconnected`
- `timestamp`

Notes:

- returns `200` when healthy
- returns `503` when the database ping fails

### B. Conversations, Tasks, And Chat Messages

#### `GET /api/v1/conversations?userId=<uuid>&limit=<n>`

Purpose:

- list conversation summaries for one user

Validation:

- `userId` query parameter is required
- unknown users return `404 not_found`

Response data:

- `conversations`: array of conversation summaries

Conversation fields:

- `conversationId`
- `userId`
- `title`
- `createdAt`
- `updatedAt`
- `lastMessagePreview`
- `messageCount`
- `latestTaskId`
- `latestTaskStatus`

#### `POST /api/v1/conversations`

Purpose:

- create a conversation for a user

Request body:

```json
{
  "userId": "uuid",
  "conversationId": "uuid optional",
  "title": "optional title"
}
```

Behavior:

- validates the user exists
- creates a new conversation when `conversationId` does not exist
- returns the existing conversation when the supplied `conversationId` already belongs to the same user
- returns `404 not_found` when the supplied `conversationId` is already owned by another user

Response data:

- `conversation`

#### `POST /api/v1/tasks`

Purpose:

- create a task linked to a conversation

Request body:

```json
{
  "userId": "uuid",
  "taskId": "uuid optional",
  "conversationId": "uuid optional",
  "title": "optional title for auto-created conversation",
  "orchestratorTaskRef": "optional string",
  "traceId": "optional string",
  "status": "optional string",
  "inputSummary": "optional string"
}
```

Behavior:

- validates the user exists
- creates the conversation first when `conversationId` is omitted
- enforces conversation ownership when `conversationId` is supplied

Response data:

- `task`

Task fields:

- `taskId`
- `conversationId`
- `userId`
- `orchestratorTaskRef`
- `traceId`
- `status`
- `inputSummary`
- `createdAt`
- `updatedAt`
- `completedAt`

#### `GET /api/v1/tasks/{taskId}?userId=<uuid>`

Purpose:

- fetch one task owned by a user

Validation:

- `taskId` path parameter must be a UUID
- `userId` query parameter is required

Response data:

- `task`

#### `POST /api/v1/chat/{conversationId}/messages`

Purpose:

- append a message to a conversation

Request body:

```json
{
  "userId": "uuid",
  "role": "user | assistant | system",
  "content": "message text",
  "tokenCount": 123,
  "idempotencyKey": "uuid optional"
}
```

Behavior:

- validates the user exists
- enforces conversation ownership
- creates the conversation on first write when the conversation ID is new and belongs to this user
- messages are immutable after creation
- idempotency is scoped per conversation
- replaying the same `idempotencyKey` with the same payload returns the existing message
- replaying the same `idempotencyKey` with a different payload returns `409 conflict`

Response data:

- `message`

Message fields:

- `messageId`
- `conversationId`
- `userId`
- `role`
- `content`
- `tokenCount`
- `createdAt`

#### `GET /api/v1/chat/{conversationId}/messages?userId=<uuid>&limit=<n>`

Purpose:

- list messages in one conversation

Validation:

- `userId` query parameter is required
- unknown users return `404`
- users cannot read another user's conversation

Response data:

- `messages`: array of chat messages

#### `GET /api/v1/chat/{conversationId}/history?userId=<uuid>&max_turns=<n>&token_budget=<n>&include_roles=<csv>`

Purpose:

- return recent conversation turns in chronological order, trimmed to a token budget, so the Orchestrator / Agents can fold session context into an LLM prompt without blowing the model's context window
- same storage as `/messages` (reads `chat_schema.messages`); no new data type

Query parameters:

- `userId` — required; ownership check (same rule as `/messages`)
- `max_turns` — optional, default `40`, hard cap `500`
- `token_budget` — optional, default `4000`; drops oldest turns until `sum(tokenCount) <= budget`. `0` disables the token trim.
- `include_roles` — optional, default `user,assistant`. Accepts a CSV of `user`, `assistant`, `system`.

Token-count fallback:

- when a persisted message has no `token_count`, the budget uses a conservative `ceil(len(content) / 4)` estimate

Response data:

- `conversationId`
- `turns`: array of `{ messageId, role, content, tokenCount?, createdAt }` in chronological (oldest → newest) order
- `totalTokens`: sum of (estimated) token counts actually returned
- `truncated`: `true` when any older turns were dropped by `max_turns` or `token_budget`
- `tokenBudget`, `maxTurns`: the effective values that were applied

Observability:

- every served request emits a structured log entry `session_history.served` with `conversation_id`, `user_id`, `turn_count`, `total_tokens`, `token_budget`, `max_turns`, `truncated` — satisfies the "token budget bounded and logged" acceptance criterion from the multi-team session-context issue.

### C. Personal Info And Fact Lifecycle

#### `POST /api/v1/personal_info/{userId}/save`

Purpose:

- save raw user material into chunked memory
- optionally create extracted facts and source references

Request body:

```json
{
  "content": "raw text",
  "sourceType": "chat | uploaded_file | document | web",
  "sourceId": "uuid",
  "extractFacts": true
}
```

Behavior:

- validates the user exists
- chunks the content
- generates embeddings for chunks
- stores source references for created chunks
- optionally creates facts plus fact source references

Response data:

- `chunkIds`
- `factIds`
- `sourceReferenceIds`

#### `POST /api/v1/personal_info/{userId}/query`

Purpose:

- run semantic retrieval over stored chunks for one user

Request body:

```json
{
  "query": "string",
  "topK": 5
}
```

Behavior:

- validates the user exists
- embeds the query
- retrieves chunks by vector distance
- breaks ties with `created_at DESC`
- returns similarity scores derived from vector distance

Response data:

- `results`

Result fields:

- `chunkId`
- `text`
- `similarityScore`
- `sourceReferences`

Source-reference fields:

- `sourceReferenceId`
- `targetId`
- `targetType`
- `sourceId`
- `sourceType`

#### `GET /api/v1/personal_info/{userId}/all`

Purpose:

- list the current active facts and all stored chunks for a user

Optional query parameters:

- `includeArchived=true|false`

Behavior:

- archived facts are excluded by default
- archived facts are appended to the returned `facts` array when `includeArchived=true`

Response data:

- `facts`
- `chunks`

Fact fields:

- `factId`
- `userId`
- `category`
- `factKey`
- `factValue`
- `confidence`
- `version`
- `updatedAt`
- `archiveReason` when archived
- `supersededByFactId` when archived due to supersession

Chunk fields:

- `chunkId`
- `userId`
- `rawText`
- `modelVersion`
- `createdAt`

#### `PUT /api/v1/personal_info/{userId}/facts/{factId}`

Purpose:

- update a fact with optimistic concurrency control

Request body:

```json
{
  "category": "string",
  "factKey": "string",
  "factValue": "json value",
  "confidence": 0.95,
  "version": 1
}
```

Behavior:

- validates the user exists
- updates only when the supplied version matches the current version
- returns `409 conflict` when the fact exists but the version is stale
- returns `404 not_found` when the fact does not exist for that user

Response data:

- `fact`

#### `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

Purpose:

- delete an active fact

Response data:

- `deleted`
- `factId`

#### `POST /api/v1/personal_info/{userId}/facts/{factId}/archive`

Purpose:

- move an active fact into the archive table

Request body:

```json
{
  "reason": "decayed | contradicted | superseded | manually_archived"
}
```

Behavior:

- returns `404 not_found` when the active fact does not exist
- archived facts no longer appear in default retrieval

Response data:

- `factId`
- `archiveReason`

#### `POST /api/v1/personal_info/{userId}/facts/{factId}/supersede`

Purpose:

- create a replacement fact and archive the old fact with reason `superseded`

Request body:

```json
{
  "category": "optional string",
  "factKey": "optional string",
  "factValue": "required json value",
  "confidence": 0.95
}
```

Behavior:

- creates a new fact
- archives the old fact
- links the archived fact to the new fact through `supersededByFactId`

Response data:

- `oldFactId`
- `newFactId`
- `archiveReason`

### D. System Events

#### `POST /api/v1/system/events`

Purpose:

- create a system event log entry

Request body:

```json
{
  "traceId": "uuid optional",
  "serviceName": "optional string",
  "severity": "optional string",
  "message": "required string",
  "metadata": {}
}
```

Response data:

- `eventId`
- `createdAt`

#### `GET /api/v1/system/events?limit=<n>&serviceName=<name>&severity=<level>`

Purpose:

- list system events

Response data:

- `events`

Event fields:

- `eventId`
- `traceId`
- `serviceName`
- `severity`
- `message`
- `metadata`
- `createdAt`

### E. Vault Secrets

All vault routes require:

- header `X-Internal-API-Key`

#### `POST /api/v1/vault/{userId}/secrets`

Purpose:

- create or save an encrypted secret

Request body:

```json
{
  "key_name": "OPENAI_API_KEY",
  "value": "secret-value"
}
```

Response data:

- `key_name`
- `created`

#### `GET /api/v1/vault/{userId}/secrets?key_name=<name>`

Purpose:

- retrieve and decrypt one secret

Response data:

- `key_name`
- `value`

#### `PUT /api/v1/vault/{userId}/secrets/{keyName}`

Purpose:

- update an encrypted secret

Request body:

```json
{
  "value": "new-secret-value"
}
```

Response data:

- `key_name`
- `updated`

#### `DELETE /api/v1/vault/{userId}/secrets/{keyName}`

Purpose:

- delete one secret

Response data:

- `key_name`
- `deleted`

### F. Agent Execution Logs

#### `POST /api/v1/agent/{taskId}/executions`
#### `POST /api/v1/agents/tasks/{taskId}/executions`

Purpose:

- append an execution log for one task

The singular route is the preferred route. The plural route remains as a legacy compatibility alias.

Request body:

```json
{
  "agentId": "string",
  "actionType": "tool_call | reasoning_step | final_answer",
  "payload": {},
  "status": "pending | success | failed",
  "errorContext": "optional string"
}
```

Legacy snake_case request keys are still accepted:

- `agent_id`
- `action_type`
- `error_context`

Response data:

- `executionId`
- `createdAt`

#### `GET /api/v1/agent/{taskId}/executions?limit=<n>`
#### `GET /api/v1/agents/tasks/{taskId}/executions?limit=<n>`

Purpose:

- list execution history for one task

Response data:

- `executions`

Execution fields:

- `executionId`
- `taskId`
- `agentId`
- `actionType`
- `payload`
- `status`
- `errorContext`
- `createdAt`

### G. Orchestrator Records

All orchestrator routes require:

- header `X-Internal-API-Key`

#### `POST /api/v1/orchestrator/records`

Purpose:

- persist one orchestrator record

Request body:

```json
{
  "orchestrator_task_ref": "required string",
  "task_id": "required string",
  "plan_id": "optional string",
  "subtask_id": "optional string",
  "trace_id": "optional string",
  "data_type": "required string",
  "timestamp": "required RFC3339 timestamp",
  "payload": {},
  "ttl_seconds": 0
}
```

Behavior:

- validates `data_type`
- rejects payloads larger than 256 KB
- task-state-style records can upsert depending on the underlying data type contract

Response data:

- `id`
- `record`

Record fields:

- `id`
- `orchestrator_task_ref`
- `task_id`
- `plan_id`
- `subtask_id`
- `trace_id`
- `data_type`
- `timestamp`
- `payload`
- `ttl_seconds`
- `created_at`

#### `GET /api/v1/orchestrator/records`

Purpose:

- query orchestrator records

Query parameters:

- `data_type` required
- `task_id` optional
- `orchestrator_task_ref` optional
- `from_timestamp` optional
- `to_timestamp` optional
- `state_filter=not_terminal` optional

Behavior:

- requires either `task_id`, `orchestrator_task_ref`, or `state_filter`
- validates timestamp filters when supplied

Response data:

- `records`

#### `GET /api/v1/orchestrator/records/latest?task_id=<id>&data_type=<type>`

Purpose:

- fetch the latest record for a task and data type

Response data:

- `record`

### H. Scheduled Jobs

#### `POST /api/v1/scheduled_jobs`

Purpose:

- create a scheduled job

Request body:

```json
{
  "jobType": "required string",
  "targetKind": "required string",
  "targetService": "required string",
  "status": "required string",
  "scheduleKind": "required string",
  "intervalSeconds": 300,
  "name": "required string",
  "payload": {},
  "nextRunAt": "required RFC3339 timestamp"
}
```

Response data:

- `id`
- `jobType`
- `targetKind`
- `targetService`
- `status`
- `scheduleKind`
- `name`
- `nextRunAt`

#### `POST /api/v1/scheduled_jobs/run_due`

Purpose:

- execute all jobs whose `next_run_at <= now` and whose status is `active`

Current behavior:

- records scheduled job runs
- updates `last_run_at`, `last_success_at`, and `next_run_at`
- produces a dispatch-style result payload
- does not yet perform a real orchestrator/BUS dispatch for external jobs

Response data:

- `runs`

#### `GET /api/v1/scheduled_jobs/{jobId}/runs`

Purpose:

- list recorded runs for a job

Response data:

- `runs`

Run fields:

- `id`
- `jobId`
- `status`
- `targetService`
- `startedAt`
- `finishedAt`
- `result`

## 5. CLI

The service includes `memory-cli`, which can talk to the HTTP API or connect directly to the database.

Build:

```bash
go build -o memory-cli ./cmd/cli
```

### Current commands

Facts:

- `memory-cli facts query --user <uuid> "question"`
- `memory-cli facts all --user <uuid>`
- `memory-cli facts save --user <uuid> "fact text"`

Chat:

- `memory-cli chat history --conversation <uuid> --limit 10`
- `memory-cli chat history --session <uuid> --limit 10`

Agent:

- `memory-cli agent history --task <uuid> --limit 20`

System:

- `memory-cli system events --limit 50`

Vault:

- `memory-cli vault list --user <uuid>`

## 6. Local Development

Required environment variables:

- `DB_HOST`
- `DB_PORT`
- `DB_USER`
- `DB_PASSWORD`
- `DB_NAME`
- `VAULT_MASTER_KEY`
- `INTERNAL_VAULT_API_KEY`

Optional environment variables:

- `OPENAI_API_KEY`
- `PORT`
- `OTEL_EXPORTER_OTLP_ENDPOINT`

Run the server:

```bash
go run ./cmd/server
```

## 7. Testing

The current memory test suite passes with the local Postgres-backed test setup:

```bash
GOCACHE=/tmp/go-build \
DB_HOST=localhost \
DB_PORT=5432 \
DB_USER=user \
DB_PASSWORD=password \
DB_NAME=memory_db \
VAULT_MASTER_KEY=0123456789abcdef0123456789abcdef \
INTERNAL_VAULT_API_KEY=test-vault-key \
go test ./tests -count=1
```

Verified status as of April 21, 2026:

- `go test ./tests -count=1` passes
- `go generate ./cmd/server` succeeds after fixing the `//go:generate` path in `cmd/server/main.go`
- Swagger generation coverage still depends on handler annotation completeness, so generated output should still be reviewed when routes change

## 8. Swagger

Swagger artifacts are committed under:

- `memory/docs/docs.go`
- `memory/docs/swagger.json`
- `memory/docs/swagger.yaml`

Regenerate Swagger artifacts from `memory/` with:

```bash
go generate ./cmd/server
```

The service serves Swagger UI at:

- `/swagger/index.html`

## 9. Known Current Gaps

These are intentionally brief here; the actionable list lives in [to_do.md](./to_do.md).

- fact extraction is still placeholder behavior
- the embedding story is usable but still needs production-hardening/documentation cleanup
- scheduled external dispatch is still stubbed rather than connected to the real orchestrator/BUS path
- the generic typed memory store expected by PR 124 does not exist yet
- Swagger generation now runs successfully, but full Swagger coverage still depends on keeping route annotations complete
