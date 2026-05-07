# Memory Service To Do

This file replaces the old `memory/changelogs/` directory.

Everything below was re-checked against the current codebase and test suite on April 21, 2026. Items that were already implemented were folded into `README.md`. Items listed here are the work that is still genuinely open, partially implemented, or still unverified.

## Verified open work

### 1. Replace placeholder fact extraction

Current state:

- `POST /api/v1/personal_info/{userId}/save` can persist chunks and optionally create facts.
- The extractor path in `internal/logic/processor.go` still creates a mock fact payload instead of a real extracted fact.

Remaining work:

- add a real extractor interface and implementation
- define the extracted fact schema clearly
- validate extracted category/key/value/confidence fields
- keep the extractor pluggable for tests

### 2. Finish production embedding hardening

Current state:

- OpenAI embeddings are used when `OPENAI_API_KEY` is set
- a deterministic local embedder is used otherwise for local/dev flows

Remaining work:

- make the local embedder the sole model used
- make the local embedder a full production model

### 3. Replace stubbed scheduled external dispatch with real orchestrator integration

Current state:

- scheduled jobs can be created, run, and inspected
- run history is stored in `scheduler_schema.scheduled_job_runs`
- external jobs currently record a synthetic dispatch-style success result

Remaining work:

- define the real memory -> orchestrator dispatch contract
- publish due external jobs through the real BUS/orchestrator path
- record real success and failure results in run history
- keep the implementation aligned with repo constraints around outbound comms

### 4. Improve Swagger annotation coverage

Current state:

- Swagger artifacts are committed and served by the API
- `go generate ./cmd/server` now runs successfully after fixing the `//go:generate` path

Remaining work:

- improve or expand handler annotations so generated Swagger output fully reflects the intended API surface
- review generated artifacts whenever routes change
- optionally add a CI drift check

### 5. Implement the generic typed memory store expected by PR 124

Current state:

- the service still exposes domain-specific APIs for chat, personal info, vault, orchestrator records, scheduler jobs, system events, and agent executions
- there is no generic typed record surface for agent memory

Remaining work:

- design a generic typed-memory model
- support data types such as `episode`, `agent_memory`, `user_profile`, and `skill_cache`
- support reads by synthetic keys such as domain or user-context identifiers
- support `ReadAllByType`-style loading
- support search over generic session-history-style records
- define retention and TTL semantics where needed

### 6. Keep clarifying the agent-facing docs and CLI contract

Current state:

- the README now documents the current behavior more directly
- the CLI works for facts, chat history, agent history, system events, and vault listing

Remaining work:

- decide whether source-material ingestion and structured-fact creation should remain under the same user-facing "facts" umbrella
- align CLI naming and demos with the intended long-term agent workflow
- document when callers should store source material versus submitting structured facts directly

## Historical notes that are still vague

These appeared in the removed changelogs but were too underspecified to mark complete or incomplete with confidence. They are preserved here so the information is not lost.

- centralized logs
- bootstrap/compose cleanup
- CLI/GitHub modules note from `milestone-2.md`
- lock main
- create issues and PRs
- GitHub owner file

## What was verified as done and removed from the old changelogs

- delete fact route exists and is covered
- singular agent routes exist alongside the legacy routes
- agent routes use the standard response envelope and support `limit`
- user existence validation exists across chat, personal info, and vault flows
- chat ownership checks exist through explicit conversation ownership
- chat idempotency conflict detection exists and is scoped per conversation
- personal-info retrieval uses pgvector distance with deterministic tie-break ordering
- vault routes use the standard envelope
- archived fact retrieval and supersession routes exist
- scheduled-jobs API and run-history surface exist
- orchestrator record routes exist behind the internal API key

## Last verification snapshot

- Code inspection: `memory/cmd/server/main.go`, `memory/internal/api`, `memory/internal/logic`, `memory/internal/storage`, `memory/cmd/cli`, `memory/scripts/init-db.sql`
- Test verification: `go test ./tests -count=1` passed on April 21, 2026
- Swagger regeneration check: `go generate ./cmd/server` succeeded on April 21, 2026 after fixing the generator path, but Swagger coverage still needs review
