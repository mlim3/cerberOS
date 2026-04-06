# Memory Service Implementation Gap Changelog

This document tracks discrepancies between the contract in `/Users/colbydobson/cs/cerberOS/memory/README.md` and the current implementation, plus concrete remediation steps.

## Scope

- Spec source: `/Users/colbydobson/cs/cerberOS/memory/README.md`
- Code reviewed:
- `/Users/colbydobson/cs/cerberOS/memory/cmd/server/main.go`
- `/Users/colbydobson/cs/cerberOS/memory/internal/api/*.go`
- `/Users/colbydobson/cs/cerberOS/memory/internal/logic/processor.go`
- `/Users/colbydobson/cs/cerberOS/memory/internal/storage/queries/*.sql`
- `/Users/colbydobson/cs/cerberOS/memory/scripts/init-db.sql`

## Gaps And Fixes

### 1) Missing endpoint: delete fact

- Discrepancy:
- Spec requires `DELETE /api/v1/personal_info/{userId}/facts/{factId}`.
- Route/handler is not exposed in server mux.
- Impact:
- API contract is incomplete; clients cannot remove incorrect facts.
- Fix needed:
1. Add `DeleteFact` handler in `personal_info_handler.go`.
2. Wire route in `main.go`.
3. Use repository `DeleteFact` query and return:
- `ok: true`, `data.deleted: true`, `data.factId: <id>` on success.
- `not_found` if no row deleted for `userId + factId`.
4. Add integration tests for success and not_found cases.

### 2) Agent endpoint path mismatch

- Discrepancy:
- Spec path: `/api/v1/agent/{taskId}/executions`.
- Implementation path: `/api/v1/agents/tasks/{taskId}/executions`.
- Impact:
- Contract drift; generated SDKs/clients from README will fail.
- Fix needed:
1. Add spec-conformant routes in `main.go`:
- `POST /api/v1/agent/{taskId}/executions`
- `GET /api/v1/agent/{taskId}/executions`
2. Keep legacy routes temporarily for backward compatibility or remove with a versioned deprecation plan.
3. Update swagger/docs to reflect final route set.

### 3) Agent handlers do not use standard envelope

- Discrepancy:
- Spec requires all endpoints to return `{ok,data,error}` envelope.
- Agent handlers use `http.Error` and return raw arrays/no JSON envelope.
- Impact:
- Inconsistent API behavior and client parsing logic.
- Fix needed:
1. Replace `http.Error` responses in `agent_handler.go` with `ErrorResponse(...)`.
2. Wrap successful responses in `SuccessResponse(...)`.
3. POST should return `executionId` and `createdAt` in `data`.
4. GET should return `data.executions`.

### 4) Agent payload contract mismatch (field names + response shape)

- Discrepancy:
- Spec request fields use camelCase: `agentId`, `actionType`, `errorContext`.
- Current handler expects snake_case keys and requires caller-provided `id`.
- Spec POST returns `executionId`, `createdAt`; current POST returns empty `201`.
- Impact:
- Clients following README cannot call endpoint successfully.
- Fix needed:
1. Update request DTO to camelCase JSON tags.
2. Generate `executionId` server-side (UUIDv7).
3. Set `createdAt` in query/return object.
4. Return `{ ok:true, data:{ executionId, createdAt }, error:null }`.

### 5) Agent GET missing `limit` behavior from spec

- Discrepancy:
- Spec defines optional `limit`.
- Current GET returns all executions for task.
- Impact:
- Potential performance issues on large tasks; contract mismatch.
- Fix needed:
1. Add SQL query with `LIMIT`.
2. Parse/validate `limit` query param with sane default/max.
3. Return limited ordered list.

### 6) User existence validation not implemented

- Discrepancy:
- Spec states all user-scoped endpoints validate `userId` exists in `identity_schema.users`.
- No such check is currently done.
- Impact:
- Invalid user IDs can create/read/update data incorrectly; security model not enforced.
- Fix needed:
1. Add repository query: `UserExists(user_id)` in `identity_schema`.
2. Invoke check in all user-scoped handlers before work:
- chat create (for `req.userId`)
- personal_info save/query/all/update/delete
- vault endpoints (already user-scoped)
3. Return `not_found` when user does not exist.

### 7) Chat session ownership validation missing

- Discrepancy:
- Spec requires verification that `sessionId` belongs to `userId` on message creation.
- No session ownership model/check exists.
- Impact:
- Cross-user session pollution risk.
- Fix needed:
1. Introduce `chat_schema.sessions` table (or equivalent ownership mapping) with `session_id`, `user_id`.
2. On POST message, validate `(session_id, user_id)` ownership.
3. Decide session bootstrap strategy:
- create-on-first-message within transaction, or
- explicit session creation endpoint.
4. Return `not_found` for non-owned session references.

### 8) Chat idempotency conflict behavior incomplete

