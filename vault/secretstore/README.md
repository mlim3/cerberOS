# vault/secretstore

The secretstore is a standalone HTTP microservice that sits between the engine and whatever actual secret backend your team uses (HashiCorp Vault, AWS Secrets Manager, GCP Secret Manager, etc.). It is the **only** component allowed to touch raw secret values.

## Responsibilities

1. Authenticate callers — only the engine may talk to it
2. Accept a list of secret keys from the engine
3. Delegate to the backend secret manager
4. Return resolved key→value pairs

Agents, users, and other services never interact with secretstore directly and never see plaintext secrets.

## API

```
POST /secrets/resolve
X-Engine-Token: <shared token>
Content-Type: application/json

{ "keys": ["API_KEY", "DB_PASS"] }
```

```json
200 OK
{ "secrets": { "API_KEY": "...", "DB_PASS": "..." } }
```

| Status | Meaning |
|--------|---------|
| `200`  | All keys resolved successfully |
| `400`  | Malformed request or empty key list |
| `401`  | Missing or wrong `X-Engine-Token` |
| `404`  | One or more keys not found in the backend |

## Design decisions

### 1. Batch API instead of per-key lookups — _Bulk Request pattern_

**Problem:** The engine processes scripts with an arbitrary number of `{{PLACEHOLDER}}` tokens. A per-key `GET /secret/{key}` API would mean one HTTP round-trip per secret, introducing latency that compounds with script complexity and adds noise to the backend secret manager's audit log.

**Solution:** The single `POST /secrets/resolve` endpoint accepts a list of keys and returns a map. The preprocessor scans the entire script once, collects all unique keys, and resolves them in a single call. One script execution = one secretstore call regardless of how many secrets the script uses.

**Trade-off:** If one key is missing the whole batch fails. This is intentional — a partially-substituted script executing in a VM would be worse than a clean error.

---

### 2. Pluggable backend via interface — _Strategy pattern_

**Problem:** The secretstore needs to work today with mock data and later with a real secret manager that another team owns. Hardcoding the backend couples the service to a specific implementation and makes it hard to test or hand off.

**Solution:** The `SecretManager` interface defines the only contract:

```go
type SecretManager interface {
    GetSecrets(keys []string) (map[string]string, error)
}
```

`MockSecretManager` satisfies it in-process for development. When the other team delivers their implementation (a Vault client, an AWS SDK wrapper, etc.), it drops in as a new struct that implements this interface. `main.go` wires the concrete type at startup — nothing else changes.

**Why this matters for the team boundary:** The other team can develop and test their `SecretManager` implementation independently. The secretstore only cares that it satisfies the interface.

---

### 3. Token-based caller authentication — _Shared Secret / Middleware Guard pattern_

**Problem:** The secretstore must only respond to the engine. Any other caller — including agents submitting scripts — must be silently rejected. Network-level isolation alone is not sufficient (other containers on the same network, misconfiguration, etc.).

**Solution:** A shared token is required on every request via the `X-Engine-Token` header. The `engineOnly` middleware wraps every route handler and rejects anything without the correct token before the request body is even read. The token is injected at runtime via the `ENGINE_TOKEN` env var (set on the secretstore) and `SECRET_STORE_TOKEN` (set on the engine). The service refuses to start if `ENGINE_TOKEN` is unset.

**Why a shared token rather than mTLS or OAuth:** This is the minimum viable auth for a service-to-service call on a private network. It keeps the implementation in pure stdlib (no certificates to manage, no OAuth server to run). Upgrade to mTLS when the deployment model warrants it — the middleware is the only thing that needs to change.

---

### 4. Secrets never leave this service in logs — _Secret Hygiene_

The secretstore never logs secret values. Error messages reference key names only (e.g., `secret not found: API_KEY`). The engine is responsible for scrubbing values from VM output after execution. This creates a clean boundary: secretstore owns raw values, engine owns scrubbing.

## Configuration

| Env var        | Required | Default       | Description |
|----------------|----------|---------------|-------------|
| `ENGINE_TOKEN` | yes      | —             | Shared token the engine must send on every request |
| `PORT`         | no       | `8001`        | Port the service listens on |

## Building

```sh
docker build -f Dockerfile -t vault-secretstore .
docker run -e ENGINE_TOKEN=<token> -p 8001:8001 vault-secretstore
```

## Integrating a real secret manager

1. Implement `SecretManager` in a new file (e.g., `vault_manager.go`)
2. In `main.go`, replace `NewMockSecretManager()` with your implementation
3. Add any env vars your implementation needs (Vault address, AWS region, etc.)

The HTTP layer, auth middleware, and API contract stay unchanged.
