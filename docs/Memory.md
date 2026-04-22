A highly secure, distributed memory and agent orchestration service.

## Setup Instructions

For new teammates, getting started is easy. We've provided a bootstrap script to set up your environment:

From the root directory of the project

```bash
cd scripts
bash mem-up.sh
```

You can also run the script with the `--seed` flag to populate the database with mock data:

```bash
bash mem-up.sh --seed
```

This will ensure you have all the necessary dependencies and tools installed.

## Local Development Shortcuts

From `memory/`:

Build the CLI:

```bash
go build -o memory-cli ./cmd/cli
```

Run the service directly:

```bash
go run ./cmd/server
```

Regenerate Swagger artifacts:

```bash
go generate ./cmd/server
```

## What The Memory Service Stores

The memory service is the persistence layer for:

- **Conversations, tasks, and messages** — user-owned chat threads, execution tasks linked to conversations, and immutable chat messages
- **Personal info** — raw text chunks with vector embeddings for semantic retrieval, plus structured facts with full lifecycle support (archive, supersession, versioned updates)
- **Vault secrets** — per-user encrypted secrets stored with AES-256-GCM, decryptable only through the internal API
- **Orchestrator records** — durable state and audit records for internal orchestrator workflows
- **Scheduled jobs** — job definitions and run history for internal and external recurring work
- **Agent execution logs** — per-task execution trace entries
- **System events** — internal service event log

## API Documentation (Swagger)

We use generated Swagger artifacts for API documentation. Once the server is running, you can access the Swagger UI at:

[http://localhost:8081/swagger/index.html](http://localhost:8081/swagger/index.html)

This interface provides a complete, interactive documentation of all available endpoints, allowing you to easily discover and test the API.

## Security & Vault

Security is a top priority for cerberOS, especially concerning sensitive personal information. We've implemented a robust Vault system.

- **Endpoint Guarding (`X-Internal-API-Key`):** All Vault endpoints and all Orchestrator endpoints are strictly internal and guarded by an `X-Internal-API-Key` header. This key must match the `INTERNAL_VAULT_API_KEY` defined in the environment configuration. This prevents unauthorized external access to sensitive secret management and orchestrator persistence functions.
- **Master Key Isolation (`VAULT_MASTER_KEY`):** The encryption and decryption of secrets rely on application-level AES-256-GCM encryption. The key used for this encryption, `VAULT_MASTER_KEY`, must be a 32-byte hex-encoded string. It is injected via the environment and isolated from the database itself. If the database is compromised, the data remains encrypted and inaccessible without this master key.

## Tracing

The Memory service participates in distributed tracing by reading the W3C `traceparent` header when it is present. When no valid `traceparent` header is supplied, it falls back to generating a fresh trace ID for compatibility with direct local requests and older callers.

## Metrics

Prometheus metrics are exposed at `GET /internal/metrics` (bypassing the standard `/api/v1` versioning). This endpoint tracks `http_requests_total` and `http_request_duration_seconds` for comprehensive monitoring.

## Full Specification

For the complete endpoint reference, data model, CLI commands, local development setup, and known gaps, see [memory/README.md](../memory/README.md).
