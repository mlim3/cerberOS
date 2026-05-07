# Memory PR Notes

## Summary

This update fixes the memory microservice to have most of the basic functionality it needs for the CerberOS project.  
It also addresses practical ui conversation persistence and orchestrator_records

1. API/lifecycle/auth consistency work across Memory routes, docs, tests, and CLI behavior
2. a larger chat/task persistence refactor that turns conversations into first-class UI/history objects and separates them from execution tasks
3. the first phase of real orchestrator-state persistence under `/api/v1/orchestrator/*`
4. documentation consolidation that removes the old `memory/changelogs/` directory and folds the verified implemented behavior into `memory/README.md`, with remaining open work moved into `memory/to_do.md`

On the newer chat/task side, before this change the stack was still treating `taskId`, `sessionId`, and `conversationId` too interchangeably. The biggest practical problem was that the UI could not cleanly restore prior chat threads without relying on RAM-held mappings, and the backend had no durable middle layer between chat transcripts and orchestrator execution.

Taken together, this PR now does all of the following:

- standardizes internal auth/header behavior across Memory-facing routes and docs
- adds missing personal-info lifecycle and scheduler API support
- improves CLI/schema compatibility around session/conversation naming
- moves the chat model toward a cleaner persistence structure:
  - `conversations` are the user-facing chat threads
  - `messages` belong to conversations
  - `tasks` are execution runs linked to conversations
- adds the first durable orchestrator-records API and storage layer for:
  - `task_state`
  - `plan_state`
  - `subtask_state`
  - `audit_log`
  - `recovery_event`
  - `policy_event`

and task identity is no longer aliased to conversation identity in the main IO flow.

## Changes Made

### API contract and auth consistency

- standardized internal auth header naming on `X-Internal-API-Key`
- switched vault auth middleware to read `X-Internal-API-Key`
- updated vault handlers to return the standard JSON response envelope on success and failure
- aligned memory tests, demos, presentation notes, and Swagger docs to the `X-Internal-API-Key` header
- updated cross-repo integration/docs references so Memory callers now use `X-Internal-API-Key` consistently instead of mixed `X-API-KEY` / `X-Internal-API-Key` naming

### Personal info lifecycle support

- added archive support for facts:
  - `POST /api/v1/personal_info/{userId}/facts/{factId}/archive`
- added supersession support for facts:
  - `POST /api/v1/personal_info/{userId}/facts/{factId}/supersede`
- added archive-aware fact retrieval with `includeArchived=true`
- added archive storage table support in the DB init script

### Scheduled jobs support

- added scheduled jobs API surface:
  - `POST /api/v1/scheduled_jobs`
  - `POST /api/v1/scheduled_jobs/run_due`
  - `GET /api/v1/scheduled_jobs/{jobId}/runs`
- added scheduler persistence and run-history storage
- added basic internal/external dispatch-style run recording

### Chat naming cleanup

- replaced `sessionId` / `session_id` with `conversationId` / `conversation_id` in the chat persistence path
- updated Memory chat routes, repository code, schema expectations, tests, CLI compatibility logic, and generated docs to use conversation terminology
- kept compatibility logic where needed for older DBs that still have `session_id`

### First-class conversations

- added durable `chat_schema.conversations` support as the canonical chat-thread model
- added user-scoped conversation listing and creation routes:
  - `GET /api/v1/conversations`
  - `POST /api/v1/conversations`
- enforced ownership checks on conversation reads so users cannot read another user's conversation history
- conversation list responses now include latest-task metadata so the UI can restore the thread and know which task stream is currently active

### First-class tasks linked to conversations

- added `chat_schema.tasks`
- each task now belongs to exactly one conversation
- added task routes in Memory:
  - `POST /api/v1/tasks`
  - `GET /api/v1/tasks/{taskId}`
- tasks store the conversation mapping and execution-level fields such as:
  - `conversation_id`
  - `user_id`
  - `orchestrator_task_ref`
  - `trace_id`
  - `status`
  - `input_summary`
  - timestamps

This gives the backend a durable mapping:

- one conversation -> many tasks
- one conversation -> many messages

instead of conflating `conversationId` and `taskId`.

### Orchestrator records

- added `orchestrator_schema.orchestrator_records`
- added internal-only orchestrator routes in Memory:
  - `POST /api/v1/orchestrator/records`
  - `GET /api/v1/orchestrator/records`
  - `GET /api/v1/orchestrator/records/latest`
