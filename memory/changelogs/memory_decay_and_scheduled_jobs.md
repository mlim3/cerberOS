# Memory Decay And Scheduled Jobs Implementation Plan

## Goal

Implement fact decay and archival as scheduled jobs, while keeping archived facts queryable and establishing a single scheduling model that supports both internal memory jobs and jobs for outside services.

This plan assumes:

- active facts stay in `personal_info_schema.user_facts`
- non-active historical facts move to an archive table instead of being deleted
- decay, archival, contradiction, and supersession are all handled through explicit lifecycle transitions
- all automated lifecycle work runs through scheduled jobs rather than inline background goroutines
- internal and external scheduled jobs share one scheduler model and one primary jobs table

## Design Decisions

### 1. Fact lifecycle model

Facts have two storage states:

- active: current facts used by normal retrieval
- archived: historical facts excluded from default retrieval but still queryable on demand

Facts also have lifecycle reasons:

- decayed
- contradicted
- superseded
- manually_archived

Archive is the storage destination. The reason explains why the fact left the active table.

### 2. Decay model

Each active fact gets decay metadata:

- `decay_score`
- `decay_rate`
- `refresh_count`
- `last_refreshed_at`
- `last_observed_at`
- `fact_type` or `stability_class`

Decay is time-based. Recommended first implementation:

- linear decay for operational clarity
- effective score computed from:
  - stored `decay_score`
  - elapsed time since `last_refreshed_at`
  - `decay_rate`

Recommended formula:

`effective_decay_score = max(0, decay_score - decay_rate * elapsed_days)`

Refresh should use diminishing returns so repeatedly confirmed facts last longer without becoming immortal.

Recommended refresh formula:

`new_decay_score = min(1.0, base_refresh_score + refresh_bonus)`

Where:

- `base_refresh_score` defaults to something like `0.7`
- `refresh_bonus = min(0.25, 0.05 * ln(1 + refresh_count + 1))`

This keeps the model simple, bounded, and predictable.

### 3. Archive policy

Facts move to archive when any of the following happens:

- effective decay score falls below threshold
- a newer fact contradicts the old fact
- a newer fact explicitly supersedes the old fact
- a user or agent manually archives a fact

Archived facts are never physically deleted by default.

### 4. Scheduled jobs model

Use one jobs table for all scheduled work.

Do not split internal and external scheduled jobs into separate tables unless they later require materially different storage, auth, or lifecycle rules.

Instead, classify jobs by metadata:

- `job_type`
- `target_kind`
- `target_service`
- `payload`

This keeps scheduling, execution, retry, and audit history unified.

## Schema Changes

### A. Extend active facts table

Add to `personal_info_schema.user_facts`:

- `fact_type VARCHAR(50) NOT NULL DEFAULT 'semi_durable'`
- `decay_score FLOAT NOT NULL DEFAULT 1.0`
- `decay_rate FLOAT NOT NULL DEFAULT 0.01`
- `refresh_count INT NOT NULL DEFAULT 0`
- `last_refreshed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`
- `last_observed_at TIMESTAMPTZ`
- `source_priority INT NOT NULL DEFAULT 0`

Notes:

- `fact_type` or `stability_class` determines the default decay profile
- `source_priority` is optional, but useful later for contradiction resolution

### B. Add archive table

Create `personal_info_schema.user_facts_archive` with:

- `archive_id UUID PRIMARY KEY`
- `fact_id UUID NOT NULL`
- `user_id UUID NOT NULL`
- `category VARCHAR(50)`
- `fact_key VARCHAR(100) NOT NULL`
- `fact_value JSONB NOT NULL`
- `confidence FLOAT`
- `version INT`
- `fact_type VARCHAR(50) NOT NULL`
- `decay_score FLOAT NOT NULL`
- `decay_rate FLOAT NOT NULL`
- `refresh_count INT NOT NULL`
- `last_refreshed_at TIMESTAMPTZ`
- `last_observed_at TIMESTAMPTZ`
- `archive_reason VARCHAR(50) NOT NULL`
- `superseded_by_fact_id UUID`
- `archived_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`
- `original_created_at TIMESTAMPTZ`
- `original_updated_at TIMESTAMPTZ`

