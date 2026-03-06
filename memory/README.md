# Memory Service API Specification

Version: v1  
Implementation Language: Go  
Database: PostgreSQL with pgvector

## 1. Architectural Overview

The Memory Service acts as the central nervous system for the AI OS. It follows a logically distributed architecture on a single PostgreSQL instance (Distributed in the future). Externally, it exposes intuitive, domain-specific, and versioned REST/RPC endpoints. Internally, it routes these requests to isolated PostgreSQL schemas, ensuring strict service boundaries, security, and optimized querying.

All endpoints are versioned under `/api/v1/`.

All requests and responses use JSON.

Timestamps use RFC3339 format (example: `2026-03-04T12:34:56Z`).

All IDs use UUIDv7 for decentralized generation and time-ordered clustering.

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

### A. Health Store

**Purpose:** To provide service health information for monitoring and orchestration systems.

**Endpoints:**

- `GET /api/v1/healthz`: Returns service health state and database connectivity.

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

**Purpose:** To maintain the exact, chronological transcript of conversations between the user and the AI.

**Endpoints:**

- `POST /api/v1/chat/{sessionId}/save`: Appends a new message (user or AI) to the transcript for a specific session.
- `GET /api/v1/chat/{sessionId}/history`: Retrieves the conversation history, returning an ordered array of message objects.

**Why it is needed:** LLMs need immediate conversational context to reply coherently. This store provides the literal short-term memory of exactly what was just said without any summarization or loss of detail.

#### `POST /api/v1/chat/{sessionId}/save`

**Request arguments:**

- `sessionId` — UUID of the conversation session from the path
- `userId` — UUID of the user who owns the conversation
- `role` — `user | assistant | system`
- `content` — the exact message text
- `tokenCount` — optional integer token count for metrics and billing
- `idempotencyKey` — UUID used to prevent duplicate insertion on retry

**Return values:**

- `messageId` — UUID of stored message
- `createdAt` — insertion timestamp

**Dataflow:**

1. Request arrives with `sessionId` and message payload.
2. Service validates the path variable and payload fields.
3. Service verifies that the referenced user exists.
4. Service checks the `idempotencyKey` to prevent duplicate insertion.
5. Message is inserted into `chat_schema.messages`.
6. Database returns the generated `messageId` and `createdAt`.
7. Standardized response envelope is returned.

#### `GET /api/v1/chat/{sessionId}/history`

**Request arguments:**

- `sessionId` — UUID of the conversation session from the path
- `limit` — optional integer maximum number of messages to return

**Return values:**

- `messages` — ordered array of chat message objects

Each message contains:

- `messageId`
- `userId`
- `role`
- `content`
- `tokenCount`
- `createdAt`

**Dataflow:**

1. Service receives the session history request.
2. Service queries `chat_schema.messages` where `session_id` matches the path variable.
3. Results are ordered by `created_at` ascending.
4. If `limit` is present, the result set is truncated.
5. The messages array is returned in the standard response envelope.

### C. Personal Info Store

**Target Schema:** `personal_info_schema` (utilizing the `pgvector` extension)

**Purpose:** The core long-term intelligent memory. This is where the system extracts facts, stores semantic chunks, and enables semantic search across the user's history.

**Endpoints:**

- `POST /api/v1/personal_info/{userId}/save`: Accepts raw text and a source reference. Internally, the Go service chunks the text, calls an embedding model, extracts hard facts, and saves the data linked to the source.
- `POST /api/v1/personal_info/{userId}/query`: Takes a user query, vectorizes it, and performs a rapid cosine-similarity search against the pgvector store to return highly relevant context for the AI agent.
- `GET /api/v1/personal_info/{userId}/all`: Returns a complete, structured JSON dump of all facts and semantic memory chunks for a specific user.
- `PUT /api/v1/personal_info/{userId}/facts/{factId}`: Updates an existing structured fact for the user.
- `DELETE /api/v1/personal_info/{userId}/facts/{factId}`: Deletes an existing structured fact for the user.

**Why it is needed:** This provides the semantic understanding of the user over months or years, ensuring the AI OS remembers user preferences, ongoing projects, and established facts.

#### `POST /api/v1/personal_info/{userId}/save`

**Request arguments:**

- `userId` — UUID of the user from the path
- `content` — raw text content to store
- `sourceType` — `chat | uploaded_file | document | web`
- `sourceId` — UUID or external identifier of the original source
- `extractFacts` — boolean indicating whether structured facts should be extracted
- `categoryHint` — optional string category hint for fact extraction

**Return values:**

- `chunkIds` — array of UUIDs for inserted semantic chunks
- `factIds` — array of UUIDs for inserted structured facts

**Dataflow:**

1. Service validates `userId` and confirms the user exists.
2. Raw `content` is accepted by the save handler.
3. The content is chunked into smaller semantic units.
4. Each chunk is sent to the embedding model.
5. Embedding vectors are returned and written into `personal_info_schema.personal_info_chunks` with the chunk text.
6. If `extractFacts` is true, the service runs the fact extraction pipeline over the same content.
7. Extracted facts are written into `personal_info_schema.user_facts`.
8. `source_references` rows are inserted linking each chunk and fact back to the originating source.
9. The service returns the created `chunkIds` and `factIds` in the standardized response envelope.

#### `POST /api/v1/personal_info/{userId}/query`

**Request arguments:**

- `userId` — UUID of the user from the path
- `query` — natural language query string
- `topK` — maximum number of results to return

**Return values:**

- `results` — ordered list of semantic memory matches

Each result contains:

- `chunkId`
- `text`
- `similarityScore`
- `sourceReference`

Each `sourceReference` contains:

- `sourceType`
- `sourceId`
- `targetType`
- `targetId`

**Dataflow:**

