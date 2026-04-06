# Memory Service API Specification

Version: v1  
Implementation Language: Go  
Database: PostgreSQL with pgvector

## Swagger Generation

Swagger is generated from handler annotations with `swaggo/swag`.

From `/Users/colbydobson/cs/cerberOS/memory`, run:

```bash
go generate ./cmd/server
```

That regenerates:

- `docs/docs.go`
- `docs/swagger.json`
- `docs/swagger.yaml`

The server imports the generated docs package and serves Swagger UI at `http://localhost:8080/swagger/index.html`.

## 1. Architectural Overview

The Memory Service acts as the central memory layer for the AI OS. It follows a logically distributed architecture on a single PostgreSQL instance, with strict sharding keys that allow physical distribution in the future. Externally, it exposes domain-specific, versioned REST endpoints. Internally, it routes these requests to isolated PostgreSQL schemas, ensuring clear service boundaries, security, and optimized querying.

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

All user-scoped endpoints validate that the referenced `userId` exists in `identity_schema.users` before performing work. All resource lookups are constrained by `userId` ownership. If a requested resource does not exist for the specified user, the service returns `not_found` rather than exposing cross-user existence.

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

Chat messages are append-only and immutable through this API. Update and delete operations are intentionally not exposed.

#### `POST /api/v1/chat/{sessionId}/messages`

**Request arguments:**

- **Request JSON:**

```json
{
  "userId": "0194d7b4-9d31-7d31-a111-111111111111",
  "role": "user",
  "content": "What did I say about PostgreSQL last week?",
  "tokenCount": 12,
  "idempotencyKey": "0194d7b4-9d31-7d31-a222-222222222222"
}
```

- `sessionId` — UUID of the conversation session
- `userId` — UUID of the user who owns the conversation
- `role` — `user | assistant | system`
- `content` — exact message text
- `tokenCount` — optional integer token count
- `idempotencyKey` — UUID used to prevent duplicate insertion on retry

**Return values:**

- `message` — inserted message record

**Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "message": {
      "messageId": "0194d7b4-9d31-7d31-a333-333333333333",
      "sessionId": "0194d7b4-9d31-7d31-a444-444444444444",
      "userId": "0194d7b4-9d31-7d31-a111-111111111111",
      "role": "user",
      "content": "What did I say about PostgreSQL last week?",
      "tokenCount": 12,
      "createdAt": "2026-03-04T12:34:56Z"
    }
  },
  "error": null
}
```

**Dataflow:**

1. Request arrives; service validates required fields and verifies that the supplied `sessionId` belongs to the supplied `userId`.
2. `idempotencyKey` uniqueness is enforced per session.
3. If the same `idempotencyKey` is retried with the same payload, the existing message record is returned.
4. If the same `idempotencyKey` is retried with a different payload, the service returns a `conflict` error.
5. If the request is new, the service inserts the row into `chat_schema.messages`.
6. The inserted or previously existing message record is returned.

#### `GET /api/v1/chat/{sessionId}/messages`

**Request arguments:**

- **Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "messages": [
      {
        "messageId": "0194d7b4-9d31-7d31-a333-333333333333",
        "sessionId": "0194d7b4-9d31-7d31-a444-444444444444",
        "userId": "0194d7b4-9d31-7d31-a111-111111111111",
        "role": "user",
        "content": "What did I say about PostgreSQL last week?",
        "tokenCount": 12,
        "createdAt": "2026-03-04T12:34:56Z"
      }
    ]
  },
  "error": null
}
```

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

This API intentionally exposes high-level memory operations rather than full CRUD over every underlying table. The primary workflow is save, semantic query, and full export, with direct fact correction available where structured edits are necessary.

#### `POST /api/v1/personal_info/{userId}/save`

**Request arguments:**

- **Request JSON:**

```json
{
  "content": "Colby prefers PostgreSQL with pgvector for agent memory because it keeps relational data and vector search in one system.",
  "sourceType": "chat",
  "sourceId": "0194d7b4-9d31-7d31-a555-555555555555",
  "extractFacts": true
}
```

- `userId` — UUID of the user
- `content` — raw text content to intelligently store
- `sourceType` — `chat | uploaded_file | document | web`
- `sourceId` — UUID of the original source
- `extractFacts` — boolean indicating whether to run the structured fact extraction pipeline

**Return values:**

- `chunkIds` — array of UUIDs for inserted semantic chunks
- `factIds` — array of UUIDs for inserted structured facts
- `sourceReferenceIds` — array of UUIDs for inserted traceability links

**Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "chunkIds": ["0194d7b4-9d31-7d31-a666-666666666666"],
    "factIds": ["0194d7b4-9d31-7d31-a777-777777777777"],
    "sourceReferenceIds": [
      "0194d7b4-9d31-7d31-a888-888888888888",
      "0194d7b4-9d31-7d31-a999-999999999999"
    ]
  },
  "error": null
}
```

**Dataflow:**

1. Service validates that `userId` exists and that the source payload is well-formed.
2. Raw `content` is accepted.
3. The Go service internally chunks the content into smaller semantic units.
4. The service calls an embedding model to vectorize each chunk.
5. Chunks and vectors are written into `personal_info_schema.personal_info_chunks`.
6. Exactly one `source_references` row is created for each inserted chunk.
7. If `extractFacts` is true, the service runs the fact extraction pipeline over the content.
8. Extracted facts are written into `personal_info_schema.user_facts`.
9. Exactly one `source_references` row is created for each inserted fact.
10. Existing source reference rows are not reused during this operation; a new traceability row is inserted for each created chunk or fact.
11. The service returns the created chunk, fact, and source reference IDs.

#### `POST /api/v1/personal_info/{userId}/query`

**Request arguments:**

- **Request JSON:**

```json
{
  "query": "What database choice has Colby preferred for memory service work?",
  "topK": 5
}
```

- `userId` — UUID of the user
- `query` — natural language query string
- `topK` — maximum number of vector results to return

**Return values:**

- `results` — ordered list of semantic memory matches. Each match contains:
  - `chunkId`
  - `text`
  - `similarityScore` — cosine similarity score in the range `[0, 1]`, where higher is better
  - `sourceReferences` — array of related source reference records

**Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "results": [
      {
        "chunkId": "0194d7b4-9d31-7d31-a666-666666666666",
        "text": "Colby prefers PostgreSQL with pgvector for agent memory because it keeps relational data and vector search in one system.",
        "similarityScore": 0.93,
        "sourceReferences": [
          {
            "sourceReferenceId": "0194d7b4-9d31-7d31-a888-888888888888",
            "targetId": "0194d7b4-9d31-7d31-a666-666666666666",
            "targetType": "chunk",
            "sourceId": "0194d7b4-9d31-7d31-a555-555555555555",
            "sourceType": "chat"
          }
        ]
      }
    ]
  },
  "error": null
}
```

**Dataflow:**

1. The query string is converted into an embedding vector internally.
2. A pgvector approximate nearest-neighbor cosine-similarity search is executed against `personal_info_chunks` for this `userId`.
3. Raw pgvector distance values are converted into cosine similarity scores in the range `[0, 1]` for the API response.
4. Results are ranked by similarity score, with higher scores treated as better matches.
5. Ties are broken by `created_at` descending.
6. Related source reference rows are loaded and attached to each matched chunk.
7. The top `K` ranked matches are returned.
8. This endpoint returns chunk matches only; it does not directly return fact rows.

#### `GET /api/v1/personal_info/{userId}/all`

**Request arguments:**

- `userId` — UUID of the user from the path

**Return values:**

- `facts` — array of structured fact objects. Each object contains:
  - `factId`
  - `userId`
  - `category`
  - `factKey`
  - `factValue`
  - `confidence`
  - `version`
  - `updatedAt`
- `chunks` — array of semantic chunk objects. Each object contains:
  - `chunkId`
  - `userId`
  - `rawText`
  - `modelVersion`
  - `createdAt`

**Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "facts": [
      {
        "factId": "0194d7b4-9d31-7d31-a777-777777777777",
        "userId": "0194d7b4-9d31-7d31-a111-111111111111",
        "category": "Preferences",
        "factKey": "memory_database_choice",
        "factValue": "PostgreSQL with pgvector",
        "confidence": 0.94,
        "version": 1,
        "updatedAt": "2026-03-04T12:35:10Z"
      }
    ],
    "chunks": [
      {
        "chunkId": "0194d7b4-9d31-7d31-a666-666666666666",
        "userId": "0194d7b4-9d31-7d31-a111-111111111111",
        "rawText": "Colby prefers PostgreSQL with pgvector for agent memory because it keeps relational data and vector search in one system.",
        "modelVersion": "text-embedding-3-large",
        "createdAt": "2026-03-04T12:35:00Z"
      }
    ]
  },
  "error": null
}
```

**Dataflow:**

1. Service queries all rows in `personal_info_schema.user_facts` for the specified `userId`.
2. Service queries all rows in `personal_info_schema.personal_info_chunks` for the specified `userId`.
3. Vector payloads are excluded from the response.
4. The two datasets are packaged into a single response envelope.

#### `PUT /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**