Indexes:

- `(user_id)`
- `(fact_id)`
- `(archive_reason)`
- `(superseded_by_fact_id)`
- `(archived_at DESC)`

### C. Add scheduled jobs table

Create `scheduler_schema.scheduled_jobs` with:

- `id UUID PRIMARY KEY`
- `job_type VARCHAR(100) NOT NULL`
- `target_kind VARCHAR(20) NOT NULL`
- `target_service VARCHAR(100) NOT NULL`
- `status VARCHAR(20) NOT NULL`
- `schedule_kind VARCHAR(20) NOT NULL`
- `schedule_expr TEXT`
- `interval_seconds INT`
- `payload JSONB NOT NULL DEFAULT '{}'::jsonb`
- `next_run_at TIMESTAMPTZ NOT NULL`
- `last_run_at TIMESTAMPTZ`
- `last_success_at TIMESTAMPTZ`
- `last_failure_at TIMESTAMPTZ`
- `retry_count INT NOT NULL DEFAULT 0`
- `max_retries INT NOT NULL DEFAULT 3`
- `created_by VARCHAR(100)`
- `created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`
- `updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`

Recommended enums by convention:

- `job_type`: `fact_decay_scan`, `fact_archive_move`, `external_dispatch`, `memory_maintenance`
- `target_kind`: `internal`, `external`
- `status`: `active`, `paused`, `disabled`
- `schedule_kind`: `interval`, `cron`

Indexes:

- `(status, next_run_at)`
- `(job_type)`
- `(target_kind, target_service)`

### D. Add scheduled job runs table

Create `scheduler_schema.scheduled_job_runs` with:

- `id UUID PRIMARY KEY`
- `job_id UUID NOT NULL`
- `started_at TIMESTAMPTZ NOT NULL`
- `finished_at TIMESTAMPTZ`
- `status VARCHAR(20) NOT NULL`
- `attempt INT NOT NULL DEFAULT 1`
- `error TEXT`
- `result JSONB`

Indexes:

- `(job_id, started_at DESC)`
- `(status)`

## Internal Job Types

### 1. `fact_decay_scan`

Purpose:

- periodically find active facts whose effective decay score is below threshold

Behavior:

- scans active facts in batches
- computes effective decay score from stored metadata
- selects facts to archive
- emits archive work inline or via follow-up archive job payloads

### 2. `fact_archive_move`

Purpose:

- move eligible facts from active table to archive table atomically

Behavior:

- inserts archive row
- copies lifecycle metadata
- removes row from active table
- preserves source references and provenance linkage

### 3. `external_dispatch`

Purpose:

- kick off work for outside services through the BUS

Examples:

- notify orchestrator that a cron-backed task is due
- emit events related to memory-owned schedules

## External Job Support

Keep external jobs in the same `scheduled_jobs` table.

Use:

- `target_kind = 'external'`
- `target_service = 'orchestrator'` or other service name
- `payload` for dispatch contract

This allows memory to store both:

- internal maintenance jobs such as decay and archival
- external scheduled jobs such as orchestrator triggers

## Service Layer Changes

### 1. Storage/repository changes

Add repository methods for:

- loading active facts in decay batches
- computing archive candidates
- archiving a fact transactionally
- listing archived facts
- retrieving archived facts by user and filters
- creating/updating/deleting scheduled jobs
- claiming due scheduled jobs
- recording scheduled job runs
- releasing or rescheduling failed jobs

### 2. Lifecycle APIs

Add internal service methods for:

- `RefreshFact(...)`
- `ArchiveFact(...)`
- `SupersedeFact(oldFactID, newFactID, reason)`
- `ListArchivedFacts(...)`

Refresh should only happen on reaffirmation or supported evidence, not on reads.

