// Package policy implements M3: Policy Enforcer.
//
// The Policy Enforcer validates every task against the Vault (OpenBao) policy
// set before dispatch. It derives and returns a policy_scope for every approved
// task. This scope is the immutable ceiling for all agent credential requests.
//
// Responsibilities (§4.1 M3):
//   - Query OpenBao to validate user permission scope covers required_skill_domains
//   - Derive and return policy_scope for validated task_spec
//   - Reject tasks with out-of-scope skill domains (returns POLICY_VIOLATION)
//   - Log every policy decision (ALLOW/DENY) as a structured audit event — always attempted
//   - Maintain a TTL-based policy cache (see cache.go)
//   - Handle Vault unavailability per vault_failure_mode (FAIL_CLOSED default)
//   - Trigger credential revocation on every terminal task outcome
//
// CRITICAL (§2.2): Policy enforcement is not advisory.
// DENY → POLICY_VIOLATION to User I/O. Never silently dropped. No partial execution.
// CRITICAL (§FR-PE-03): Every policy decision attempts to write an audit_event, ALLOW or DENY.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
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
func New(
	cfg *config.OrchestratorConfig,
	vault interfaces.VaultClient,
	memory interfaces.MemoryClient,
) *Enforcer {
	return &Enforcer{
		cfg:    cfg,
		vault:  vault,
		memory: memory,
		cache:  NewPolicyCache(cfg.VaultPolicyCacheTTL),
		nodeID: cfg.NodeID,
	}
}

// ValidateAndScope validates a specific task against Vault and returns the
// derived policy_scope for that task.
// taskID and orchestratorTaskRef are included so every ALLOW/DENY decision
// can be written as a task-linked audit event to Memory.
func (e *Enforcer) ValidateAndScope(
	ctx context.Context,
	taskID string,
	orchestratorTaskRef string,
	userID string,
	requiredSkillDomains []string,
	timeoutSeconds int,
) (types.PolicyScope, error) {
	logger := observability.LogFromContext(ctx)
	if cachedScope, ok := e.cache.Get(userID, requiredSkillDomains); ok {
		auditErr := e.writeAuditEvent(ctx, taskID, orchestratorTaskRef, userID, types.OutcomeSuccess, "cache_hit")
		if auditErr != nil {
			logger.Warn("failed to write policy audit event on cache hit",
				"task_id", taskID,
				"orchestrator_task_ref", orchestratorTaskRef,
				"user_id", userID,
				"error", auditErr,
			)
		}
		return cachedScope, nil
	}

	scope, err := e.vault.ValidateAndScope(userID, requiredSkillDomains, timeoutSeconds)
	if err != nil {
		if e.cfg.VaultFailureMode == config.VaultFailureModeOpen {
			if cachedScope, ok := e.cache.Get(userID, requiredSkillDomains); ok {
				auditErr := e.writeAuditEvent(ctx, taskID, orchestratorTaskRef, userID, types.OutcomeSuccess, "cache_fallback")
				if auditErr != nil {
					logger.Warn("failed to write policy audit event on cache fallback",
						"task_id", taskID,
						"orchestrator_task_ref", orchestratorTaskRef,
						"user_id", userID,
						"error", auditErr,
					)
				}
				return cachedScope, nil
			}
		}

		auditErr := e.writeAuditEvent(ctx, taskID, orchestratorTaskRef, userID, types.OutcomeDenied, err.Error())
		if auditErr != nil {
			// TODO Phase 1: emit metric for audit write failure.
			logger.Warn("failed to write policy audit event on deny",
				"task_id", taskID,
				"orchestrator_task_ref", orchestratorTaskRef,
				"user_id", userID,
				"error", auditErr,
			)
		}
		return types.PolicyScope{}, err
	}

	e.cache.Set(userID, requiredSkillDomains, scope)

	auditErr := e.writeAuditEvent(ctx, taskID, orchestratorTaskRef, userID, types.OutcomeSuccess, "")
	if auditErr != nil {
		// TODO Phase 1: emit metric for audit write failure.
		logger.Warn("failed to write policy audit event on allow",
			"task_id", taskID,
			"orchestrator_task_ref", orchestratorTaskRef,
			"user_id", userID,
			"error", auditErr,
		)
	}

	return scope, nil
}

// VerifyScopeStillValid confirms a policy_scope has not expired before recovery re-dispatch.
// The scope CANNOT expand — if Vault says the scope has changed, escalate to SCOPE_EXPIRED (§13.2).
//
// TODO Phase 2: implement
func (e *Enforcer) VerifyScopeStillValid(_ context.Context, scope types.PolicyScope) error {
	_ = scope
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
func (e *Enforcer) RevokeCredentials(_ context.Context, orchestratorTaskRef string) error {
	_ = orchestratorTaskRef
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

// writeAuditEvent persists a policy ALLOW or DENY decision to Memory as a
// policy_event audit record.
// In phase 1, callers treat audit persistence as best effort: write failures
// are returned to the caller but do not override the primary Vault decision.
func (e *Enforcer) writeAuditEvent(_ context.Context, taskID, orchestratorTaskRef, userID string, outcome string, reason string) error {
	eventType := types.EventPolicyAllow
	if outcome != types.OutcomeSuccess {
		eventType = types.EventPolicyDeny
	}

	detail := map[string]any{"reason": reason}
	rawDetail, err := json.Marshal(detail)
	if err != nil {
		rawDetail = json.RawMessage(`{"reason":"failed to marshal policy event detail"}`)
	}

	timestamp := time.Now().UTC()
	event := types.AuditEvent{
		LogID:               fmt.Sprintf("policy-%d", timestamp.UnixNano()),
		OrchestratorTaskRef: orchestratorTaskRef,
		EventType:           eventType,
		InitiatingModule:    types.ModulePolicyEnforcer,
		Outcome:             outcome,
		EventDetail:         rawDetail,
		Timestamp:           timestamp,
		NodeID:              e.nodeID,
		TaskID:              taskID,
		UserID:              userID,
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}

	if err := e.memory.Write(types.OrchestratorMemoryWritePayload{
		OrchestratorTaskRef: orchestratorTaskRef,
		TaskID:              taskID,
		DataType:            types.DataTypePolicyEvent,
		Timestamp:           timestamp,
		Payload:             payload,
	}); err != nil {
		return fmt.Errorf("write policy audit event: %w", err)
	}

	return nil
}
