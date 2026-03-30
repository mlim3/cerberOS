package secretmanager

import (
	"context"
	"fmt"

	"github.com/mlim3/cerberOS/vault/engine/audit"
)

type MockSecretManager struct {
	secrets map[string]string
	logger  *audit.Logger
}

// NewMockSecretManager returns an in-memory SecretManager with seeded dev keys.
func NewMockSecretManager(logger *audit.Logger) *MockSecretManager {
	return &MockSecretManager{
		secrets: map[string]string{
			"API_KEY":     "mock-api-key-12345",
			"DB_PASS":     "mock-db-password",
			"SECRET_KEY":  "mock-secret-key",
			"TEST_SECRET": "hello-from-secretstore",
		},
		logger: logger,
	}
}

func (m *MockSecretManager) GetSecrets(keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		val, ok := m.secrets[key]
		if !ok {
			return nil, fmt.Errorf("secret %s not found", key)
		}
		result[key] = val
	}
	return result, nil
}

func (m *MockSecretManager) PutSecret(ctx context.Context, key, value string) error {
	if m == nil {
		return fmt.Errorf("mock secret manager not initialized")
	}
	m.secrets[key] = value
	return nil
}

func (m *MockSecretManager) DeleteSecret(ctx context.Context, key string) error {
	if m == nil {
		return fmt.Errorf("mock secret manager not initialized")
	}
	delete(m.secrets, key)
	return nil
}

var _ SecretManager = (*MockSecretManager)(nil)
