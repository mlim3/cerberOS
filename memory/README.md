# Memory Service API Specification

Version: v1  
Implementation Language: Go  
Database: PostgreSQL with pgvector

## 1. Architectural Overview

The Memory Service acts as the central storage layer for the AI OS. It follows a logically distributed architecture on a single PostgreSQL instance, with room to distribute later if needed. Externally, it exposes simple, versioned REST endpoints. Internally, it routes requests to isolated PostgreSQL schemas, keeping clear service boundaries while staying closely aligned to the underlying tables.

All endpoints are versioned under `/api/v1/`.

All requests and responses use JSON.

Timestamps use RFC3339 format (example: `2026-03-04T12:34:56Z`).

All IDs use UUIDv7 for decentralized generation and time-ordered clustering.

This service intentionally stays thin. It does not try to own complex data transformation logic. Callers are responsible for tasks like chunking, embedding generation, fact extraction, and payload shaping before writing data into the service. The service is primarily responsible for validation, persistence, retrieval, and basic concurrency checks.

## 2. Standard API Response Format

All endpoints return a standardized response envelope.

### Successful response

```json
{
  "ok": true,
  "data": {...},
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
    "message": "Human readable error",
    "details": {...}
  }
}
```

## 3. The API Contract & Service Logic (v1)

We use path variables like `{sessionId}`, `{userId}`, `{taskId}`, and `{factId}` to clearly identify the resource being modified.

The API is intentionally simple and closely follows the database schemas. In general, the service accepts already-shaped data, writes it to the corresponding schema tables, and returns the inserted or queried records. Service users are responsible for higher-level transformations such as text chunking, embedding generation, semantic extraction, and deciding when to overwrite facts.

### A. Health Endpoint

**Purpose:** To provide service health information for monitoring and orchestration systems.

**Endpoint:**

- `GET /api/v1/healthz`

**Arguments:**

- None

**Return values:**

- `status` — `healthy` or `degraded`
- `database` — `connected` or `disconnected`
- `timestamp` — current server time

**Dataflow:**

1. The request enters the HTTP handler.
2. The handler performs a lightweight database ping.
3. If the ping succeeds, the service returns `healthy`.
4. The standardized response envelope is returned to the caller.

### B. Chat Store

**Target Schema:** `chat_schema`

**Purpose:** To store the exact chronological transcript of conversations between the user and the AI.

**Endpoints:**

- `POST /api/v1/chat/{sessionId}/messages`
- `GET /api/v1/chat/{sessionId}/messages`

**Why it is needed:** This store provides exact short-term conversational memory with no summarization or transformation.

#### `POST /api/v1/chat/{sessionId}/messages`

**Request arguments:**

- `sessionId` — UUID of the conversation session from the path
- `userId` — UUID of the user who owns the conversation
- `role` — `user | assistant | system`
- `content` — exact message text
- `tokenCount` — optional integer token count
- `idempotencyKey` — UUID used to prevent duplicate insertion on retry

**Return values:**

- `messageId`
- `sessionId`
- `userId`
- `role`
- `content`
- `tokenCount`
- `idempotencyKey`
- `createdAt`

**Dataflow:**

1. Request arrives with `sessionId` and message payload.
2. Service validates required fields.
3. Service checks `idempotencyKey` uniqueness.
4. Service inserts the row into `chat_schema.messages`.
5. The inserted record is returned in the standardized response envelope.

#### `GET /api/v1/chat/{sessionId}/messages`

**Request arguments:**

- `sessionId` — UUID of the conversation session from the path
- `limit` — optional integer maximum number of messages to return

**Return values:**

- `messages` — ordered array of message records

Each message contains:

- `messageId`
- `sessionId`
- `userId`
- `role`
- `content`
- `tokenCount`
- `idempotencyKey`
- `createdAt`

**Dataflow:**

1. Service queries `chat_schema.messages` where `session_id` matches the path variable.
2. Results are ordered by `created_at` ascending.
3. If `limit` is provided, the result set is truncated.
4. The records are returned in the standardized response envelope.

### C. Personal Info Store

**Target Schema:** `personal_info_schema`

