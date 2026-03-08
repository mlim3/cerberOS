A highly secure, distributed memory and agent orchestration service.

## Setup Instructions

For new teammates, getting started is easy. We've provided a bootstrap script to set up your environment:

From the root directory of the project
```bash
cd scripts
bash mem-up.sh
```

This will ensure you have all the necessary dependencies and tools installed.

## API Documentation (Swagger)

We use Swagger (OpenAPI 3.0) for API documentation. Once the server is running, you can access the Swagger UI at:

[http://localhost:8080/swagger/index.html](http://localhost:8080/swagger/index.html)

This interface provides a complete, interactive documentation of all available endpoints, allowing you to easily discover and test the API.

## Security & Vault

Security is a top priority for cerberOS, especially concerning sensitive personal information. We've implemented a robust Vault system.

*   **Endpoint Guarding (`X-API-KEY`):** All Vault endpoints are strictly internal and guarded by an `X-API-KEY` header. This key must match the `INTERNAL_VAULT_API_KEY` defined in the environment configuration. This prevents unauthorized external access to sensitive secret management functions.
*   **Master Key Isolation (`VAULT_MASTER_KEY`):** The encryption and decryption of secrets rely on application-level AES-256-GCM encryption. The key used for this encryption, `VAULT_MASTER_KEY`, must be a 32-byte hex-encoded string. It is injected via the environment and isolated from the database itself. If the database is compromised, the data remains encrypted and inaccessible without this master key.

## Metrics

Prometheus metrics are exposed at `GET /internal/metrics` (bypassing the standard `/api/v1` versioning). This endpoint tracks `http_requests_total` and `http_request_duration_seconds` for comprehensive monitoring.