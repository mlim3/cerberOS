package main

import "fmt"

// SecretManager is the interface the backend team implements against their
// actual secret manager (HashiCorp Vault, AWS Secrets Manager, GCP Secret
// Manager, etc.). Swap MockSecretManager for a real implementation here
// without changing anything else in the service.
type SecretManager interface {
	GetSecrets(keys []string) (map[string]string, error)
}

// MockSecretManager is an in-memory implementation used during development.
// Replace with a real SecretManager implementation when integrating the
// actual backend.
type MockSecretManager struct {
	secrets map[string]string
}

func NewMockSecretManager() *MockSecretManager {
	return &MockSecretManager{
		secrets: map[string]string{
			"API_KEY":     "mock-api-key-12345",
			"DB_PASS":     "mock-db-password",
			"SECRET_KEY":  "mock-secret-key",
			"TEST_SECRET": "hello-from-secretstore",
		},
	}
}

func (m *MockSecretManager) GetSecrets(keys []string) (map[string]string, error) {
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