**Purpose:** To store semantic chunks, structured facts, and traceability links for long-term memory.

**Endpoints:**

- `POST /api/v1/personal_info/{userId}/chunks`
- `GET /api/v1/personal_info/{userId}/chunks`
- `POST /api/v1/personal_info/{userId}/facts`
- `GET /api/v1/personal_info/{userId}/facts`
- `PUT /api/v1/personal_info/{userId}/facts/{factId}`
- `DELETE /api/v1/personal_info/{userId}/facts/{factId}`
- `POST /api/v1/personal_info/{userId}/sources`
- `GET /api/v1/personal_info/{userId}/sources`

**Why it is needed:** This schema stores the long-term memory records used by other services. The memory service itself remains a persistence and retrieval layer, while callers handle transformation and extraction before writing data.

#### `POST /api/v1/personal_info/{userId}/chunks`

**Request arguments:**

- `userId` — UUID of the user from the path
- `rawText` — chunk text to store
- `embedding` — vector payload matching the configured pgvector dimension
- `modelVersion` — embedding model version string

**Return values:**

- `chunkId`
- `userId`
- `rawText`
- `modelVersion`
- `createdAt`

**Dataflow:**

1. Service validates the request payload.
2. Service inserts the row into `personal_info_schema.personal_info_chunks`.
3. The inserted chunk record is returned.

#### `GET /api/v1/personal_info/{userId}/chunks`

**Request arguments:**

- `userId` — UUID of the user from the path
- `limit` — optional integer maximum number of rows

**Return values:**

- `chunks` — array of chunk records

Each chunk contains:

- `chunkId`
- `userId`
- `rawText`
- `modelVersion`
- `createdAt`

**Dataflow:**

1. Service queries `personal_info_schema.personal_info_chunks` by `user_id`.
2. If `limit` is provided, the result set is truncated.
3. The records are returned in the standardized response envelope.

#### `POST /api/v1/personal_info/{userId}/facts`

**Request arguments:**

- `userId` — UUID of the user from the path
- `category` — fact category string
- `factKey` — key name
- `factValue` — JSON value
- `confidence` — confidence score

**Return values:**

- `factId`
- `userId`
- `category`
- `factKey`
- `factValue`
- `confidence`
- `version`
- `updatedAt`
- `createdAt`

**Dataflow:**

1. Service validates the request payload.
2. Service inserts the row into `personal_info_schema.user_facts`.
3. The inserted fact record is returned.

#### `GET /api/v1/personal_info/{userId}/facts`

**Request arguments:**

- `userId` — UUID of the user from the path
- `category` — optional category filter
- `limit` — optional integer maximum number of rows

**Return values:**

- `facts` — array of fact records

Each fact contains:

- `factId`
- `userId`
- `category`
- `factKey`
- `factValue`
- `confidence`
- `version`
- `updatedAt`
- `createdAt`

**Dataflow:**

1. Service queries `personal_info_schema.user_facts` by `user_id`.
2. If `category` is provided, the query adds a category filter.
3. If `limit` is provided, the result set is truncated.
4. The records are returned in the standardized response envelope.

#### `PUT /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**

- `userId` — UUID of the user from the path
- `factId` — UUID of the fact from the path
- `category` — updated category string
- `factKey` — updated key name
- `factValue` — updated JSON value
- `confidence` — updated confidence score
- `version` — current version for optimistic concurrency control

**Return values:**

- `factId`
- `userId`
- `category`
- `factKey`
- `factValue`
- `confidence`
- `version`
- `updatedAt`
- `createdAt`

**Dataflow:**

1. Service loads the target fact by `factId` and `userId`.
2. Service compares the submitted `version` against the stored version.
3. If versions do not match, the service returns a `conflict` error.
4. If versions match, the row is updated and the version is incremented.
5. The updated fact record is returned.

#### `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**

- `userId` — UUID of the user from the path
- `factId` — UUID of the fact from the path

**Return values:**

- `deleted` — boolean deletion confirmation
- `factId` — UUID of deleted fact

**Dataflow:**

1. Service validates that the fact exists for the specified user.
2. The target row is deleted from `personal_info_schema.user_facts`.
3. Related `source_references` rows may be deleted as cleanup if configured by the service.
4. The deletion result is returned.

