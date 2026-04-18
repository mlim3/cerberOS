# PR 124 Memory Service Expectations

## Purpose

Document what PR `#124` (`feat: Autonomous Skill Generation on Complex Tasks`) now expects from the memory service, what that functionality is for, and how that compares to the current implementation in `/Users/colbydobson/cs/cerberOS/memory`.

This is a contract and gap note. It does not describe code already changed in the `memory` service. The PR itself does not modify files under `/Users/colbydobson/cs/cerberOS/memory`; it changes the agents side and the shared message/types contract in ways that expand what the memory service is expected to support.

## PR Context

- PR: `feat: Autonomous Skill Generation on Complex Tasks`
- PR number: `#124`
- Repository: `mlim3/cerberOS`
- Reviewed scope relevant to memory:
- `agents-component/cmd/agent-process/memorytools.go`
- `agents-component/cmd/agent-process/session.go`
- `agents-component/cmd/agent-process/skillsynthesis.go`
- `agents-component/cmd/agent-process/loop.go`
- `agents-component/internal/factory/factory.go`
- `agents-component/internal/memory/memory.go`
- `agents-component/pkg/types/types.go`

## High-Level Change

Before this PR, the local memory service is primarily a domain-specific REST service for:

- chat transcripts
- personal info storage and semantic retrieval
- system events
- vault secrets
- agent task execution logs

After PR `#124`, the agents side expects memory to also behave like a generic typed state store that can:

- persist arbitrary agent memory records by type
- retrieve records by synthetic keys such as domain or user context
- retrieve all records of a given type across agents
- search past session entries by query
- store synthesized skills for reuse in future sessions

## What The New Functionality Is For

The expected memory functionality now supports four new agent capabilities.

### 1. Domain-scoped reusable memory

Expected data type:

- `agent_memory`

Purpose:

- Persist distilled facts learned during one task so future agents in the same domain can start with those facts in their system prompt.

Examples:

- a domain-specific API always paginates responses
- a tool requires a timeout to avoid hanging
- an external system returns timestamps in a specific format

Expected usage pattern:

- agents write facts using `memory_update`
- facts are stored under synthetic key `agent_id = "domain:<domain>"`
- factory reads them at spawn and injects them into prompt context

### 2. User preference memory

Expected data type:

- `user_profile`

Purpose:

- Persist durable user preferences and working style observations so later agents can tailor behavior without relearning them every task.

Examples:

- user prefers concise summaries
- user wants code examples in Go
- user prefers bullet points over prose

Expected usage pattern:

- agents write observations using `profile_update`
- observations are stored under synthetic key `agent_id = "user:<user_context_id>"`
- factory reads them at spawn and injects them into prompt context

### 3. Session recall / history search

Expected data type:

- `episode`

Purpose:

- Let an agent search prior session turns instead of re-running tools when it only needs to recall something it already observed.

Examples:

- recall how a previous rate-limit issue was handled
- recover a previously seen command result
- re-use a past excerpt without recomputing it

Expected usage pattern:

- agents call `memory_search`
- memory service receives a read request with `search_query` and `max_results`
- service returns the most relevant session excerpts

### 4. Synthesized skill persistence

Expected data type:

- `skill_cache`

Purpose:

- Persist reusable synthesized skills extracted from complex tasks so they can be loaded into the skill hierarchy on future startup.

Examples:

- a reusable multi-step domain procedure discovered after 5+ tool calls
- a new structured command flow derived from successful execution history

Expected usage pattern:

- agent synthesizes a `SkillNode` after a sufficiently complex successful task
- synthesized skill is written to memory as `skill_cache`
- factory loads all `skill_cache` entries at startup via `ReadAllByType("skill_cache")`

## Agent-Side Contract Introduced By PR 124

From the PR diff, the memory service is now expected to support a generic record model with behavior equivalent to:

- `Write(record)`
- `Read(agentID, dataType)`
- `ReadAllByType(dataType)`
- `Search(agentID, dataType, contextTag, searchQuery, maxResults)`

The request/record shapes implied by the PR include:

- `agent_id`
- `session_id`
- `data_type`
- `ttl_hint`
- `payload`
- `tags`
- `trace_id`
- `context_tag`
- `search_query`
- `max_results`

Important new data types explicitly used by the PR:

- `episode`
- `agent_memory`
- `user_profile`
- `skill_cache`

Important lookup patterns explicitly used by the PR:

- `Read("domain:<domain>", "agent_memory")`
- `Read("user:<user_context_id>", "user_profile")`
- `ReadAllByType("skill_cache")`
- `MemoryReadRequest{AgentID, DataType: "episode", ContextTag: "session", SearchQuery, MaxResults}`

## Current Memory Service Surface

The current memory service routes exposed in `/Users/colbydobson/cs/cerberOS/memory/cmd/server/main.go` are:

- `GET /api/v1/healthz`
- `POST /api/v1/chat/{sessionId}/messages`
- `GET /api/v1/chat/{sessionId}/messages`
- `POST /api/v1/personal_info/{userId}/save`
- `POST /api/v1/personal_info/{userId}/query`
- `GET /api/v1/personal_info/{userId}/all`
- `PUT /api/v1/personal_info/{userId}/facts/{factId}`
- `DELETE /api/v1/personal_info/{userId}/facts/{factId}`
- `POST /api/v1/system/events`
- `GET /api/v1/system/events`
- `POST /api/v1/vault/{userId}/secrets`
- `PUT /api/v1/vault/{userId}/secrets/{keyName}`
- `GET /api/v1/vault/{userId}/secrets`
- `DELETE /api/v1/vault/{userId}/secrets/{keyName}`
- `POST /api/v1/agent/{taskId}/executions`
- `GET /api/v1/agent/{taskId}/executions`
- legacy aliases under `/api/v1/agents/tasks/{taskId}/executions`

Relevant current implementation characteristics:

- agent logs are keyed by `task_id`, not by `(agent_id, data_type)`
- personal info semantic retrieval is only for user-scoped personal info chunks
- there is no generic memory record table exposed through the API
- there is no generic record tagging model
- there is no `ReadAllByType` behavior
- there is no session-history search for arbitrary `episode` entries

## What Exists Today That Is Related

These current features are adjacent, but they do not satisfy the new PR contract by themselves.

### Agent task execution logs

Current files:

- `/Users/colbydobson/cs/cerberOS/memory/internal/api/agent_handler.go`
- `/Users/colbydobson/cs/cerberOS/memory/internal/storage/queries/agent_logs.sql`

Current behavior:

- append-only execution rows in `agent_logs_schema.task_executions`
- retrieval by `task_id`
- request shape tailored to execution logs:
  - `agentId`
  - `actionType`
  - `payload`
  - `status`
  - `errorContext`

Why this is not enough:

- no `data_type`
- no generic `tags`
- no synthetic key lookup by `domain:<domain>` or `user:<user_context_id>`
- no `ReadAllByType`
- no search semantics

### Personal info semantic search

Current files:

- `/Users/colbydobson/cs/cerberOS/memory/internal/api/personal_info_handler.go`
- `/Users/colbydobson/cs/cerberOS/memory/internal/logic/processor.go`

Current behavior:

- query embedding generation
- similarity search against `personal_info_schema.personal_info_chunks`
- results are user scoped and intended for personal memory

Why this is not enough:

- it is specific to personal info, not agent state
- it operates on embedded user memory chunks, not generic session `episode` entries
- it does not expose the `(agent_id, data_type, context_tag, search_query, max_results)` contract expected by the agents side

## Gaps Against The New Expected Contract

### 1. Missing generic typed-memory store

Current gap:

- the memory service has no generic storage model for arbitrary typed memory records

Expected functionality:

- store append-only records with:
  - `agent_id`
  - `session_id`
  - `data_type`
  - `ttl_hint`
  - `payload`
  - `tags`
  - timestamps

Why it is needed:

- all new agent-side memory features depend on a shared typed record model rather than domain-specific DTOs

### 2. Missing support for new data types

Current gap:

- the service has no first-class support for:
  - `episode`
  - `agent_memory`
  - `user_profile`
  - `skill_cache`

Expected functionality:

- write and read records under each of those `data_type` values

Why it is needed:

- `episode` powers session recall
- `agent_memory` powers domain-level carryover knowledge
- `user_profile` powers user preference carryover
- `skill_cache` powers synthesized skill reuse across restarts

### 3. Missing reads by synthetic key

Current gap:

- current reads are by task id, user id, or other domain-specific identifiers

Expected functionality:

- support reads like:
  - `Read("domain:web", "agent_memory")`
  - `Read("user:<user_context_id>", "user_profile")`

Why it is needed:

- the PR standardizes these synthetic keys so future agents can access shared memory without knowing historic agent IDs

### 4. Missing `ReadAllByType`

Current gap:

- no route or repository behavior loads all records for a `data_type`

Expected functionality:

- read all `skill_cache` records across agents and sessions

Why it is needed:

- factory startup needs to rehydrate synthesized skills into the live skill hierarchy

### 5. Missing session-history search over `episode` records

Current gap:

- no full-text or equivalent search exists for session turn storage

Expected functionality:

- accept:
  - `agent_id`
  - `data_type = "episode"`
  - `context_tag = "session"`
  - `search_query`
  - `max_results`
- return top relevant session excerpts

Why it is needed:

- `memory_search` depends on recall over prior turns without re-running tools

### 6. Missing generic tags / metadata support

Current gap:

- current agent and system log shapes do not provide a generic `tags` map

Expected functionality:

- arbitrary key/value tags stored with typed records

Why it is needed:

- PR uses tags like:
  - `domain`
  - `context`
  - `user_context_id`
  - `origin`
  - `skill_name`

### 7. Missing arbitrary JSON payload support for reusable skill records

Current gap:

- current execution logs store payload, but not as part of a generic typed-memory abstraction

Expected functionality:

- persist synthesized `SkillNode` JSON blobs under `skill_cache`

Why it is needed:

- synthesized skills must survive restart and be reloaded into the skill manager

### 8. Missing TTL-aware typed records

Current gap:

- no generic `ttl_hint` field exists in the current memory service surface

Expected functionality:

- record-level TTL hints or equivalent retention metadata

Why it is needed:

- the typed-memory contract includes TTL intent, even if some records like `skill_cache`, `agent_memory`, and `user_profile` effectively use no expiry

## Expected Memory Service Responsibilities After PR 124

If the memory service is to satisfy the new contract implied by the PR, it should now be responsible for:

1. Accepting generic append-only typed memory writes from agents or the orchestrator-facing layer.
2. Persisting session turns as `episode` records for later retrieval and search.
3. Persisting domain-scoped distilled facts as `agent_memory`.
4. Persisting user preference observations as `user_profile`.
5. Persisting synthesized skill definitions as `skill_cache`.
6. Returning records by stable synthetic keys, not only by historical task IDs.
7. Returning all records of a type when startup rehydration needs cross-agent data.
8. Supporting search-based recall for past session entries.

## Suggested Implementation Direction

This changelog does not prescribe the final API shape, but the service likely needs one of these approaches:

- add a new generic state-memory API alongside the existing REST domain APIs
- or add an internal bus/NATS-facing adapter backed by new generic storage tables
- or do both, with the REST layer remaining domain-specific and the orchestration layer using a lower-level typed-memory interface

Regardless of transport, the storage and repository layer likely needs:

- a generic memory records table/schema
- indexes for `(agent_id, data_type)`
- indexes or query paths for `data_type`
- support for structured tags
- support for arbitrary JSON payloads
- a search path for `episode` content

## Concrete Next Questions

Before implementation, these design choices need to be settled:

1. Should the generic typed-memory contract be exposed over REST, over NATS, or only internally?
2. Should `episode` search be plain PostgreSQL full-text search, pgvector-backed semantic search, or a hybrid?
3. Should `skill_cache` records live in the same generic table as `episode` and profile/memory records, or in a separate table optimized for startup loading?
4. How should retention work for `episode` records versus effectively permanent records like `skill_cache`?
5. Which component owns translation from the PR's `MemoryReadRequest` / `MemoryWrite` shapes into concrete memory service routes or bus messages?

## Summary

PR `#124` does not directly change the local `memory` service code, but it materially changes what the service is expected to support.

The memory service is now expected to evolve from a domain-specific memory API into a more general agent-state backend that supports:

- typed memory records
- domain memory
- user profile memory
- session recall
- synthesized skill persistence
- cross-agent type-based loading

At the time of this review, those capabilities are expected by the agents side but are not implemented in `/Users/colbydobson/cs/cerberOS/memory`.
