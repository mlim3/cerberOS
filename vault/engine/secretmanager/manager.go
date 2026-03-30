// Package secretmanager defines [SecretManager] and small helpers to construct
// implementations.
//
// Wire explicitly:
//
//	secretmanager.New(auditor, secretmanager.NewMockSecretManager)
//	secretmanager.New(auditor, secretmanager.NewOpenBaoSecretManager) // nil → no client
package secretmanager

import (
	"context"
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
