# Memory Service API Specification

Version: v1  
Implementation Language: Go  
Database: PostgreSQL with pgvector

## 1. Architectural Overview

The Memory Service acts as the central nervous system for the AI OS. It follows a logically distributed architecture on a single PostgreSQL instance (with strict sharding keys to allow physical distribution in the future). Externally, it exposes intuitive, domain-specific, and versioned REST endpoints. Internally, it routes these requests to isolated PostgreSQL schemas, ensuring strict service boundaries, security, and optimized querying.

All endpoints are versioned under `/api/v1/`.
All requests and responses use JSON.
Timestamps use RFC3339 format (example: `2026-03-04T12:34:56Z`).
All IDs use UUIDv7 for decentralized generation and time-ordered clustering.

**Design Philosophy (The Facade Pattern):** This service intentionally abstracts complexity away from the caller. For long-term memory, the caller simply passes raw text. The Memory Service itself handles text chunking, embedding generation, fact extraction, and traceability linking internally. This ensures the rest of the OS does not need to understand vector math or chunking strategies.

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

### A. Health Endpoint

**Purpose:** To provide service health information for monitoring and orchestration systems.

**Endpoint:** `GET /api/v1/healthz`

**Arguments:** None

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

**Why it is needed:** This store provides exact short-term conversational memory with no summarization or transformation.

#### `POST /api/v1/chat/{sessionId}/messages`

**Request arguments:**
- `sessionId` — UUID of the conversation session
- `userId` — UUID of the user who owns the conversation
- `role` — `user | assistant | system`
- `content` — exact message text
- `tokenCount` — optional integer token count
- `idempotencyKey` — UUID used to prevent duplicate insertion on retry

**Return values:**
- `messageId` — UUID of stored message
- `createdAt` — insertion timestamp

**Dataflow:**
1. Request arrives; service validates required fields.
2. Service checks `idempotencyKey` uniqueness to prevent retries from duplicating messages.
3. Service inserts the row into `chat_schema.messages`.
4. Returns the generated ID and timestamp.

#### `GET /api/v1/chat/{sessionId}/messages`

**Request arguments:**
- `sessionId` — UUID of the conversation session
- `limit` — optional integer maximum number of messages to return

**Return values:**
- `messages` — ordered array of message records. Each record contains:
  - `messageId`
  - `sessionId`
  - `userId`
  - `role`
  - `content`
  - `tokenCount`
  - `createdAt`

**Dataflow:**
1. Service queries `chat_schema.messages` where `session_id` matches the path variable.
2. Results are ordered by `created_at` ascending.
3. If `limit` is provided, the result set is truncated.

### C. Personal Info Store

**Target Schema:** `personal_info_schema` (utilizing `pgvector`)

**Purpose:** The core long-term intelligent memory. This is where the system handles semantic chunks, structured facts, and traceability links.

#### `POST /api/v1/personal_info/{userId}/save`

**Request arguments:**
- `userId` — UUID of the user
- `content` — raw text content to intelligently store
- `sourceType` — `chat | uploaded_file | document | web`
- `sourceId` — UUID of the original source
- `extractFacts` — boolean indicating whether to run the structured fact extraction pipeline

**Return values:**
- `chunkIds` — array of UUIDs for inserted semantic chunks
- `factIds` — array of UUIDs for inserted structured facts

**Dataflow:**
1. Raw `content` is accepted.
2. The Go service internally chunks the content into smaller semantic units.
3. The service calls an embedding model to vectorize each chunk.
4. Chunks and vectors are written into `personal_info_schema.personal_info_chunks`.
5. If `extractFacts` is true, the service runs the LLM fact extraction pipeline over the content.
6. Extracted facts are written into `personal_info_schema.user_facts`.
7. `source_references` rows are inserted linking the chunks/facts back to the `sourceId`.

#### `POST /api/v1/personal_info/{userId}/query`

**Request arguments:**
- `userId` — UUID of the user
- `query` — natural language query string
- `topK` — maximum number of vector results to return

**Return values:**
- `results` — ordered list of semantic memory matches. Each match contains:
  - `chunkId`
  - `text`
  - `similarityScore`
  - `sourceReference`

**Dataflow:**
1. The query string is converted into an embedding vector internally.
2. A pgvector cosine-similarity search (`<=>`) is executed against `personal_info_chunks` for this `userId`.
3. The top `K` matches are returned alongside their joined source references.

#### `GET /api/v1/personal_info/{userId}/all`

