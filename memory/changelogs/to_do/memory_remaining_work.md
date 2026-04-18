# Memory Remaining Work

## Purpose

Track the implementation work that still needs to happen outside of the scoped plan in `memory_decay_and_scheduled_jobs.md`.

## Partial Work

### 1. Ownership and security hardening

Current state:

- user existence validation exists
- chat session ownership is partially enforced by checking whether a session already contains messages for another user

Remaining gap:

- session ownership is still inferred from message rows instead of modeled explicitly
- this is workable short term, but not strong enough as the long-term ownership boundary

Needed work:

1. add an explicit session ownership model, likely `chat_schema.sessions`
2. validate `(session_id, user_id)` against that table
3. decide session bootstrap behavior

### 2. Vault contract consistency

Current state:

- vault routes exist
- vault key middleware returns the standard envelope
- audit logging exists

Remaining gap:

- vault handlers still use `http.Error` in multiple places
- response shapes and error semantics are not fully aligned with the rest of the API

Needed work:

1. replace raw `http.Error` usage with `SuccessResponse(...)` and `ErrorResponse(...)`
2. normalize error codes and payload shape
3. confirm request/response DTO naming consistency

### 3. Swagger generation workflow

Current state:

- the repo is wired to use `swaggo`
- `go:generate` is present
- generated artifacts are committed

Remaining gap:

- full regeneration has not yet been validated in a normal network/tooling environment

Needed work:

1. run `go generate ./cmd/server` in a working environment
2. confirm generated output matches committed docs
3. optionally add a CI drift check

### 4. CLI semantics and agent-facing workflow

Current state:

- immediate CLI bug around `facts all` returning `null` is fixed
- focused CLI fact tests now pass

Remaining gap:

- the product-level semantics still need to be made clearer
- some CLI commands represent structured facts, while others behave more like source-material or chunk operations

Needed work:

1. decide whether to keep `facts` as the umbrella command
2. decide whether “save source material” and “save fact” should be separate commands
3. align CLI names, docs, and demo output to the intended agent workflow

## Open Work

### 1. Memory to orchestrator BUS integration

Needed work:

1. define the event contract for memory -> orchestrator dispatch
2. decide what memory owns:
   - schedule storage only
   - schedule evaluation only
   - schedule evaluation and due-event dispatch
3. implement BUS publisher integration from memory
4. test end-to-end dispatch behavior

### 2. Agent workflow and documentation contract

Needed work:

1. document the expected write pattern for agents
2. document what memory accepts:
   - source material
   - extracted facts
   - source references
3. make examples explicit so agent callers know when they must submit facts themselves
4. update CLI/demo/docs to reinforce that workflow

### 3. Retrieval correctness tests

Needed work:

1. add deterministic tests for similarity ordering
2. add tests for the `created_at DESC` tie-break
3. add tests around active vs archived fact visibility once decay/archive lands

### 4. Changelog cleanup

Needed work:

1. update or replace stale implementation-gap documents
2. make sure “done vs partial vs open” is accurate
3. keep future planning docs scoped so they do not become misleading catch-alls

## Suggested Next Order

### Phase 1: API consistency and ownership

1. finish vault contract cleanup
2. add explicit chat session ownership model
3. refresh stale changelog/gap documents

### Phase 2: Agent-facing contract

1. finalize CLI semantics
2. update docs around source material vs extracted facts
3. update demos to reflect the intended workflow

### Phase 3: Cross-service scheduling integration

1. implement memory -> orchestrator BUS event contract
2. connect scheduled jobs to BUS dispatch
3. test external due-job dispatch behavior

### Phase 4: Validation

1. strengthen retrieval/ranking tests
2. validate Swagger generation end to end
3. add CI checks where useful

## Definition Of Done

This remaining-work track is complete when:

- vault handlers match the rest of the API contract
- chat session ownership is explicit, not inferred
- agent/docs/CLI contract is clear that agents extract and submit facts when required
- memory can dispatch scheduled external work to orchestrator through the BUS
- retrieval correctness is covered by deterministic tests
- stale planning docs have been updated or retired
