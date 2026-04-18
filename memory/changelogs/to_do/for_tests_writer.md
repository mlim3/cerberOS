# For Tests Writer

## Purpose

This document is for an agent writing tests for the remaining memory-service work.

It describes only:

- intended end-state behavior
- expected API and CLI shapes
- what should be validated from the outside

It intentionally avoids:

- internal implementation details
- current bugs
- project-specific shortcuts
- assumptions based on current handler or schema internals

The tests should be written as black-box validations of the intended contract.

## Test Harness And Framework

Use the following defaults unless the task explicitly requires something else:

- language: Go
- test framework: the Go standard library `testing` package
- test style: black-box integration and contract tests
- command execution: use `os/exec` for CLI verification
- HTTP verification: use real HTTP requests against the service, or `httptest` only when the goal is still black-box API behavior

### Test locations

Use these conventions:

- service and integration tests: `memory/tests`
- CLI behavior tests: `memory/tests`
- package-local unit tests only when there is a strong reason to test a boundary in isolation

### Environment assumptions

When tests require the backing database:

- use the local Docker-backed Postgres setup for the memory service
- assume database configuration comes from environment variables:
  - `DB_HOST`
  - `DB_PORT`
  - `DB_USER`
  - `DB_PASSWORD`
  - `DB_NAME`
- assume vault-related tests may require:
  - `VAULT_MASTER_KEY`
  - `INTERNAL_VAULT_API_KEY`

### Preferred verification style

Prefer:

- full request/response assertions
- visible CLI stdout/stderr and exit-code assertions
- seeded fixture data and realistic payloads

Avoid:

- testing internal helper functions unless explicitly asked
- coupling tests to implementation-specific internals
- assuming current repo bugs or temporary compatibility behavior

## Scope Of Remaining Work

The remaining work this test plan should cover includes:

1. explicit ownership and security hardening
2. vault API contract consistency
3. clear agent-facing CLI and API usage contract
4. scheduled external dispatch behavior
5. retrieval correctness and archive visibility behavior

This document does not cover low-level migration testing or internal storage-layer tests.

## Global API Expectations

All API endpoints should follow the standard JSON envelope.

### Successful response

```json
{
  "ok": true,
  "data": {},
  "error": null
}
```

### Error response

```json
{
  "ok": false,
  "data": null,
  "error": {
    "code": "invalid_argument | not_found | conflict | internal",
    "message": "Human readable message",
    "details": null
  }
}
```

Tests should validate:

- correct HTTP status code
- correct envelope shape
- correct `error.code`
- presence or absence of expected fields in `data`

## Ownership And Security Contract

### User-scoped endpoints

Any endpoint acting on user-owned data must reject unknown users.

Expected behavior:

- valid but unknown `userId` returns `404 not_found`
- malformed `userId` returns `400 invalid_argument`

### Chat session ownership

Chat sessions are user-owned resources.

Expected behavior:

- a session owned by one user must not be writable by another user
- a session owned by one user must not leak data to another user
- non-owned session access should return `404 not_found`, not cross-user existence information

Tests should cover:

1. owner can write and read
2. different user cannot write to the same session
3. different user cannot access the same session’s data

## Vault Contract

Vault endpoints are internal-only and require the internal API key.

### Authentication behavior

If the API key is missing or invalid:

- status should be `401`
- body should still use the standard error envelope

### Expected request and response shapes

#### Create secret

`POST /api/v1/vault/{userId}/secrets`

Request:

```json
{
  "key_name": "OPENAI_API_KEY",
  "value": "secret-value"
}
```

Expected behavior:

- valid request returns `201`
- unknown user returns `404`
- malformed user id returns `400`
- missing required fields returns `400`

#### Get secret

`GET /api/v1/vault/{userId}/secrets?key_name=OPENAI_API_KEY`

Expected behavior:

- valid request returns `200`
- response includes the stored key name and decrypted value
- unknown user returns `404`
- missing query parameter returns `400`

#### Update secret

`PUT /api/v1/vault/{userId}/secrets/{keyName}`

Request:

```json
{
  "value": "new-secret-value"
}
```

Expected behavior:

- valid request returns `200`
- unknown user returns `404`
- missing or invalid value returns `400`

#### Delete secret

`DELETE /api/v1/vault/{userId}/secrets/{keyName}`

Expected behavior:

- valid request returns `200`
- unknown user returns `404`
- malformed input returns `400`

Tests should assert that all vault routes use the standard envelope on both success and failure.

## Agent-Facing Memory Contract

The intended product behavior is:

- memory should store source material
- agents should extract and submit structured facts when needed
- memory should not rely on an internal LLM to derive facts implicitly as the primary contract

Tests should be written against the external contract only:

- source material ingestion
- fact insertion/update
- fact retrieval
- archival visibility behavior

The tests should not assume any specific internal extraction implementation.

## CLI Contract