**Request arguments:**
- `userId` — UUID of the user from the path

**Return values:**
- `facts` — array of structured fact objects
- `chunks` — array of semantic chunk objects (excluding the raw vector data)

#### `PUT /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**
- `userId` — UUID of the user
- `factId` — UUID of the fact
- `category` — updated category string
- `factKey` — updated key name
- `factValue` — updated JSON value
- `confidence` — updated confidence score
- `version` — current version for optimistic concurrency control

**Return values:**
- `factId`
- `version` — incremented version
- `updatedAt`

#### `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**
- `userId` — UUID of the user
- `factId` — UUID of the fact

**Return values:**
- `deleted` — boolean
- `factId`

### D. Agent Log Store

**Target Schema:** `agent_logs_schema`

**Purpose:** To track the autonomous actions, tool executions, and reasoning paths of the internal AI agents.

#### `POST /api/v1/agent/{taskId}/executions`

**Request arguments:**
- `taskId` — UUID of the task
- `agentId` — identifier of the specific agent performing the step
- `actionType` — `tool_call | reasoning_step | final_answer`
- `payload` — machine-readable JSON input/output of the step
- `status` — `pending | success | failed`
- `errorContext` — optional text describing the failure

**Return values:**
- `executionId`
- `createdAt`

#### `GET /api/v1/agent/{taskId}/executions`

**Request arguments:**
- `taskId` — UUID of the task
- `limit` — optional integer maximum number of rows

**Return values:**
- `executions` — ordered array of execution records containing all payload data, ordered by `createdAt` ascending.

### E. Service Log Store

**Target Schema:** `service_log_schema`

**Purpose:** To store system-level telemetry and infrastructure visibility records.

#### `POST /api/v1/system/events`

**Request arguments:**
- `traceId` — optional UUID used to correlate distributed requests
- `serviceName` — name of the emitting service
- `severity` — `INFO | WARN | ERROR | FATAL`
- `message` — human-readable log message
- `metadata` — structured machine-readable context

**Return values:**
- `eventId`
- `createdAt`

#### `GET /api/v1/system/events`

**Request arguments:**
- `serviceName` — optional service name filter
- `severity` — optional severity filter
- `limit` — optional integer maximum number of rows

**Return values:**
- `events` — ordered array of system event records, ordered by `createdAt` descending.

## 4. Database Schema (Logical Distribution & Data Modeling)

All schemas reside in a logically distributed PostgreSQL instance. Primary keys utilize UUIDv7. Logical sharding keys (`user_id`, `session_id`) are strictly enforced on all tables to allow future physical distribution.

### Schema: identity_schema

| Column Name | Data Type    | Constraints / Indexes  | Distributed Purpose / Description                |
| :---------- | :----------- | :--------------------- | :----------------------------------------------- |
| id          | UUID         | PRIMARY KEY            | Generated via UUIDv7. Canonical user identifier. |
| email       | VARCHAR(255) | UNIQUE INDEX, NOT NULL | Human identity and login correlation field.      |
| created_at  | TIMESTAMPTZ  | DEFAULT NOW()          | User creation timestamp.                         |

### Schema: chat_schema (Table: messages)

| Column Name     | Data Type   | Constraints / Indexes | Distributed Purpose / Description                                                    |
| :-------------- | :---------- | :-------------------- | :----------------------------------------------------------------------------------- |
| id              | UUID        | PRIMARY KEY           | Generated via UUIDv7.                                                                |
| session_id      | UUID        | INDEX, NOT NULL       | **Logical sharding key**. All messages for a session stay on the same shard.         |
| user_id         | UUID        | INDEX, NOT NULL       | Links the session to the canonical identity record.                                  |
| role            | VARCHAR(50) | NOT NULL              | `user`, `assistant`, or `system`.                                                    |
| content         | TEXT        | NOT NULL              | The exact text of the message.                                                       |
| token_count     | INT         |                       | Optional computed token count.                                                       |
| idempotency_key | UUID        | UNIQUE INDEX          | Prevents duplicate insertion.                                                        |
| created_at      | TIMESTAMPTZ | DEFAULT NOW()         | Immutable timestamp of the event.                                                    |

### Schema: personal_info_schema 

**Table: personal_info_chunks**

