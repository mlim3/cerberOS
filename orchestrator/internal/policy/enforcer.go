// Package policy implements M3: Policy Enforcer.
//
// The Policy Enforcer validates every task against the Vault (OpenBao) policy
// set before dispatch. It derives and attaches a policy_scope to every approved
// task_spec. This scope is the immutable ceiling for all agent credential requests.
//
// Responsibilities (§4.1 M3):
//   - Query OpenBao to validate user permission scope covers required_skill_domains
//   - Derive and attach policy_scope to validated task_spec
//   - Reject tasks with out-of-scope skill domains (returns POLICY_VIOLATION)
//   - Log every policy decision (ALLOW/DENY) as a structured audit event — always
//   - Maintain a TTL-based policy cache (see cache.go)
//   - Handle Vault unavailability per vault_failure_mode (FAIL_CLOSED default)
//   - Trigger credential revocation on every terminal task outcome
//
// CRITICAL (§2.2): Policy enforcement is not advisory.
// DENY → POLICY_VIOLATION to User I/O. Never silently dropped. No partial execution.
// CRITICAL (§FR-PE-03): Every policy decision writes an audit_event, ALLOW or DENY.
package policy

import (
	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// Enforcer is M3: Policy Enforcer.
type Enforcer struct {
	cfg    *config.OrchestratorConfig
	vault  interfaces.VaultClient
	memory interfaces.MemoryClient
	cache  *PolicyCache
	nodeID string
}

// New creates a new Policy Enforcer.
func New(cfg *config.OrchestratorConfig, vault interfaces.VaultClient, memory interfaces.MemoryClient) *Enforcer {
	return &Enforcer{
		cfg:    cfg,
		vault:  vault,
		memory: memory,
		cache:  NewPolicyCache(cfg.VaultPolicyCacheTTL),
		nodeID: cfg.NodeID,
	}
}

// ValidateAndScope validates the task against Vault and returns the derived policy_scope.
// On ALLOW: writes policy_allow audit event, returns PolicyScope.
// On DENY:  writes policy_deny audit event, returns error with human-readable reason.
// On Vault unreachable: applies vault_failure_mode (FAIL_CLOSED by default).
//
// CRITICAL: Audit event is written regardless of outcome (§FR-PE-03).
//
// TODO Phase 2: implement full logic:
//   - Check cache first (cache key: userID + sorted(skillDomains))
//   - On cache miss: call vault.ValidateAndScope()
//   - On Vault error: apply vault_failure_mode
//   - Write audit_event to memory (always)
//   - On success: store in cache, return scope
func (e *Enforcer) ValidateAndScope(userID string, requiredSkillDomains []string, timeoutSeconds int) (types.PolicyScope, error) {
	// TODO Phase 2
	return types.PolicyScope{}, nil
}

// VerifyScopeStillValid confirms a policy_scope has not expired before recovery re-dispatch.
// The scope CANNOT expand — if Vault says the scope has changed, escalate to SCOPE_EXPIRED (§13.2).
//
// TODO Phase 2: implement
func (e *Enforcer) VerifyScopeStillValid(scope types.PolicyScope) error {
	// TODO Phase 2
	return nil
}

// RevokeCredentials revokes the Vault token for the given orchestrator_task_ref.
// Called by Recovery Manager on every terminal task outcome — non-optional (§13.3).
//
// On Vault unavailability: log REVOCATION_FAILED, schedule retry (max 5 attempts,
// exponential backoff). Does NOT block task termination.
//
// TODO Phase 2: implement with retry scheduler
func (e *Enforcer) RevokeCredentials(orchestratorTaskRef string) error {
	// TODO Phase 2
	return nil
}

// InvalidateCacheForPolicy clears cached policies matching a Vault policy update event.
// Called when the Gateway receives a Vault policy update notification via NATS (§FR-PE-05).
//
// TODO Phase 2: implement
func (e *Enforcer) InvalidateCacheForPolicy(policyName string) {
	// TODO Phase 2
	e.cache.InvalidateAll() // conservative: invalidate all on any Vault policy change
}

// writeAuditEvent persists a policy decision to the Memory Component.
// Must be called on every policy check, regardless of outcome (§FR-PE-03).
//
// TODO Phase 2: implement
func (e *Enforcer) writeAuditEvent(taskID, orchestratorTaskRef, userID string, outcome string, reason string) {
	// TODO Phase 2
}
