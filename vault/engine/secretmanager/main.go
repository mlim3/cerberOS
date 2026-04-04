// Package secretmanager defines [SecretManager] and constructors that delegate to
// [mock] and [openbao] implementations.
//
// Wire explicitly:
//
//	secretmanager.NewMockSecretManager(auditor)
//	secretmanager.NewOpenBaoSecretManager(auditor) // nil → no client
package secretmanager

import (
	"context"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager/mock"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager/openbao"
)

// SecretManager resolves a batch of secret keys in a single atomic call.
// If any key is not found or access is denied, the entire call fails —
// no partial results are returned. This guarantees all-or-nothing semantics
// for the inject flow.
type SecretManager interface {
	// GetSecrets returns values for all requested keys or an error.
	// Callers must not receive a partial map on failure.
	GetSecrets(keys []string) (map[string]string, error)
	// PutSecret writes a secret value to the secret manager.
	PutSecret(ctx context.Context, key, value string) error
	// DeleteSecret deletes a secret from the secret manager.
	DeleteSecret(ctx context.Context, key string) error
}

// Type aliases for callers and tests that use concrete types without importing subpackages.
type (
	MockSecretManager    = mock.MockSecretManager
	OpenBaoSecretManager = openbao.OpenBaoSecretManager
)

// KvMount is the KV v2 mount path used by the OpenBao backend.
const KvMount = openbao.KvMount

// NewMockSecretManager returns an in-memory SecretManager with seeded dev keys.
func NewMockSecretManager(logger *audit.Logger) *mock.MockSecretManager {
	return mock.New(logger)
}

// NewOpenBaoSecretManager returns an OpenBao-backed SecretManager, or nil if the client cannot be created.
func NewOpenBaoSecretManager(logger *audit.Logger) *openbao.OpenBaoSecretManager {
	return openbao.New(logger)
}
