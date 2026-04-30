-- Enable pgvector extension
CREATE EXTENSION IF NOT EXISTS vector;

-- Create schemas
CREATE SCHEMA IF NOT EXISTS identity_schema;
CREATE SCHEMA IF NOT EXISTS chat_schema;
CREATE SCHEMA IF NOT EXISTS personal_info_schema;
CREATE SCHEMA IF NOT EXISTS agent_logs_schema;
CREATE SCHEMA IF NOT EXISTS service_log_schema;
CREATE SCHEMA IF NOT EXISTS scheduler_schema;
CREATE SCHEMA IF NOT EXISTS orchestrator_schema;

-- ==========================================
-- identity_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS identity_schema.users (
    id UUID PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

INSERT INTO identity_schema.users (id, email)
VALUES ('00000000-0000-0000-0000-000000000001', 'dev-default@example.com')
ON CONFLICT (id) DO NOTHING;

-- ==========================================
-- chat_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS chat_schema.conversations (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT 'New Conversation',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_conversations_user_id ON chat_schema.conversations(user_id);
CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON chat_schema.conversations(updated_at DESC);

CREATE TABLE IF NOT EXISTS chat_schema.tasks (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES chat_schema.conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    orchestrator_task_ref TEXT,
    trace_id VARCHAR(64),
    status VARCHAR(50) NOT NULL DEFAULT 'awaiting_feedback',
    input_summary TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_chat_tasks_conversation_id ON chat_schema.tasks(conversation_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_chat_tasks_user_id ON chat_schema.tasks(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_chat_tasks_orchestrator_task_ref ON chat_schema.tasks(orchestrator_task_ref);

CREATE TABLE IF NOT EXISTS chat_schema.messages (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL,
    user_id UUID NOT NULL,
    role VARCHAR(50) NOT NULL,
    content TEXT NOT NULL,
    token_count INT,
    idempotency_key UUID,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON chat_schema.messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_user_id ON chat_schema.messages(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_conversation_idempotency
    ON chat_schema.messages(conversation_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- ==========================================
-- personal_info_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS personal_info_schema.personal_info_chunks (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    raw_text TEXT NOT NULL,
    embedding VECTOR(1536),
    model_version VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_personal_info_chunks_user_id ON personal_info_schema.personal_info_chunks(user_id);
CREATE INDEX IF NOT EXISTS idx_personal_info_chunks_embedding ON personal_info_schema.personal_info_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE IF NOT EXISTS personal_info_schema.user_facts (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    category VARCHAR(50),
    fact_key VARCHAR(100) NOT NULL,
    fact_value JSONB NOT NULL,
    confidence FLOAT CHECK (confidence >= 0.0 AND confidence <= 1.0),
    version INT DEFAULT 1,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_facts_user_id ON personal_info_schema.user_facts(user_id);
CREATE INDEX IF NOT EXISTS idx_user_facts_category ON personal_info_schema.user_facts(category);

CREATE TABLE IF NOT EXISTS personal_info_schema.user_facts_archive (
    archive_id UUID PRIMARY KEY,
    fact_id UUID NOT NULL,
    user_id UUID NOT NULL,
    category VARCHAR(50),
    fact_key VARCHAR(100) NOT NULL,
    fact_value JSONB NOT NULL,
    confidence FLOAT,
    version INT,
    archive_reason VARCHAR(50) NOT NULL,
    superseded_by_fact_id UUID,
    archived_at TIMESTAMPTZ DEFAULT NOW(),
    original_updated_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_user_facts_archive_user_id ON personal_info_schema.user_facts_archive(user_id);
CREATE INDEX IF NOT EXISTS idx_user_facts_archive_fact_id ON personal_info_schema.user_facts_archive(fact_id);
CREATE INDEX IF NOT EXISTS idx_user_facts_archive_reason ON personal_info_schema.user_facts_archive(archive_reason);

CREATE TABLE IF NOT EXISTS personal_info_schema.source_references (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    target_id UUID NOT NULL,
    target_type VARCHAR(50) NOT NULL,
    source_id UUID NOT NULL,
    source_type VARCHAR(50) NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_source_references_user_id ON personal_info_schema.source_references(user_id);
CREATE INDEX IF NOT EXISTS idx_source_references_target_id ON personal_info_schema.source_references(target_id);
CREATE INDEX IF NOT EXISTS idx_source_references_source_id ON personal_info_schema.source_references(source_id);

-- ==========================================
-- agent_logs_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS agent_logs_schema.task_executions (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL,
    agent_id VARCHAR(100) NOT NULL,
    action_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) NOT NULL,
    error_context TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_executions_task_id ON agent_logs_schema.task_executions(task_id);
CREATE INDEX IF NOT EXISTS idx_task_executions_agent_id ON agent_logs_schema.task_executions(agent_id);

-- ==========================================
-- orchestrator_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS orchestrator_schema.orchestrator_records (
    id UUID PRIMARY KEY,
    orchestrator_task_ref TEXT NOT NULL,
    task_id TEXT NOT NULL,
    plan_id TEXT,
    subtask_id TEXT,
    trace_id VARCHAR(64),
    data_type TEXT NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    ttl_seconds INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_orch_records_task_id_type
    ON orchestrator_schema.orchestrator_records (task_id, data_type, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_orch_records_orch_ref_type
    ON orchestrator_schema.orchestrator_records (orchestrator_task_ref, data_type, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_orch_records_type_timestamp
    ON orchestrator_schema.orchestrator_records (data_type, timestamp DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_orch_records_task_state_upsert
    ON orchestrator_schema.orchestrator_records (task_id, data_type)
    WHERE data_type = 'task_state';
CREATE UNIQUE INDEX IF NOT EXISTS idx_orch_records_plan_state_upsert
    ON orchestrator_schema.orchestrator_records (task_id, plan_id, data_type)
    WHERE data_type = 'plan_state';
CREATE UNIQUE INDEX IF NOT EXISTS idx_orch_records_subtask_state_upsert
    ON orchestrator_schema.orchestrator_records (task_id, subtask_id, data_type)
    WHERE data_type = 'subtask_state';

CREATE OR REPLACE FUNCTION orchestrator_schema.reject_append_only_mutation()
RETURNS trigger AS $$
BEGIN
    IF OLD.data_type IN ('audit_log', 'recovery_event', 'policy_event') THEN
        RAISE EXCEPTION 'append-only orchestrator record type cannot be mutated: %', OLD.data_type;
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_orch_records_no_update_append_only ON orchestrator_schema.orchestrator_records;
CREATE TRIGGER trg_orch_records_no_update_append_only
    BEFORE UPDATE ON orchestrator_schema.orchestrator_records
    FOR EACH ROW
    EXECUTE FUNCTION orchestrator_schema.reject_append_only_mutation();

DROP TRIGGER IF EXISTS trg_orch_records_no_delete_append_only ON orchestrator_schema.orchestrator_records;
CREATE TRIGGER trg_orch_records_no_delete_append_only
    BEFORE DELETE ON orchestrator_schema.orchestrator_records
    FOR EACH ROW
    EXECUTE FUNCTION orchestrator_schema.reject_append_only_mutation();

-- ==========================================
-- service_log_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS service_log_schema.system_events (
    id UUID PRIMARY KEY,
    trace_id UUID,
    service_name VARCHAR(100),
    severity VARCHAR(20),
    message TEXT NOT NULL,
    metadata JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_system_events_trace_id ON service_log_schema.system_events(trace_id);
CREATE INDEX IF NOT EXISTS idx_system_events_service_name ON service_log_schema.system_events(service_name);
CREATE INDEX IF NOT EXISTS idx_system_events_severity ON service_log_schema.system_events(severity);

-- ==========================================
-- scheduler_schema
-- ==========================================
CREATE TABLE IF NOT EXISTS scheduler_schema.scheduled_jobs (
    id UUID PRIMARY KEY,
    job_type VARCHAR(100) NOT NULL,
    target_kind VARCHAR(20) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    status VARCHAR(20) NOT NULL,
    schedule_kind VARCHAR(20) NOT NULL,
    interval_seconds INT,
    name VARCHAR(255) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS scheduler_schema.scheduled_job_runs (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES scheduler_schema.scheduled_jobs(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    result JSONB,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_status_next_run_at ON scheduler_schema.scheduled_jobs(status, next_run_at);
CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_job_id_started_at ON scheduler_schema.scheduled_job_runs(job_id, started_at DESC);

-- ==========================================
-- vault_schema
-- ==========================================
CREATE SCHEMA IF NOT EXISTS vault_schema;

CREATE TABLE IF NOT EXISTS vault_schema.secrets (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    key_name VARCHAR(255) NOT NULL,
    encrypted_value BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, key_name)
);

CREATE INDEX IF NOT EXISTS idx_secrets_user_id ON vault_schema.secrets(user_id);

-- ==========================================
-- scheduling_schema — scheduled jobs / cron API
-- ==========================================
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