#### `POST /api/v1/personal_info/{userId}/sources`

**Request arguments:**

- `userId` — UUID of the user from the path
- `targetId` — UUID of the chunk or fact
- `targetType` — `chunk | fact`
- `sourceId` — UUID of the source record
- `sourceType` — `chat | uploaded_file | document | web`

**Return values:**

- `sourceReferenceId`
- `targetId`
- `targetType`
- `sourceId`
- `sourceType`
- `createdAt`

**Dataflow:**

1. Service validates the request payload.
2. Service inserts the row into `personal_info_schema.source_references`.
3. The inserted traceability record is returned.

#### `GET /api/v1/personal_info/{userId}/sources`

**Request arguments:**

- `userId` — UUID of the user from the path
- `targetId` — optional UUID of the chunk or fact
- `targetType` — optional `chunk | fact`
- `sourceType` — optional source type filter
- `limit` — optional integer maximum number of rows

**Return values:**

- `sources` — array of source reference records

Each source reference contains:

- `sourceReferenceId`
- `targetId`
- `targetType`
- `sourceId`
- `sourceType`
- `createdAt`

**Dataflow:**

1. Service queries `personal_info_schema.source_references` for records associated with the specified user context.
2. Optional filters are applied if provided.
3. If `limit` is provided, the result set is truncated.
4. The records are returned in the standardized response envelope.

### D. Agent Log Store

**Target Schema:** `agent_logs_schema`

**Purpose:** To track the autonomous actions, tool executions, and reasoning paths of the internal AI agents.

**Endpoints:**

- `POST /api/v1/agent/{taskId}/executions`
- `GET /api/v1/agent/{taskId}/executions`

**Why it is needed:** This provides an audit trail of agent behavior without adding business logic on top of the stored records.

#### `POST /api/v1/agent/{taskId}/executions`

**Request arguments:**

- `taskId` — UUID of the task from the path
- `agentId` — identifier of the specific agent performing the step
- `actionType` — action type string
- `payload` — machine-readable input/output of the step
- `status` — `pending | success | failed`
- `errorContext` — optional text describing the failure

**Return values:**

- `executionId`
- `taskId`
- `agentId`
- `actionType`
- `payload`
- `status`
- `errorContext`
- `createdAt`

**Dataflow:**

1. Service validates the request payload.
2. Service inserts the row into `agent_logs_schema.task_executions`.
3. The inserted execution record is returned.

#### `GET /api/v1/agent/{taskId}/executions`

**Request arguments:**

- `taskId` — UUID of the task from the path
- `limit` — optional integer maximum number of rows

**Return values:**

- `executions` — ordered array of execution records

Each execution contains:

- `executionId`
- `taskId`
- `agentId`
- `actionType`
- `payload`
- `status`
- `errorContext`
- `createdAt`

**Dataflow:**

1. Service queries `agent_logs_schema.task_executions` by `task_id`.
2. Results are ordered by `created_at` ascending.
3. If `limit` is provided, the result set is truncated.
4. The records are returned in the standardized response envelope.

### E. Service Log Store

**Target Schema:** `service_log_schema`

**Purpose:** To store system-level telemetry and infrastructure visibility records.

**Endpoints:**

- `POST /api/v1/system/events`
- `GET /api/v1/system/events`

**Why it is needed:** This provides software and infrastructure logs without mixing them into user memory or agent execution records.

#### `POST /api/v1/system/events`

**Request arguments:**

- `traceId` — optional UUID used to correlate distributed requests
- `serviceName` — name of the emitting service
- `severity` — `INFO | WARN | ERROR | FATAL`
- `message` — human-readable log message
- `metadata` — structured machine-readable context

**Return values:**

- `eventId`
- `traceId`
- `serviceName`
- `severity`
- `message`
- `metadata`
- `createdAt`

**Dataflow:**

1. Service validates the request payload.
2. Service inserts the row into `service_log_schema.system_events`.
3. The inserted system event record is returned.

#### `GET /api/v1/system/events`

**Request arguments:**