- all `/api/v1/orchestrator/*` routes are protected by `X-Internal-API-Key`
- added `trace_id`, `plan_id`, and `subtask_id` support on orchestrator records
- implemented write semantics by `data_type`:
  - upsert/replace for `task_state`
  - upsert/replace for `plan_state`
  - upsert/replace for `subtask_state`
  - append-only insert for `audit_log`
  - append-only insert for `recovery_event`
  - append-only insert for `policy_event`
- enforced append-only protection at the DB layer for append-only record types
- added server-side `state_filter=not_terminal` support for orchestrator startup rehydration queries
- enforced a 256KB payload cap on orchestrator writes
- kept orchestrator persistence separate from chat/conversation tables so:
  - `chat_schema.tasks` remains the UI/IO-facing execution bridge
  - `orchestrator_schema.orchestrator_records` remains the detailed lifecycle/audit store

### Message ownership and transcript retrieval

- chat message creation remains conversation-scoped:
  - `POST /api/v1/chat/{conversationId}/messages`
- chat message reads are explicitly user-scoped:
  - `GET /api/v1/chat/{conversationId}/messages?userId=...`
- task lookups can now resolve the owning conversation before loading the transcript

### IO API changes

- removed the real-mode shortcut that treated `conversationId = taskId`
- added Memory client support for:
  - creating conversations
  - creating tasks
  - fetching tasks
  - listing conversations
  - fetching conversation logs
- updated IO API endpoints so:
  - new threads create conversations
  - user sends create tasks inside those conversations
  - transcript loading is conversation-based
  - task streaming remains task-based
- added a conversation-log endpoint on the IO side for UI bootstrap:
  - `GET /api/conversations/{conversationId}/logs`

### Web UI changes

- the sidebar now restores and renders conversations, not task IDs pretending to be conversations
- the UI stores the current active task separately from the conversation ID
- selecting a conversation loads its full transcript
- sending a message creates a fresh task within that conversation and streams against that task's SSE channel
- credential prompts and plan-preview flows now follow the active task within the selected conversation instead of assuming the conversation ID is the task ID

### Chat / CLI compatibility

- added CLI chat history support for `--conversation`
- kept `--session` as a deprecated compatibility alias
- made direct DB CLI chat history work against either:
  - `conversation_id`
  - `session_id`
- updated README examples accordingly

### Testing improvements

- added middleware unit tests for:
  - `extractTraceparentID`
  - vault-key middleware edge cases
- added scheduled-jobs validation/edge-case tests
- added archive negative-path tests
- added supersede negative-path tests
- added CLI compatibility tests for both `--conversation` and `--session`
- added focused conversation/task ownership and retrieval coverage for the new chat/task model
- added focused orchestrator contract coverage for:
  - internal-key enforcement
  - invalid `data_type` rejection
  - `task_state` replacement semantics
  - append-only `audit_log` behavior
  - `records/latest` lookup
  - `state_filter=not_terminal`
- added GitHub Actions PR coverage for Memory in:
  - `.github/workflows/memory-build-and-test.yaml`
- the new PR workflow runs on pull requests to `main` when:
  - `memory/**` changes
  - `docker-compose.yml` changes
- the workflow:
  - starts `memory-db`
  - waits for Postgres readiness
  - builds `memory-cli`
  - runs `go test -v ./internal/api ./cmd/server`
  - runs `go test -v ./tests -count=1`

### Root docs updates

- updated `docs/Memory.md` to reflect the full current feature set (conversations/tasks, orchestrator records, scheduled jobs, fact lifecycle, updated vault/orchestrator internal-key scope) and added a pointer to `memory/README.md` for the full spec
- updated `docs/integration_memory.md` to cover the new conversation/task model, orchestrator records API and write semantics, fact lifecycle endpoints, and the expanded `X-Internal-API-Key` scope that now includes orchestrator routes in addition to vault routes

### Documentation consolidation

- removed the old `memory/changelogs/` directory
- audited the changelog entries against the current Memory code and test suite
- moved still-open items into `memory/to_do.md`
- rewrote `memory/README.md` as the single comprehensive Memory spec/current-state reference — it now covers:
  - service purpose and architecture overview
  - internal-only route families and auth requirements
  - current embedding and fact extraction behavior
  - API conventions (base path, content type, time format, ID generation, response envelope)
  - full data model summary across the chat, personal-info, vault, orchestrator, and scheduler domains
  - complete endpoint specification for all route families: health, conversations/tasks/messages, personal info and fact lifecycle (including archive and supersession), system events, vault secrets, agent execution logs, orchestrator records, and scheduled jobs
  - CLI commands reference
  - local development environment variables and server startup
  - test suite setup and verified status
  - Swagger artifact locations and regeneration instructions
  - known current gaps with a pointer to `to_do.md` for actionable follow-up