### 3. Retrieval behavior

Default fact retrieval should use only active facts.

Add optional archive-aware retrieval:

- `includeArchived=true`
- or separate archive endpoints if you want a stricter contract

Archived facts should never appear in normal retrieval unless explicitly requested.

## Contradiction And Supersession Flow

When a new fact conflicts with an old fact:

1. detect contradiction or supersession
2. create or update the new active fact
3. archive the old fact with:
   - `archive_reason = contradicted` or `superseded`
   - `superseded_by_fact_id = <new fact id>`
4. preserve provenance and version history

Contradiction and supersession should not leave the old fact in the active facts table.

## Scheduler Execution Model

### Phase 1

Implement a simple scheduler loop inside memory:

- poll `scheduled_jobs` for `status = active AND next_run_at <= now()`
- claim a job
- execute handler or BUS dispatch
- write a `scheduled_job_runs` row
- compute and persist the next run time

This keeps implementation small and supports both internal and external jobs quickly.

### Phase 2

Harden scheduler semantics:

- add advisory-lock or lease-based claiming
- add batch claiming
- add dead-run recovery
- add retry backoff rules

## Migrations

### Migration 1

- create `scheduler_schema`
- create `scheduled_jobs`
- create `scheduled_job_runs`

### Migration 2

- alter `personal_info_schema.user_facts`
- add decay metadata columns

### Migration 3

- create `personal_info_schema.user_facts_archive`

### Migration 4

- seed internal scheduled jobs:
  - periodic decay scan
  - periodic archive move if separated

## Recommended Delivery Order

### Phase 1: Schema and storage

1. add fact decay metadata columns
2. add archive table
3. add scheduler tables
4. add sqlc queries and repository methods

### Phase 2: Fact lifecycle logic

1. implement effective decay score calculation
2. implement refresh logic with diminishing returns
3. implement archive transition logic
4. implement contradiction and supersession archival flow

### Phase 3: Scheduler

1. implement due-job polling
2. implement internal job runner for decay and archive jobs
3. implement run history recording
4. implement retry and reschedule logic

### Phase 4: External scheduling support

1. implement external job payload schema
2. implement BUS dispatch for `target_kind = external`
3. add orchestrator-facing job examples

### Phase 5: API and query contract

1. default retrieval excludes archived facts
2. add archive retrieval support
3. add internal/admin APIs for schedules if needed
4. document refresh, supersession, and archival behavior

### Phase 6: Tests

1. migration tests
2. fact refresh tests
3. decay threshold tests
4. archive move tests
5. contradiction/supersession archive tests
6. scheduler claim/execute/reschedule tests
7. external dispatch tests

## Default Policy Values

Recommended starting profiles:

- durable: `decay_rate = 0.002`
- semi_durable: `decay_rate = 0.01`
- volatile: `decay_rate = 0.05`

Recommended thresholds:

- archive when `effective_decay_score <= 0.1`

Recommended scheduled jobs:

- `fact_decay_scan`: every 6 hours
- `fact_archive_move`: every 6 hours if separate, otherwise handled by scan job

## Open Questions

1. Should contradiction detection be explicit by the caller, or inferred by memory?
2. Should source references remain attached only to archived fact history, or also be copied into a separate archive references table?
3. Do we want archive retrieval in the public API, or internal/admin-only first?
4. Will memory itself evaluate schedules long-term, or is this an interim scheduler until orchestrator owns schedule evaluation?

## Definition Of Done

- active facts contain decay metadata
- archived facts live in a dedicated archive table
- no facts are hard-deleted during normal lifecycle transitions
- contradiction and supersession archive old facts instead of leaving them active
- decay and archival run only through scheduled jobs
- one scheduler model supports both internal and external jobs
- normal retrieval excludes archived facts
- archived facts remain queryable when explicitly requested
- scheduled job runs are auditable
- orchestrator-targeted scheduled jobs can be stored and dispatched through the BUS
