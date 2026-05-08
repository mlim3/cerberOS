package mocks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// domainToCredTypes maps each skill domain to the credential types it requires.
// Used by VaultMock to derive AvailableCredTypes from requested domains.
var domainToCredTypes = map[string][]string{
	"web":           {"web_api_key"},
	"data":          {"data_read_key", "data_write_key"},
	"comms":         {"comms_api_key"},
	"storage":       {"storage_credential"},
	"google_search": {"google_search_api_key"},
	"github":        {"github_token"},
}

// VaultMock is a controllable in-memory implementation of interfaces.VaultClient.
// Use it in unit tests and the demo to simulate Vault behavior without a real OpenBao instance.
//
// Usage:
//
//	vault := &mocks.VaultMock{}
//	vault.ShouldDeny = true                   // simulate policy denial
//	vault.ShouldBeUnreachable = true          // simulate Vault being down
//	vault.RevokedRefs                         // inspect what was revoked
type VaultMock struct {
	mu sync.Mutex

	// Control flags — set these before calling methods to simulate scenarios
	ShouldDeny          bool   // ValidateAndScope returns POLICY_VIOLATION
	ShouldBeUnreachable bool   // All calls return connection error (simulate Vault down)
	ShouldScopeExpired  bool   // VerifyScopeStillValid returns expired error
	DenyReason          string // Custom denial reason (default: "domain not in user policy")

	// UserCredTypes overrides available credential types per user_id.
	// If nil, all credential types for the requested domains are returned (optimistic mock).
	// Set this in tests that need to simulate a user with only specific credentials registered.
	UserCredTypes map[string][]string // user_id → []credential_type

	// Inspection — read these after calls to verify behavior
	RevokedRefs      []string // All orchestrator_task_refs that had credentials revoked
	ValidatedTasks   []string // All task IDs that passed ValidateAndScope
	HealthCheckCalls int      // Number of times HealthCheck was called
}

func (m *VaultMock) ValidateAndScope(userID string, requiredSkillDomains []string, timeoutSeconds int) (types.PolicyScope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ShouldBeUnreachable {
		return types.PolicyScope{}, errors.New("vault unreachable: connection refused")
	}

	if m.ShouldDeny {
		reason := m.DenyReason
		if reason == "" {
			reason = "domain not in user policy"
		}
		return types.PolicyScope{}, fmt.Errorf("policy denied: %s", reason)
	}

	m.ValidatedTasks = append(m.ValidatedTasks, userID)

	// Derive available credential types from the requested domains.
	var availableCredTypes []string
	if override, ok := m.UserCredTypes[userID]; ok {
		availableCredTypes = override
	} else {
		seen := map[string]bool{}
		for _, domain := range requiredSkillDomains {
			for _, ct := range domainToCredTypes[domain] {
				if !seen[ct] {
					seen[ct] = true
					availableCredTypes = append(availableCredTypes, ct)
				}
			}
		}
	}

	now := time.Now()
	return types.PolicyScope{
		Domains:            requiredSkillDomains,
		TokenRef:           fmt.Sprintf("mock-token-accessor-%s", userID),
		IssuedAt:           now,
		ExpiresAt:          now.Add(time.Duration(timeoutSeconds+300) * time.Second),
		AvailableCredTypes: availableCredTypes,
		Metadata: map[string]string{
			"user_id": userID,
			"mock":    "true",
		},
	}, nil
}

func (m *VaultMock) VerifyScopeStillValid(scope types.PolicyScope) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ShouldBeUnreachable {
		return errors.New("vault unreachable: cannot verify scope")
	}

	if m.ShouldScopeExpired {
		return errors.New("scope expired: token_ref no longer valid")
	}

	if time.Now().After(scope.ExpiresAt) {
		return errors.New("scope expired: past expiry time")
	}

	return nil
}

func (m *VaultMock) RevokeCredentials(orchestratorTaskRef string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ShouldBeUnreachable {
		return errors.New("vault unreachable: revocation failed")
	}

	m.RevokedRefs = append(m.RevokedRefs, orchestratorTaskRef)
	return nil
}

func (m *VaultMock) HealthCheck() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.HealthCheckCalls++

	if m.ShouldBeUnreachable {
		return errors.New("vault unreachable")
	}
	return nil
}

func (m *VaultMock) Execute(_ context.Context, userID string, req types.VaultExecuteRequest) (types.VaultExecuteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ShouldBeUnreachable {
		return types.VaultExecuteResult{}, errors.New("vault unreachable: cannot execute operation")
	}

	// Return a synthetic success result so tests and demos don't need a real vault.
	result, _ := json.Marshal(map[string]any{
		"mock":           true,
		"operation_type": req.OperationType,
		"user_id":        userID,
	})
	return types.VaultExecuteResult{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          types.VaultExecStatusSuccess,
		OperationResult: result,
		ElapsedMS:       1,
	}, nil
}

// Reset clears all state and control flags. Useful between test cases.
func (m *VaultMock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ShouldDeny = false
	m.ShouldBeUnreachable = false
	m.ShouldScopeExpired = false
	m.DenyReason = ""
	m.UserCredTypes = nil
	m.RevokedRefs = nil
	m.ValidatedTasks = nil
	m.HealthCheckCalls = 0
}
