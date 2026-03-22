-- Enable pgvector extension
CREATE EXTENSION IF NOT EXISTS vector;

-- Create schemas
CREATE SCHEMA IF NOT EXISTS identity_schema;
CREATE SCHEMA IF NOT EXISTS chat_schema;
CREATE SCHEMA IF NOT EXISTS personal_info_schema;
CREATE SCHEMA IF NOT EXISTS agent_logs_schema;
CREATE SCHEMA IF NOT EXISTS service_log_schema;

-- ==========================================
-- identity_schema
-- ==========================================
CREATE TABLE identity_schema.users (
    id UUID PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ==========================================
-- chat_schema
-- ==========================================
CREATE TABLE chat_schema.messages (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL,
    user_id UUID NOT NULL,
    role VARCHAR(50) NOT NULL,
    content TEXT NOT NULL,
    token_count INT,
    idempotency_key UUID UNIQUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_messages_session_id ON chat_schema.messages(session_id);
CREATE INDEX idx_messages_user_id ON chat_schema.messages(user_id);

-- ==========================================
-- personal_info_schema
-- ==========================================
CREATE TABLE personal_info_schema.personal_info_chunks (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    raw_text TEXT NOT NULL,
    embedding VECTOR(1536),
    model_version VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_personal_info_chunks_user_id ON personal_info_schema.personal_info_chunks(user_id);
CREATE INDEX idx_personal_info_chunks_embedding ON personal_info_schema.personal_info_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE personal_info_schema.user_facts (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    category VARCHAR(50),
    fact_key VARCHAR(100) NOT NULL,
    fact_value JSONB NOT NULL,
    confidence FLOAT CHECK (confidence >= 0.0 AND confidence <= 1.0),
    version INT DEFAULT 1,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_user_facts_user_id ON personal_info_schema.user_facts(user_id);
CREATE INDEX idx_user_facts_category ON personal_info_schema.user_facts(category);

CREATE TABLE personal_info_schema.source_references (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    target_id UUID NOT NULL,
    target_type VARCHAR(50) NOT NULL,
    source_id UUID NOT NULL,
    source_type VARCHAR(50) NOT NULL
);

CREATE INDEX idx_source_references_user_id ON personal_info_schema.source_references(user_id);
CREATE INDEX idx_source_references_target_id ON personal_info_schema.source_references(target_id);
CREATE INDEX idx_source_references_source_id ON personal_info_schema.source_references(source_id);

-- ==========================================
-- agent_logs_schema
-- ==========================================
CREATE TABLE agent_logs_schema.task_executions (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL,
    agent_id VARCHAR(100) NOT NULL,
    action_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(20) NOT NULL,
    error_context TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_task_executions_task_id ON agent_logs_schema.task_executions(task_id);
CREATE INDEX idx_task_executions_agent_id ON agent_logs_schema.task_executions(agent_id);

-- ==========================================
-- service_log_schema
-- ==========================================
CREATE TABLE service_log_schema.system_events (
    id UUID PRIMARY KEY,
    trace_id UUID,
    service_name VARCHAR(100),
    severity VARCHAR(20),
    message TEXT NOT NULL,
    metadata JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_system_events_trace_id ON service_log_schema.system_events(trace_id);
CREATE INDEX idx_system_events_service_name ON service_log_schema.system_events(service_name);
CREATE INDEX idx_system_events_severity ON service_log_schema.system_events(severity);

-- ==========================================
-- vault_schema
-- ==========================================
CREATE SCHEMA IF NOT EXISTS vault_schema;

CREATE TABLE vault_schema.secrets (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    key_name VARCHAR(255) NOT NULL,
    encrypted_value BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, key_name)
);

CREATE INDEX idx_secrets_user_id ON vault_schema.secrets(user_id);