1. Service validates the user exists.
2. The query string is converted into an embedding vector.
3. A pgvector cosine-similarity search is executed against `personal_info_schema.personal_info_chunks.embedding`.
4. The top `K` matches are selected.
5. Related `source_references` rows are joined in.
6. Results are ordered by similarity score descending.
7. Standardized response envelope is returned.

#### `GET /api/v1/personal_info/{userId}/all`

**Request arguments:**

- `userId` — UUID of the user from the path

**Return values:**

- `facts` — array of structured fact objects
- `chunks` — array of semantic chunk objects

Each fact contains:

- `factId`
- `category`
- `factKey`
- `factValue`
- `confidence`
- `version`
- `updatedAt`

Each chunk contains:

- `chunkId`
- `rawText`
- `modelVersion`
- `createdAt`

**Dataflow:**

1. Service validates the user exists.
2. Service queries all rows in `personal_info_schema.user_facts` for the user.
3. Service queries all rows in `personal_info_schema.personal_info_chunks` for the user.
4. The two datasets are packaged into a single response.
5. Standardized response envelope is returned.

#### `PUT /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**

- `userId` — UUID of the user from the path
- `factId` — UUID of the fact from the path
- `category` — updated category string
- `factKey` — updated key name
- `factValue` — updated JSON value
- `confidence` — updated confidence score
- `version` — current fact version for optimistic concurrency control

**Return values:**

- `factId` — UUID of updated fact
- `version` — incremented version after update
- `updatedAt` — update timestamp

**Dataflow:**

1. Service validates that the fact exists and belongs to the specified user.
2. Service compares the submitted `version` against the current row version.
3. If versions do not match, the service returns a `conflict` error.
4. If versions match, the fact row is updated.
5. The row version is incremented.
6. `updated_at` is refreshed.
7. Standardized response envelope is returned.

#### `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**

- `userId` — UUID of the user from the path
- `factId` — UUID of the fact from the path

**Return values:**

- `deleted` — boolean deletion confirmation
- `factId` — UUID of deleted fact

**Dataflow:**

1. Service validates that the fact exists and belongs to the specified user.
2. The target row is deleted from `personal_info_schema.user_facts`.
3. Any `source_references` rows targeting that fact are deleted as cleanup.
4. Standardized response envelope is returned.

### D. Agent Log Store

**Target Schema:** `agent_logs_schema`

**Purpose:** To track the autonomous actions, tool executions, and reasoning paths of the internal AI agents.

**Endpoints:**

- `POST /api/v1/agent/{taskId}/log/save`: Records an agent's current task, the tools it attempted to use, and the success or failure state of that action.
- `GET /api/v1/agent/{taskId}/log/history`: Retrieves the chronological history of all agent actions for a specific task.

**Why it is needed:** Auditing and safety. If an agent executes an unexpected command or fails a task, developers need a precise audit trail of the agent's decision-making process.

#### `POST /api/v1/agent/{taskId}/log/save`

**Request arguments:**

- `taskId` — UUID of the task from the path
- `agentId` — identifier of the specific agent performing the step
- `actionType` — `tool_call | reasoning_step | final_answer`
- `payload` — exact machine-readable input/output of the step
- `status` — `pending | success | failed`
- `errorContext` — optional text describing the failure

**Return values:**

- `logId` — UUID of inserted task execution row
- `createdAt` — insertion timestamp

**Dataflow:**

1. Agent emits execution details to the log endpoint.
2. Service validates the payload and required identifiers.
3. Entry is written to `agent_logs_schema.task_executions`.
4. Standardized response envelope is returned.

#### `GET /api/v1/agent/{taskId}/log/history`

**Request arguments:**

- `taskId` — UUID of the task from the path

**Return values:**

- `logs` — ordered array of task execution entries

Each log entry contains:

- `logId`
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
3. Standardized response envelope is returned.

### E. Service Log Store

**Target Schema:** `service_log_schema`

**Purpose:** System-level telemetry and infrastructure visibility.

**Endpoints:**

- `POST /api/v1/system/log/save`: Ingests system-level errors, bottlenecks, and database connection issues.
- `GET /api/v1/system/log/history`: Retrieves system health metrics and error logs.

**Why it is needed:** While Agent Logs track AI behavior, Service Logs track software behavior. This is essential for identifying memory leaks, latency spikes, and integration failures.

#### `POST /api/v1/system/log/save`

**Request arguments:**

- `traceId` — optional UUID used to correlate distributed requests
- `serviceName` — name of the emitting service
- `severity` — `INFO | WARN | ERROR | FATAL`
- `message` — human-readable log message
- `metadata` — structured machine-readable context

**Return values:**

- `logId` — UUID of inserted system event
- `createdAt` — insertion timestamp

**Dataflow:**

1. Internal service emits telemetry to the endpoint.
2. Service validates payload fields.
3. Entry is written to `service_log_schema.system_events`.
4. Standardized response envelope is returned.

#### `GET /api/v1/system/log/history`

**Request arguments:**

- `limit` — optional integer maximum number of events to return

**Return values:**

- `logs` — ordered array of system event entries

Each log entry contains:

- `logId`
- `traceId`
- `serviceName`
- `severity`
- `message`
- `metadata`
- `createdAt`

**Dataflow:**

1. Service queries `service_log_schema.system_events`.
2. Results are ordered by `created_at` descending.
3. If `limit` is present, the result set is truncated.
4. Standardized response envelope is returned.

## 4. Database Schema (Logical Distribution & Data Modeling)

To satisfy the requirements of a highly concurrent, distributed AI operating system, the database is physically modeled to support zero-downtime migrations, optimistic concurrency control, and logical sharding. All primary keys utilize UUIDv7 for decentralized generation and time-ordered clustering.

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