- `serviceName` — optional service name filter
- `severity` — optional severity filter
- `limit` — optional integer maximum number of rows

**Return values:**

- `events` — ordered array of system event records

Each event contains:

- `eventId`
- `traceId`
- `serviceName`
- `severity`
- `message`
- `metadata`
- `createdAt`

**Dataflow:**

1. Service queries `service_log_schema.system_events`.
2. Optional filters are applied if provided.
3. Results are ordered by `created_at` descending.
4. If `limit` is provided, the result set is truncated.
5. The records are returned in the standardized response envelope.

## 4. Database Schema (Logical Distribution & Data Modeling)

The database is modeled to stay simple, explicit, and closely aligned to the API. Each schema owns a clear type of record. Most endpoints map directly to rows in these tables, with only lightweight validation and concurrency checks in the service layer. All primary keys utilize UUIDv7 for decentralized generation and time-ordered clustering.

### Schema: identity_schema

#### Table: users

**Purpose:** Canonical user identity table used to partition all memory by user.

| Column Name | Data Type    | Constraints / Indexes  | Distributed Purpose / Description                |
| :---------- | :----------- | :--------------------- | :----------------------------------------------- |
| id          | UUID         | PRIMARY KEY            | Generated via UUIDv7. Canonical user identifier. |
| email       | VARCHAR(255) | UNIQUE INDEX, NOT NULL | Human identity and login correlation field.      |
| created_at  | TIMESTAMPTZ  | DEFAULT NOW()          | User creation timestamp.                         |
| updated_at  | TIMESTAMPTZ  | DEFAULT NOW()          | Last mutation timestamp for the user record.     |

### Schema: chat_schema

#### Table: messages

**Purpose:** Append-only immutable ledger of conversational history.

| Column Name     | Data Type   | Constraints / Indexes | Distributed Purpose / Description                                                    |
| :-------------- | :---------- | :-------------------- | :----------------------------------------------------------------------------------- |
| id              | UUID        | PRIMARY KEY           | Generated via UUIDv7.                                                                |
| session_id      | UUID        | INDEX, NOT NULL       | Logical sharding key. All messages for a session stay on the same shard.             |
| user_id         | UUID        | INDEX, NOT NULL       | Links the session to the canonical identity record.                                  |
| role            | VARCHAR(50) | NOT NULL              | `user`, `assistant`, or `system`.                                                    |
| content         | TEXT        | NOT NULL              | The exact text of the message.                                                       |
| token_count     | INT         |                       | Optional computed token count for rate-limiting and billing metrics.                 |
| idempotency_key | UUID        | UNIQUE INDEX          | Prevents duplicate insertion if the network drops a response and the client retries. |
| created_at      | TIMESTAMPTZ | DEFAULT NOW()         | Immutable timestamp of the event.                                                    |

### Schema: personal_info_schema

This schema is designed for concurrent reads and writes by multiple background agents extracting facts simultaneously.

#### Table: personal_info_chunks

**Purpose:** Vector storage for semantic RAG (Retrieval-Augmented Generation).

| Column Name   | Data Type    | Constraints / Indexes | Distributed Purpose / Description                                                      |
| :------------ | :----------- | :-------------------- | :------------------------------------------------------------------------------------- |
| id            | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                                                  |
| user_id       | UUID         | INDEX, NOT NULL       | Logical sharding key.                                                                  |
| raw_text      | TEXT         | NOT NULL              | Text chunk to be retrieved during semantic search.                                     |
| embedding     | VECTOR(1536) | HNSW INDEX            | pgvector column indexed using HNSW for high-speed approximate nearest neighbor search. |
| model_version | VARCHAR(50)  | NOT NULL              | Tracks which embedding model created the vector.                                       |
| created_at    | TIMESTAMPTZ  | DEFAULT NOW()         | Chunk creation timestamp.                                                              |

#### Table: user_facts

**Purpose:** Hard structured facts used for definitive rules and exact lookups.

