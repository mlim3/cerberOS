package interfaces

import "github.com/mlim3/cerberOS/orchestrator/internal/types"

// VaultClient defines how the Policy Enforcer communicates with OpenBao (§11.3).
// The real implementation uses the OpenBao HTTP API.
// The mock implementation is in internal/mocks/vault_mock.go.
//
// CRITICAL: Policy enforcement is not advisory. A task that cannot be scoped
// safely is returned as POLICY_VIOLATION. It is never silently dropped (§2.2).
type VaultClient interface {
	// ValidateAndScope queries Vault to confirm the user's permission scope
	// covers all required skill domains. On success, returns a PolicyScope
	// that becomes the immutable ceiling for all agent credential requests.
	// On failure, returns a human-readable error for the POLICY_VIOLATION response.
	ValidateAndScope(userID string, requiredSkillDomains []string, timeoutSeconds int) (types.PolicyScope, error)

	// VerifyScopeStillValid confirms the existing policy_scope has not expired
	// before a recovery re-dispatch. Scope CANNOT expand during recovery (§13.2).
	VerifyScopeStillValid(scope types.PolicyScope) error

	// RevokeCredentials revokes the Vault token associated with the given
	// orchestrator_task_ref. Called on every terminal task outcome (§13.3).
	// On Vault unavailability, logs REVOCATION_FAILED and queues retry — does
	// not block task termination.
	RevokeCredentials(orchestratorTaskRef string) error

	// HealthCheck probes Vault reachability (GET /v1/sys/health).
	// Used by the health monitoring loop every 10 seconds (§12.1).
	HealthCheck() error
}