- **Request JSON:**

```json
{
  "category": "Preferences",
  "factKey": "memory_database_choice",
  "factValue": "PostgreSQL with pgvector",
  "confidence": 0.97,
  "version": 1
}
```

- `userId` — UUID of the user
- `factId` — UUID of the fact
- `category` — updated category string
- `factKey` — updated key name
- `factValue` — updated JSON value
- `confidence` — updated confidence score
- `version` — current version for optimistic concurrency control

**Return values:**

- `fact` — updated fact record

**Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "fact": {
      "factId": "0194d7b4-9d31-7d31-a777-777777777777",
      "userId": "0194d7b4-9d31-7d31-a111-111111111111",
      "category": "Preferences",
      "factKey": "memory_database_choice",
      "factValue": "PostgreSQL with pgvector",
      "confidence": 0.97,
      "version": 2,
      "updatedAt": "2026-03-05T09:00:00Z"
    }
  },
  "error": null
}
```

**Dataflow:**

1. Service loads the target fact by `factId` and `userId`.
2. Service compares the submitted `version` against the stored version.
3. If versions do not match, the service returns a `conflict` error.
4. If versions match, the fact row is updated and the version is incremented.
5. The updated fact metadata is returned.
6. PUT is treated as full replacement of the mutable fact fields; partial updates are not supported.

#### `DELETE /api/v1/personal_info/{userId}/facts/{factId}`

**Request arguments:**

- `userId` — UUID of the user
- `factId` — UUID of the fact

**Return values:**

- `deleted` — boolean
- `factId` — UUID of deleted fact

**Successful response JSON:**

```json
{
  "ok": true,
  "data": {
    "deleted": true,
    "factId": "0194d7b4-9d31-7d31-a777-777777777777"
  },
  "error": null
}
```

**Dataflow:**

1. Service validates that the fact exists for the specified user.
2. The target row is deleted from `personal_info_schema.user_facts`.
3. The service returns the deletion result.

### D. Agent Log Store

**Target Schema:** `agent_logs_schema`

**Purpose:** To track the autonomous actions, tool executions, and reasoning paths of the internal AI agents.

Task execution records are append-only audit records. Update and delete operations are intentionally not exposed through this API.

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

- `executions` — ordered array of execution records. Each record contains:
  - `executionId`
  - `taskId`
  - `agentId`
  - `actionType`
  - `payload`
  - `status`
  - `errorContext`
  - `createdAt`

### E. Service Log Store

**Target Schema:** `service_log_schema`

**Purpose:** To store system-level telemetry and infrastructure visibility records.

System events are append-only operational records. Update and delete operations are intentionally not exposed through this API. Retention and cleanup are handled outside this API boundary.

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

- `events` — ordered array of system event records. Each record contains:
  - `eventId`
  - `traceId`
  - `serviceName`
  - `severity`
  - `message`
  - `metadata`
  - `createdAt`

## 4. Database Schema (Logical Distribution & Data Modeling)

All schemas reside in a logically distributed PostgreSQL instance. Primary keys utilize UUIDv7. Logical sharding keys (`user_id`, `session_id`) are strictly enforced on all tables to allow future physical distribution.

### Schema: identity_schema

**Table: users**

| Column Name | Data Type    | Constraints / Indexes  | Distributed Purpose / Description                |
| :---------- | :----------- | :--------------------- | :----------------------------------------------- |
| id          | UUID         | PRIMARY KEY            | Generated via UUIDv7. Canonical user identifier. |
| email       | VARCHAR(255) | UNIQUE INDEX, NOT NULL | Human identity and login correlation field.      |
| created_at  | TIMESTAMPTZ  | DEFAULT NOW()          | User creation timestamp.                         |

### Schema: chat_schema (Table: messages)

| Column Name     | Data Type   | Constraints / Indexes | Distributed Purpose / Description                                            |
| :-------------- | :---------- | :-------------------- | :--------------------------------------------------------------------------- |
| id              | UUID        | PRIMARY KEY           | Generated via UUIDv7.                                                        |
| session_id      | UUID        | INDEX, NOT NULL       | **Logical sharding key**. All messages for a session stay on the same shard. |
| user_id         | UUID        | INDEX, NOT NULL       | Links the session to the canonical identity record.                          |
| role            | VARCHAR(50) | NOT NULL              | `user`, `assistant`, or `system`.                                            |
| content         | TEXT        | NOT NULL              | The exact text of the message.                                               |
| token_count     | INT         |                       | Optional computed token count.                                               |
| idempotency_key | UUID        | UNIQUE INDEX          | Prevents duplicate insertion.                                                |
| created_at      | TIMESTAMPTZ | DEFAULT NOW()         | Immutable timestamp of the event.                                            |

### Schema: personal_info_schema

This schema stores long-term memory records and is the primary schema where the service performs chunking, embedding-backed retrieval, fact persistence, and traceability linking.

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

| Column Name | Data Type    | Constraints / Indexes                           | Distributed Purpose / Description                                                             |
| :---------- | :----------- | :---------------------------------------------- | :-------------------------------------------------------------------------------------------- |
| id          | UUID         | PRIMARY KEY                                     | Generated via UUIDv7.                                                                         |
| user_id     | UUID         | INDEX, NOT NULL                                 | **Logical sharding key**.                                                                     |
| category    | VARCHAR(50)  | INDEX                                           | `Diet`, `Code_Preference`, `Relationships`, etc.                                              |
| fact_key    | VARCHAR(100) | NOT NULL                                        | Example: `allergy`.                                                                           |
| fact_value  | JSONB        | NOT NULL                                        | Flexible structured value.                                                                    |
| confidence  | FLOAT        | CHECK (confidence >= 0.0 AND confidence <= 1.0) | AI certainty score.                                                                           |
| version     | INT          | DEFAULT 1                                       | **Optimistic concurrency control** field. Prevents race conditions during concurrent updates. |
| updated_at  | TIMESTAMPTZ  | DEFAULT NOW()                                   | Last update timestamp.                                                                        |

**Table: source_references**

| Column Name | Data Type   | Constraints / Indexes | Distributed Purpose / Description                                        |
| :---------- | :---------- | :-------------------- | :----------------------------------------------------------------------- |
| id          | UUID        | PRIMARY KEY           | Generated via UUIDv7.                                                    |
| user_id     | UUID        | INDEX, NOT NULL       | **Logical sharding key**. Ensures references reside on the user's shard. |
| target_id   | UUID        | INDEX, NOT NULL       | ID of the chunk or fact being referenced.                                |
| target_type | VARCHAR(50) | NOT NULL              | `chunk` or `fact`.                                                       |
| source_id   | UUID        | INDEX, NOT NULL       | ID of the chat message, uploaded file, document, etc.                    |
| source_type | VARCHAR(50) | NOT NULL              | `chat`, `uploaded_file`, `document`, `web`.                              |

### Schema: agent_logs_schema (Table: task_executions)

| Column Name   | Data Type    | Constraints / Indexes | Distributed Purpose / Description                                |
| :------------ | :----------- | :-------------------- | :--------------------------------------------------------------- |
| id            | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                            |
| task_id       | UUID         | INDEX, NOT NULL       | **Correlation ID** grouping all agent actions for the same task. |
| agent_id      | VARCHAR(100) | INDEX, NOT NULL       | Specific agent executing the step.                               |
| action_type   | VARCHAR(50)  | NOT NULL              | `tool_call`, `reasoning_step`, or `final_answer`.                |
| payload       | JSONB        | NOT NULL              | Exact input/output of the tool call or reasoning step.           |
| status        | VARCHAR(20)  | NOT NULL              | `pending`, `success`, or `failed`.                               |
| error_context | TEXT         |                       | Stack trace or model failure reason.                             |
| created_at    | TIMESTAMPTZ  | DEFAULT NOW()         | Event timestamp.                                                 |

### Schema: service_log_schema (Table: system_events)

| Column Name  | Data Type    | Constraints / Indexes | Distributed Purpose / Description                                  |
| :----------- | :----------- | :-------------------- | :----------------------------------------------------------------- |
| id           | UUID         | PRIMARY KEY           | Generated via UUIDv7.                                              |
| trace_id     | UUID         | INDEX                 | **Distributed trace ID** passed through service boundaries.        |
| service_name | VARCHAR(100) | INDEX                 | Emitting service name.                                             |
| severity     | VARCHAR(20)  | INDEX                 | `INFO`, `WARN`, `ERROR`, or `FATAL`.                               |
| message      | TEXT         | NOT NULL              | Human-readable log message.                                        |
| metadata     | JSONB        |                       | Structured machine-readable context (e.g., latency, memory usage). |
| created_at   | TIMESTAMPTZ  | DEFAULT NOW()         | Time-series optimized event timestamp.                             |