| Column Name | Data Type    | Constraints / Indexes                           | Distributed Purpose / Description                                                         |
| :---------- | :----------- | :---------------------------------------------- | :---------------------------------------------------------------------------------------- |
| id          | UUID         | PRIMARY KEY                                     | Generated via UUIDv7.                                                                     |
| user_id     | UUID         | INDEX, NOT NULL                                 | Logical sharding key.                                                                     |
| category    | VARCHAR(50)  | INDEX                                           | `Diet`, `Code_Preference`, `Relationships`, and similar categories.                       |
| fact_key    | VARCHAR(100) | NOT NULL                                        | Example: `allergy`.                                                                       |
| fact_value  | JSONB        | NOT NULL                                        | Flexible structured value without rigid migration requirements.                           |
| confidence  | FLOAT        | CHECK (confidence >= 0.0 AND confidence <= 1.0) | AI certainty score for the extracted or maintained fact.                                  |
| version     | INT          | DEFAULT 1                                       | Optimistic concurrency control field. Prevents race conditions during concurrent updates. |
| updated_at  | TIMESTAMPTZ  | DEFAULT NOW()                                   | Last update timestamp.                                                                    |
| created_at  | TIMESTAMPTZ  | DEFAULT NOW()                                   | Initial insertion timestamp.                                                              |

#### Table: source_references

**Purpose:** Many-to-many traceability mapping between stored memory and original source material.

| Column Name | Data Type   | Constraints / Indexes | Distributed Purpose / Description                                       |
| :---------- | :---------- | :-------------------- | :---------------------------------------------------------------------- |
| id          | UUID        | PRIMARY KEY           | Generated via UUIDv7.                                                   |
| target_id   | UUID        | INDEX, NOT NULL       | ID of the chunk or fact being referenced.                               |
| target_type | VARCHAR(50) | NOT NULL              | `chunk` or `fact`.                                                      |
| source_id   | UUID        | INDEX, NOT NULL       | ID of the chat message, uploaded file, or external source object.       |
| source_type | VARCHAR(50) | NOT NULL              | `chat`, `uploaded_file`, `document`, `web`, and similar source classes. |
| created_at  | TIMESTAMPTZ | DEFAULT NOW()         | Traceability link creation timestamp.                                   |

### Schema: agent_logs_schema

#### Table: task_executions

**Purpose:** Event-sourced ledger of multi-agent collaboration.

| Column Name   | Data Type    | Constraints / Indexes | Distributed Purpose / Description                            |
| :------------ | :----------- | :-------------------- | :----------------------------------------------------------- |
| id            | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                        |
| task_id       | UUID         | INDEX, NOT NULL       | Correlation ID grouping all agent actions for the same task. |
| agent_id      | VARCHAR(100) | INDEX, NOT NULL       | Specific agent executing the step.                           |
| action_type   | VARCHAR(50)  | NOT NULL              | `tool_call`, `reasoning_step`, or `final_answer`.            |
| payload       | JSONB        | NOT NULL              | Exact input/output of the tool call or reasoning step.       |
| status        | VARCHAR(20)  | NOT NULL              | `pending`, `success`, or `failed`.                           |
| error_context | TEXT         |                       | Stack trace or model failure reason.                         |
| created_at    | TIMESTAMPTZ  | DEFAULT NOW()         | Event timestamp.                                             |

### Schema: service_log_schema

#### Table: system_events

**Purpose:** Software health telemetry formatted for distributed tracing and infrastructure diagnostics.

| Column Name  | Data Type    | Constraints / Indexes | Distributed Purpose / Description                                                       |
| :----------- | :----------- | :-------------------- | :-------------------------------------------------------------------------------------- |
| id           | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                                                   |
| trace_id     | UUID         | INDEX                 | Distributed trace ID passed through service boundaries.                                 |
| service_name | VARCHAR(100) | INDEX                 | Emitting service name.                                                                  |
| severity     | VARCHAR(20)  | INDEX                 | `INFO`, `WARN`, `ERROR`, or `FATAL`.                                                    |
| message      | TEXT         | NOT NULL              | Human-readable log message.                                                             |
| metadata     | JSONB        |                       | Structured machine-readable context such as latency, IP, memory usage, or host details. |
| created_at   | TIMESTAMPTZ  | DEFAULT NOW()         | Time-series optimized event timestamp.                                                  |
