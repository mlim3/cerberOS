package preprocessor

import (
	"fmt"
	"regexp"
)

// SecretStore provides secrets/env vars for injection.
// Swap MockStore for a real backend (e.g. HashiCorp Vault, KMS) later.
type SecretStore interface {
	Get(key string) (string, error)
}

// MockStore is a simple in-memory secret store for development.
type MockStore struct {
	Secrets map[string]string
}

func (m *MockStore) Get(key string) (string, error) {
	v, ok := m.Secrets[key]
	if !ok {
		return "", fmt.Errorf("secret not found: %s", key)
	}
	return v, nil
}

// Result holds the output of preprocessing.
type Result struct {
	Script          []byte   // processed script with secrets injected
	InjectedSecrets []string // resolved secret values (for output scrubbing)
}

// Preprocessor handles script transformation before VM execution.
//
// Validation hooks (future): add a []Validator field here.
//
//	type Validator interface { Validate(script []byte) error }
//
// Call validators at the start of Process() (pre-substitution: syntax checks,
// disallowed commands) and at the end (post-substitution: no unresolved
// placeholders, script size limits).
type Preprocessor struct {
	store SecretStore
}

func New(store SecretStore) *Preprocessor {
	return &Preprocessor{store: store}
}

var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Process performs placeholder substitution on the raw script.
// Placeholders use the format {{KEY_NAME}}.
func (p *Preprocessor) Process(raw []byte) (*Result, error) {
	seen := make(map[string]string)
	var lastErr error

	script := placeholderRe.ReplaceAllFunc(raw, func(match []byte) []byte {
		key := string(placeholderRe.FindSubmatch(match)[1])
		if val, ok := seen[key]; ok {
			return []byte(val)
		}
		val, err := p.store.Get(key)
		if err != nil {
			lastErr = err
			return match
		}
		seen[key] = val
		return []byte(val)
	})
	if lastErr != nil {
		return nil, lastErr
	}

	secrets := make([]string, 0, len(seen))
	for _, v := range seen {
		secrets = append(secrets, v)
	}

	return &Result{
		Script:          script,
		InjectedSecrets: secrets,
	}, nil
}