- Discrepancy:
- Spec requires:
- same `idempotencyKey` + same payload => return existing row.
- same `idempotencyKey` + different payload => `conflict`.
- Current code returns existing row for key match without payload comparison.
- Impact:
- Retries with mutated payload are silently accepted as if successful.
- Fix needed:
1. Fetch existing message by `(session_id, idempotency_key)`.
2. Compare relevant payload fields (`userId`, `role`, `content`, `tokenCount`).
3. If mismatch, return `conflict` error.
4. Add tests for both idempotent replay and conflict replay.

### 9) Chat idempotency uniqueness scope is not per session

- Discrepancy:
- Spec says uniqueness is per session.
- Schema has global unique constraint on `idempotency_key`.
- Impact:
- Reusing a key in different sessions fails unexpectedly.
- Fix needed:
1. Replace global unique constraint with composite unique index:
- `(session_id, idempotency_key)` where `idempotency_key IS NOT NULL`.
2. Add migration script for existing deployments.

### 10) Personal query similarity score is mocked/random

- Discrepancy:
- Spec requires cosine similarity score `[0,1]` derived from pgvector distance.
- Current processor assigns random score (`rand.Float64()`).
- Impact:
- Unreliable ranking and incorrect response semantics.
- Fix needed:
1. Change SQL query to return distance:
- `embedding <=> $2 AS distance`.
2. Compute similarity from distance in handler/logic (`similarity = 1 - distance`, clamp to `[0,1]` if needed).
3. Remove random score generation.
4. Add deterministic tests asserting stable ordering.

### 11) Personal query tie-breaker rule not implemented

- Discrepancy:
- Spec: ties broken by `created_at DESC`.
- Current SQL orders only by vector distance.
- Impact:
- Non-deterministic ordering for equal-distance rows.
- Fix needed:
1. Update query ordering:
- `ORDER BY embedding <=> $2 ASC, created_at DESC`.
2. Validate with integration test covering equal-distance fixtures.

### 12) Fact extraction pipeline is placeholder

- Discrepancy:
- Spec describes extraction pipeline creating structured facts from content.
- Current implementation inserts a mock generic fact.
- Impact:
- Endpoint behavior does not satisfy intended product semantics.
- Fix needed:
1. Introduce pluggable extractor interface (similar to embedder).
2. Implement real extraction provider call + schema mapping.
3. Keep mock extractor for tests via dependency injection.
4. Add validation rules for `category`, `factKey`, `factValue`, `confidence`.

### 13) Embedding pipeline is placeholder

- Discrepancy:
- Spec says service calls embedding model.
- Current implementation uses `MockEmbedder` random vectors and `model_version = "mock-model-v1"`.
- Impact:
- Retrieval quality and similarity semantics are invalid.
- Fix needed:
1. Add production embedder adapter and config wiring.
2. Set real model version in persisted chunks.
3. Keep mock embedder for unit tests only.

### 14) Error semantics are inconsistent with spec in several places

- Discrepancy:
- Spec error codes are `invalid_argument | not_found | conflict | internal`.
- Some handlers return plain-text HTTP errors and uppercase codes (vault middleware), and often map domain misses to `internal`.
- Impact:
- Client-side error handling cannot reliably use contract-defined codes.
- Fix needed:
1. Standardize all handlers/middleware to `ErrorResponse(...)`.
2. Normalize error codes to spec values (or update spec if intentionally different).
3. Ensure ownership/user-miss conditions map to `not_found`.

### 15) Input enum validation is partial/missing

- Discrepancy:
- Spec defines constrained enums (`role`, `sourceType`, `severity`, `status`, `actionType`).
- Implementation mostly checks presence only.
- Impact:
- Invalid values can be persisted and break downstream assumptions.
- Fix needed:
1. Add strict enum validation in handlers.
2. Return `invalid_argument` for unsupported values.
3. Add integration tests per enum field.

## Suggested Implementation Order

1. Contract-critical HTTP fixes:
- missing delete endpoint
- agent route/path alignment
- standard response envelope everywhere
2. Security and ownership:
- user existence validation
- session ownership checks
- idempotency conflict semantics
3. Retrieval correctness:
- real similarity score computation
- tie-break ordering
4. Data quality:
- enum validation
- embedder/extractor production adapters
5. Database migration:
- idempotency composite uniqueness per session

## Definition Of Done Checklist

- [ ] All README endpoints exist with matching paths and methods.
- [ ] All endpoints return standardized envelope.
- [ ] User-scoped endpoints enforce user existence + ownership and return `not_found` on misses.
- [ ] Chat idempotency supports same-payload replay and different-payload conflict.
- [ ] Personal query returns true similarity score `[0,1]` and tie-break by `created_at DESC`.
- [ ] Delete fact endpoint implemented and tested.
- [ ] Agent endpoints match request/response contract and support `limit`.
- [ ] Integration tests cover all discrepancy scenarios listed above.