The CLI is agent-facing and should expose clear, stable behavior.

### General expectations

- JSON-producing commands should always emit valid JSON arrays or objects
- empty list results should emit `[]`, not `null`
- success/failure should be obvious from process exit code and output

### Facts commands

#### Query facts

Example:

```bash
./memory-cli -db "env" facts query --user 11111111-1111-1111-1111-111111111111 "what do I prefer?"
```

Expected behavior:

- valid command exits successfully
- output is a JSON array
- each item contains an `id` and `content`

#### Get all facts

Example:

```bash
./memory-cli -db "env" facts all --user 11111111-1111-1111-1111-111111111111
```

Expected behavior:

- valid command exits successfully
- output is a JSON array
- empty result is `[]`

#### Save fact

Example:

```bash
./memory-cli -db "env" facts save --user 11111111-1111-1111-1111-111111111111 "I prefer writing Go code."
```

Expected behavior:

- valid command exits successfully
- output is a success message
- the saved fact should subsequently appear in `facts all`

### Chat command

Example:

```bash
./memory-cli -db "env" chat history --session aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa --limit 3
```

Expected behavior:

- output is a JSON array
- each item includes `id`, `role`, `content`, `created_at`
- empty result is `[]`

### Agent command

Example:

```bash
./memory-cli -db "env" agent history --task bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb --limit 5
```

Expected behavior:

- output is a JSON array
- each item includes `id`, `task_id`, `status`, `created_at`
- empty result is `[]`

### System command

Example:

```bash
./memory-cli -db "env" system events --limit 10
```

Expected behavior:

- output is a JSON array
- each item includes `id`, `event_type`, `message`, `created_at`
- empty result is `[]`

## Scheduled Jobs Contract

The scheduled-jobs work should support both:

- internal memory jobs
- external jobs targeting outside services such as orchestrator

Tests should validate behavior, not storage layout.

### Required behavior

1. a scheduled job can be stored with:
   - job identity
   - target service
   - schedule
   - payload
   - next run time
   - status

2. due jobs can be identified and executed

3. job execution should produce run history

4. external jobs should dispatch to the intended downstream service contract

5. failed jobs should record failure state and remain auditable

### External dispatch behavior

For jobs targeting orchestrator or another outside service:

- the job should be dispatchable when due
- dispatch should include the intended target metadata and payload
- a successful dispatch should be distinguishable from a failed dispatch

Tests should focus on:

- whether a due external job triggers the expected external action or event
- whether run status is recorded correctly

## Fact Decay And Archive Visibility Contract

These tests should align with the separate decay/scheduled-jobs plan.

### Active vs archived facts

Expected behavior:

- active facts appear in normal retrieval
- archived facts do not appear in normal retrieval
- archived facts remain queryable when explicitly requested

### Archive reasons

The externally meaningful archive reasons are:

- decayed
- contradicted
- superseded
- manually_archived

Tests should validate that:

- the prior fact is no longer treated as active
- history remains queryable
- replacement relationships are visible where the contract exposes them

### Contradiction and supersession behavior

Expected behavior:

- when a newer fact replaces an older conflicting fact, the older one should no longer appear in active retrieval
- the older one should still be retrievable through archive-aware access

### Decay behavior

Expected behavior:

- stale/decayed facts eventually stop appearing in default retrieval
- they remain queryable through archive-aware access
- decay and archive transitions should happen via scheduled job execution, not by disappearing silently

## Retrieval Correctness

Tests should treat ranking as user-visible behavior.

Expected behavior:

- more relevant results rank ahead of less relevant ones
- ordering should be deterministic for ties
- active facts should outrank archived facts in normal retrieval by virtue of archived facts being excluded

Tests should avoid asserting implementation-specific scores.
They should instead assert:

- result ordering
- presence/absence
- stability of repeated queries over the same fixture data

## Test Style Guidance

The tests written from this document should:

- use public API and CLI contracts
- validate end-state behavior only
- avoid knowledge of internal tables, handlers, or specific code paths unless a test is explicitly at the storage-contract boundary
- prefer realistic inputs and fixture data over synthetic white-box assumptions

Good assertions:

- response code
- envelope shape
- visible resource state
- visible command output
- ability or inability to access data across ownership boundaries
- whether archived data is hidden or visible under the correct conditions

Bad assertions:

- assumptions about which internal function was called
- assumptions about which table a behavior was implemented through
- assumptions based on current bugs or temporary compatibility shims

## Minimum Acceptance Coverage

At minimum, the test suite for the remaining work should cover:

1. unknown and malformed user handling across user-scoped endpoints
2. chat session ownership enforcement
3. vault envelope consistency on success and failure
4. CLI empty-list behavior and save/retrieve fact flow
5. scheduled external job dispatch behavior
6. active vs archived fact visibility
7. contradiction/supersession removing facts from active retrieval while preserving archive access
8. deterministic retrieval ordering checks
