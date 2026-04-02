package mock

import "github.com/mlim3/cerberOS/vault/engine/audit"

// MockSecretManager is an in-memory SecretManager for development and tests.
type MockSecretManager struct {
	secrets map[string]string
	logger  *audit.Logger
}
