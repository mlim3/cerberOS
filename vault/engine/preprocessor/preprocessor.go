package preprocessor

import (
	"regexp"
)

// SecretStore resolves a batch of secret keys in a single call.
// The engine's secretclient package provides the HTTP implementation;
// swap in any backend (HashiCorp Vault, AWS KMS, etc.) without touching the pipeline.
type SecretStore interface {
	Resolve(keys []string) (map[string]string, error)
}

// Result holds the output of preprocessing.
type Result struct {
	Script          []byte   // processed script with secrets injected
	InjectedSecrets []string // resolved secret values (for output scrubbing)
}

// Preprocessor handles script transformation before VM execution.
type Preprocessor struct {
	store SecretStore
}

func New(store SecretStore) *Preprocessor {
	return &Preprocessor{store: store}
}

var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Process performs placeholder substitution on the raw script.
// It collects all unique {{KEY}} placeholders, resolves them in a single
// batch call to the SecretStore, then substitutes the values.
func (p *Preprocessor) Process(raw []byte) (*Result, error) {
	// 1. Collect all unique placeholder keys (preserve insertion order)
	seen := make(map[string]struct{})
	var keys []string
	for _, m := range placeholderRe.FindAllSubmatch(raw, -1) {
		key := string(m[1])
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return &Result{Script: raw}, nil
	}

	// 2. Resolve all secrets in one round-trip
	secrets, err := p.store.Resolve(keys)
	if err != nil {
		return nil, err
	}

	// 3. Substitute placeholders
	script := placeholderRe.ReplaceAllFunc(raw, func(match []byte) []byte {
		key := string(placeholderRe.FindSubmatch(match)[1])
		if val, ok := secrets[key]; ok {
			return []byte(val)
		}
		return match
	})

	// 4. Collect resolved values for output scrubbing
	values := make([]string, 0, len(secrets))
	for _, v := range secrets {
		values = append(values, v)
	}

	return &Result{
		Script:          script,
		InjectedSecrets: values,
	}, nil
}
