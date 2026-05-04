-- One-time migration for databases created before scheduling_schema was added to init-db.sql.
-- Safe to run multiple times (IF NOT EXISTS).

CREATE SCHEMA IF NOT EXISTS scheduling_schema;

CREATE TABLE IF NOT EXISTS scheduling_schema.scheduled_jobs (
    id UUID PRIMARY KEY,
    job_type VARCHAR(100) NOT NULL,
    target_kind VARCHAR(50) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    schedule_kind VARCHAR(50) NOT NULL,
    interval_seconds INT,
    name VARCHAR(255) NOT NULL,
    payload JSONB,
    user_id VARCHAR(64) NOT NULL DEFAULT '',
    time_zone VARCHAR(64) NOT NULL DEFAULT 'UTC',
    cron_expression TEXT NOT NULL DEFAULT '',
    next_run_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Upgrade existing DBs created before user_cron columns existed
ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS user_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS time_zone VARCHAR(64) NOT NULL DEFAULT 'UTC';
ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS cron_expression TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_next_run
    ON scheduling_schema.scheduled_jobs (next_run_at)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS scheduling_schema.scheduled_job_runs (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES scheduling_schema.scheduled_jobs(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    detail JSONB,
    trace_id UUID,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_job_id ON scheduling_schema.scheduled_job_runs(job_id);
CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_started_at ON scheduling_schema.scheduled_job_runs(started_at DESC);
