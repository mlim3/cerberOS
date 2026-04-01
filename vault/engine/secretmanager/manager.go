// Package secretmanager defines the SecretManager interface and provides
// pluggable implementations. Swap MockSecretManager for a real backend
// (HashiCorp Vault, AWS Secrets Manager, etc.) without changing callers.
package secretmanager

import "fmt"

// SecretManager resolves a batch of secret keys in a single atomic call.
// If any key is not found or access is denied, the entire call fails —
// no partial results are returned. This guarantees all-or-nothing semantics
// for the inject flow.
type SecretManager interface {
	// Resolve returns values for all requested keys or an error.
	// Callers must not receive a partial map on failure.
	Resolve(keys []string) (map[string]string, error)
}

// MockSecretManager is an in-memory implementation for development.
type MockSecretManager struct {
	secrets map[string]string
}

func NewMock() *MockSecretManager {
	return &MockSecretManager{
		secrets: map[string]string{
			"API_KEY":     "mock-api-key-12345",
			"DB_PASS":     "mock-db-password",
			"SECRET_KEY":  "mock-secret-key",
			"TEST_SECRET": "hello-from-secretstore",
		},
	}
}

// Resolve returns all requested secrets or fails atomically.
// If any single key is missing, no secrets are returned.
func (m *MockSecretManager) Resolve(keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		val, ok := m.secrets[key]
		if !ok {
			return nil, fmt.Errorf("secret not found: %s", key)
		}
		result[key] = val
	}
	return result, nil
}
