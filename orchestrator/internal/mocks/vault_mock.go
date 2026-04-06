package mocks

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

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

	now := time.Now()
	return types.PolicyScope{
		Domains:   requiredSkillDomains,
		TokenRef:  fmt.Sprintf("mock-token-accessor-%s", userID),
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Duration(timeoutSeconds+300) * time.Second),
		Metadata: map[string]string{
			"user_id":  userID,
			"mock":     "true",
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

// Reset clears all state and control flags. Useful between test cases.
func (m *VaultMock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ShouldDeny = false
	m.ShouldBeUnreachable = false
	m.ShouldScopeExpired = false
	m.DenyReason = ""
	m.RevokedRefs = nil
	m.ValidatedTasks = nil
	m.HealthCheckCalls = 0
}
