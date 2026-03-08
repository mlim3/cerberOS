# Integration Memo: Memory Service

This document serves as the technical integration guide for the Orchestrator and Vault teams to interact with the Memory Service.

## Base URL & Swagger Link

The Memory Service API is available at the following base URL (assuming local development):
**`http://localhost:8080/api/v1`**

Live API documentation is available via Swagger UI at:
**`http://localhost:8080/swagger/index.html`**

## Header Requirements

When interacting with the Memory Service, specific headers are required depending on the endpoint and the context of the request.

| Header Name | Required For | Description |
| :--- | :--- | :--- |
| `X-API-KEY` | Vault endpoints (`/vault/*`) | Used to guard access to sensitive secret management functions. Must match the `INTERNAL_VAULT_API_KEY` defined in the Memory Service's environment configuration. |
| `X-Trace-ID` | All endpoints (Optional but highly recommended) | Used for cross-service debugging and distributed tracing. If provided, the Memory Service will include this ID in its logs, allowing requests to be traced across the entire system. |

## The Semantic Search Contract

The Memory Service provides a semantic search endpoint for retrieving personal information chunks relevant to a query.

**Endpoint:** `POST /personal_info/{userId}/query`

**Response Interpretation:**
The response will include a list of relevant information chunks, each with a `similarityScore`.
*   The `similarityScore` represents the cosine similarity between the query and the stored chunk.
*   **Interpretation:** A score closer to **1.0** indicates a higher relevance/similarity. A score of 1.0 means an exact match, while lower scores indicate less similarity.

## Vault Policy

The Memory Service implements strict security measures for handling sensitive data.

**Crucial Policy:**
*   The Memory Service is the **sole holder** of the `VAULT_MASTER_KEY`.
*   All secrets are stored in the database securely encrypted using AES-256-GCM.
*   The Orchestrator and other services **will only receive plaintext secrets after a successful and authorized decryption request** via the internal Vault endpoints. The Vault itself never exposes the raw encrypted bytes or the master key to external services.