- kept README in spec form instead of reducing it to a short service overview
- fixed the `//go:generate` path in `cmd/server/main.go` so `go generate ./cmd/server` now works again
- expanded the `swag` scan scope, fixed handler annotation issues, and regenerated Swagger so the current mounted Memory routes are reflected in the generated artifacts

## Verification

Focused verification completed:

```bash
cd /Users/colbydobson/cs/cerberOS/memory
GOMODCACHE=/tmp/gomodcache \
GOCACHE=/tmp/go-build \
go test ./tests -run 'Test(ChatAndIdempotency|ChatOwnership_BlackBox)' -count=1
```

```bash
cd /Users/colbydobson/cs/cerberOS/memory
GOCACHE=/tmp/go-build \
DB_HOST=localhost DB_PORT=5432 DB_USER=user DB_PASSWORD=password DB_NAME=memory_db \
VAULT_MASTER_KEY=0123456789abcdef0123456789abcdef \
INTERNAL_VAULT_API_KEY=test-vault-key \
go test ./tests -run 'TestOrchestrator' -count=1
```

```bash
cd /Users/colbydobson/cs/cerberOS
bun test io/api/src --run ''
```

At the time of this note:

- focused Memory chat/conversation/task tests pass
- focused Memory orchestrator contract tests pass
- `go test ./tests -count=1` passes for the current consolidated Memory suite
- IO API tests pass
- earlier memory/tests and memory/internal/api verification work for the auth/lifecycle pass was also green during that stage of the work
- `go test ./internal/api ./cmd/server` passes for the Memory server/router wiring
- the changelog-to-README audit was verified against current code plus the passing Memory test suite

Checks that were attempted but not fully available in this environment:

- web TypeScript compile was blocked by missing local package/type installation:
  - missing `vite/client`
- IO API TypeScript compile was blocked by missing local workspace dependencies/types in the environment:
  - `hono`
  - `@cerberos/io-core`
  - node/bun type packages

## Schema / Runtime Notes

- existing DBs may still require a real migration if they were initialized before the `session_id -> conversation_id` rename
- the Memory repository includes schema-upgrade logic for older chat tables, but production deployment should still use an explicit migration path
- `chat_schema.tasks` is now part of the intended durable model and should be treated as the execution mapping layer for UI-facing conversations

## Swagger / Docs

Swagger was regenerated earlier for the conversation-path rename, but the newer task and orchestrator routes introduced here still need a fresh regeneration pass if we want the generated Swagger artifacts to fully match the code.

Docs/code paths updated in this broader effort include:

- internal auth header naming
- vault header usage and response envelope consistency
- personal info archive/supersession behavior
- scheduled jobs API/docs
- README/to_do consolidation after changelog audit
- Memory chat/task routes
- Memory PR CI workflow
- Memory schema/init scripts
- IO Memory client contract
- IO web bootstrap/history loading behavior
- CLI conversation/session compatibility docs

## Future Follow-Up

The next logical steps after this PR are:

1. replace the hardcoded/default UI user identity path with the real auth-derived user source
2. add explicit DB migrations for the chat schema changes instead of relying on init/ensure behavior alone
3. decide whether the new Memory PR workflow should stay isolated or eventually fold into a broader repo-wide smoke/PR test workflow
4. thread orchestrator task-state updates back into the durable `chat_schema.tasks` rows more completely
5. decide whether `messages` should also persist a first-class `task_id` column for stronger attribution
6. keep improving scheduled-job dispatch so it uses the real orchestrator/BUS path instead of the current stubbed dispatch-style recording
7. continue the larger typed-memory follow-up work where applicable:
   - `episode`
   - `agent_memory`
   - `user_profile`
   - `skill_cache`
8. strengthen the fact extraction pipeline and its production documentation

## Suggested PR Framing

If this becomes a GitHub PR, the description should emphasize:

- contract cleanup and consistency
- auth/header standardization
- missing lifecycle endpoints that were added
- scheduler API introduction
- separating conversations from tasks as distinct durable concepts
- removing the `conversationId = taskId` shortcut
- adding a real task-to-conversation mapping layer in Memory
- adding first-phase `/api/v1/orchestrator/*` persistence with DB-enforced append-only semantics
- updating IO/web flows so chat history is conversation-driven while execution remains task-driven
- improved ownership enforcement for restored chat history
- expanded edge-case coverage with passing focused verification
