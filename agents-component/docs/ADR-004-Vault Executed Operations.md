# ADR-004: Vault-Executed Operations Model

**Status:** Accepted  
**Date:** 2026-03-14  
**Supersedes:** Two-Phase Credential Delivery Model (§3.2 of EDD v1.5)

---

## Context

The original credential model (EDD §3.2) used a two-phase approach:

- **Phase 1 (Pre-Authorization):** At agent spawn, the Credential Broker requested a scoped
  policy token from the Vault. The agent received a `permission_token` reference — not a secret.
- **Phase 2 (Lazy Delivery):** At runtime, when a skill required a credential, the agent
  presented its `permission_token` and received the actual credential value (API key, password,
  etc.) in return. The agent then used that value directly to call external services.

Phase 2 — delivering raw credential values to agents — creates an unnecessary exposure surface:

1. **Credential in agent memory.** The raw secret exists in the agent's address space, even
   temporarily. In a multi-tenant Firecracker environment, defense-in-depth favors keeping
   secrets out of tenant memory entirely.
2. **Credential in transit.** Even over mTLS, the value traverses the Orchestrator routing layer.
   Routing code that never sees a credential value cannot leak one.
3. **Logging risk.** Any bug that causes the credential to appear in a log, error message, or
   debug trace produces a real incident. Agents that never receive credential values cannot
   produce this class of incident.
4. **Scope creep.** An agent with a raw API key can make arbitrary calls to the external service —
   not just the intended operation. A Vault that executes the operation can enforce call-level
   restrictions (specific endpoint, specific method, specific parameter shapes) that a raw
   credential cannot.

The question is whether accepting higher latency and implementation complexity for Vault-side
execution is the right tradeoff for this system.

---

## Decision

**Credentialed operations are executed by the Vault, not by agents.**

Agents never receive raw credential values. When a skill requires calling an external service
with a credential, the agent packages a `VaultOperationRequest` (operation type, endpoint, body,
and a logical `credential_ref`) and sends it to the Vault via the Orchestrator. The Vault:

1. Validates the operation is within the agent's pre-declared scope.
2. Fetches the credential internally (it never leaves Vault storage).
3. Executes the external call.
4. Returns the result — not the credential — to the agent.

Phase 1 pre-authorization is retained. At spawn, the Lifecycle Manager sends an `AgentScopeDeclaration`
to the Orchestrator, which registers the agent's permitted `credential_ref` set with the Vault.
This allows the Vault to make fast scope checks at operation time without re-parsing the task_spec.

The `internal/credentials` module becomes an **operation request formatter and router only**. It
packages `VaultOperationRequest` structs and passes them to `internal/comms`. It contains no
OpenBao SDK imports, no HTTP client code, and no credential value handling of any kind.

---

## Consequences

### Accepted Benefits

- **Credentials never leave the Vault.** The raw secret value has one location: OpenBao storage.
  No agent memory, no NATS payloads, no log files, no error messages contain a credential.
- **Call-level enforcement.** The Vault can restrict agents to specific endpoints and methods,
  not just credential scopes. A pre-authorization that permits `aegis-agents-payments` does not
  automatically permit arbitrary calls to the payment provider's admin API.
- **Elimination of credential delivery incidents.** The class of incident where a credential
  appears in a log or error trace is structurally impossible if agents never receive credentials.
- **Simpler agent code.** Agent skill logic sends an operation request and receives a result.
  There is no credential lifecycle management code (fetch, use, discard) in agent implementation.
- **Reduced blast radius.** A compromised agent process has no credential it can exfiltrate.
  It can only request operations within its pre-declared scope — and the Vault enforces that.

### Accepted Tradeoffs

- **Latency.** Each credentialed call adds one round-trip through the Orchestrator to the Vault
  and back. This is acceptable given that credentialed operations are network calls by nature —
  the Vault adds one internal hop to an already-external call.
- **Vault as execution engine.** The Vault must implement operation execution logic (HTTP client,
  response handling). This is additional complexity in the Vault team's scope. The interface
  contract (`VaultOperationRequest` / `VaultOperationResult`) is specified here; the Vault team
  implements the executor.
- **Opaque errors.** Vault execution may produce errors that are harder to diagnose from the
  agent side. The `VaultOperationResult.Error` field must be informative without leaking
  credential or internal Vault state details.

### No Longer Applicable

- **Phase 2 Lazy Delivery** (§3.2 of EDD v1.5) is removed. The `permission_token` delivery
  flow, the `POST /v1/auth/token/create` call from the Credential Broker, and all references
  to agents receiving credential values are superseded by this ADR.
- **EDD §3.3 Phase 2 API Contract** (`POST /v1/secrets/data/{path}` from Credential Broker)
  is superseded. The Credential Broker no longer calls OpenBao directly under any circumstance.

---

## Interface Contract

### VaultOperationRequest (Agent → Orchestrator → Vault)

```json
{
  "agent_id": "agent-abc123",
  "task_id": "task-xyz789",
  "operation_type": "http_post",
  "endpoint": "https://api.stripe.com/v1/charges",
  "headers": {
    "Content-Type": "application/json"
  },
  "body": { "amount": 1000, "currency": "usd" },
  "credential_ref": "stripe-api-key",
  "scope_required": "aegis-agents-payments"
}
```

### VaultOperationResult (Vault → Orchestrator → Agent)

```json
{
  "request_id": "vop-req-456",
  "status_code": 200,
  "body": { "id": "ch_abc", "status": "succeeded" },
  "error": ""
}
```

The `body` field contains the external service's response. The credential used to execute the
call is not present in either message.

---

## Spec Sections Requiring Update

The following sections of the EDD (Aegis-Agents Component v1.5) are superseded by this ADR
and require update on next revision:

| Section | Change Required |
|---------|----------------|
| §3.1 Security Model | Remove "Credential Broker fetches and delivers credentials to agents." Update security invariant to reflect that credentials never leave Vault. |
| §3.2 Two-Phase Credential Model | Remove Phase 2 (Lazy Delivery) rows. Retain Phase 1 (Pre-Authorization). Rename section to "Scope Pre-Authorization Model." |
| §3.3 API Contract — Phase 2 | Remove `POST /v1/secrets/data/{path}` Credential Broker call. Replace with `VaultOperationRequest` / `VaultOperationResult` contract table. |
| §3.6 Requirements | Update `CRED-REQ-*` requirements to reflect Vault-executed model. Add requirements for Vault operation executor interface. |
| M5 Module Description | Update Credential Broker description from "talks to OpenBao" to "packages operation requests; routes via Orchestrator; no credential values handled." |

---

## Alternatives Considered

**Keep Phase 2 Lazy Delivery with stricter memory handling.**  
Rejected. Stricter memory handling (zeroing after use, etc.) reduces but does not eliminate the
exposure surface. The structural solution is to never deliver the credential, not to handle it
more carefully after delivery.

**Agent-side credential caching with short TTL.**  
Rejected. Caching trades latency for a persistent in-memory secret. This is the opposite direction
from the goal.

**Per-call ephemeral tokens instead of Vault execution.**  
Considered. The Vault could issue a single-use, single-endpoint token instead of executing the
call itself. This preserves the external call in agent code but limits what the token can do.
Rejected because it still delivers a usable credential to the agent — a token scoped to one
endpoint is still a credential that can be exfiltrated and replayed within its TTL.