| Column Name   | Data Type    | Constraints / Indexes | Distributed Purpose / Description                                                      |
| :------------ | :----------- | :-------------------- | :------------------------------------------------------------------------------------- |
| id            | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                                                  |
| user_id       | UUID         | INDEX, NOT NULL       | **Logical sharding key**.                                                              |
| raw_text      | TEXT         | NOT NULL              | Text chunk to be retrieved during semantic search.                                     |
| embedding     | VECTOR(1536) | HNSW INDEX            | pgvector column indexed using HNSW for high-speed approximate nearest neighbor search. |
| model_version | VARCHAR(50)  | NOT NULL              | Tracks which embedding model created the vector.                                       |
| created_at    | TIMESTAMPTZ  | DEFAULT NOW()         | Chunk creation timestamp.                                                              |

**Table: user_facts**

| Column Name | Data Type    | Constraints / Indexes                           | Distributed Purpose / Description                                                         |
| :---------- | :----------- | :---------------------------------------------- | :---------------------------------------------------------------------------------------- |
| id          | UUID         | PRIMARY KEY                                     | Generated via UUIDv7.                                                                     |
| user_id     | UUID         | INDEX, NOT NULL                                 | **Logical sharding key**.                                                                 |
| category    | VARCHAR(50)  | INDEX                                           | `Diet`, `Code_Preference`, `Relationships`, etc.                                          |
| fact_key    | VARCHAR(100) | NOT NULL                                        | Example: `allergy`.                                                                       |
| fact_value  | JSONB        | NOT NULL                                        | Flexible structured value.                                                                |
| confidence  | FLOAT        | CHECK (confidence >= 0.0 AND confidence <= 1.0) | AI certainty score.                                                                       |
| version     | INT          | DEFAULT 1                                       | **Optimistic concurrency control** field. Prevents race conditions during concurrent updates. |
| updated_at  | TIMESTAMPTZ  | DEFAULT NOW()                                   | Last update timestamp.                                                                    |

**Table: source_references**

| Column Name | Data Type   | Constraints / Indexes | Distributed Purpose / Description                                       |
| :---------- | :---------- | :-------------------- | :---------------------------------------------------------------------- |
| id          | UUID        | PRIMARY KEY           | Generated via UUIDv7.                                                   |
| user_id     | UUID        | INDEX, NOT NULL       | **Logical sharding key**. Ensures references reside on the user's shard.|
| target_id   | UUID        | INDEX, NOT NULL       | ID of the chunk or fact being referenced.                               |
| target_type | VARCHAR(50) | NOT NULL              | `chunk` or `fact`.                                                      |
| source_id   | UUID        | INDEX, NOT NULL       | ID of the chat message, uploaded file, document, etc.                   |
| source_type | VARCHAR(50) | NOT NULL              | `chat`, `uploaded_file`, `document`, `web`.                             |

### Schema: agent_logs_schema (Table: task_executions)

| Column Name   | Data Type    | Constraints / Indexes | Distributed Purpose / Description                            |
| :------------ | :----------- | :-------------------- | :----------------------------------------------------------- |
| id            | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                        |
| task_id       | UUID         | INDEX, NOT NULL       | **Correlation ID** grouping all agent actions for the same task. |
| agent_id      | VARCHAR(100) | INDEX, NOT NULL       | Specific agent executing the step.                           |
| action_type   | VARCHAR(50)  | NOT NULL              | `tool_call`, `reasoning_step`, or `final_answer`.            |
| payload       | JSONB        | NOT NULL              | Exact input/output of the tool call or reasoning step.       |
| status        | VARCHAR(20)  | NOT NULL              | `pending`, `success`, or `failed`.                           |
| error_context | TEXT         |                       | Stack trace or model failure reason.                         |
| created_at    | TIMESTAMPTZ  | DEFAULT NOW()         | Event timestamp.                                             |

### Schema: service_log_schema (Table: system_events)

| Column Name  | Data Type    | Constraints / Indexes | Distributed Purpose / Description                                                       |
| :----------- | :----------- | :-------------------- | :-------------------------------------------------------------------------------------- |
| id           | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                                                   |
| trace_id     | UUID         | INDEX                 | **Distributed trace ID** passed through service boundaries.                             |
| service_name | VARCHAR(100) | INDEX                 | Emitting service name.                                                                  |
| severity     | VARCHAR(20)  | INDEX                 | `INFO`, `WARN`, `ERROR`, or `FATAL`.                                                    |
| message      | TEXT         | NOT NULL              | Human-readable log message.                                                             |
| metadata     | JSONB        |                       | Structured machine-readable context (e.g., latency, memory usage).                      |
| created_at   | TIMESTAMPTZ  | DEFAULT NOW()         | Time-series optimized event timestamp.                                                  |