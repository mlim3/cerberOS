package preprocessor

import (
	"regexp"

	"github.com/mlim3/cerberOS/vault/engine/audit"
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
	store  SecretStore
	logger *audit.Logger
}

func New(store SecretStore, logger *audit.Logger) *Preprocessor {
	return &Preprocessor{store: store, logger: logger}
}

var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Process performs placeholder substitution on the raw script.
// It collects all unique {{KEY}} placeholders, resolves them in a single
// batch call to the SecretStore, then substitutes the values.
// agent is the identifier of the requesting agent and is included in the audit log.
func (p *Preprocessor) Process(agent string, raw []byte) (*Result, error) {
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

	// 3. Emit audit event: agent identity + secret names (never values)
	p.logger.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Agent:   agent,
		Keys:    keys,
		Message: "agent requested secrets",
	})

	// 4. Substitute placeholders
	script := placeholderRe.ReplaceAllFunc(raw, func(match []byte) []byte {
		key := string(placeholderRe.FindSubmatch(match)[1])
		if val, ok := secrets[key]; ok {
			return []byte(val)
		}
		return match
	})

	// 5. Collect resolved values for output scrubbing
	values := make([]string, 0, len(secrets))
	for _, v := range secrets {
		values = append(values, v)
	}

	return &Result{
		Script:          script,
		InjectedSecrets: values,
	}, nil
}
