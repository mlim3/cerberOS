package secretmanager

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/openbao/openbao/api/v2"
)

// KvMount is the KV v2 mount path (see vault/setup-openbao.sh: sys/mounts/kv).
const KvMount = "kv"

type OpenBaoSecretManager struct {
	client *api.Client
	logger *audit.Logger
}

// BAO_ADDR (e.g. http://openbao:8200 from Docker, or http://127.0.0.1:8200 on the host),
// BAO_TOKEN, TLS vars, etc. Returns nil if the client cannot be created.
func NewOpenBaoSecretManager(logger *audit.Logger) SecretManager {
	cfg := api.DefaultConfig()
	if err := cfg.ReadEnvironment(); err != nil {
		logger.Log(audit.Event{
			Kind:    audit.KindError,
			Message: "openbao: read environment for api client",
			Error:   err.Error(),
		})
		return nil
	}
	// DefaultConfig uses https; local OpenBao (openbao.hcl) has TLS off — prefer http when BAO_ADDR is unset.
	if os.Getenv("BAO_ADDR") == "" {
		cfg.Address = "http://127.0.0.1:8200"
	}
	client, err := api.NewClient(cfg)
	if err != nil {
		logger.Log(audit.Event{
			Kind:    audit.KindError,
			Message: "failed to create openbao client",
			Error:   err.Error(),
		})
		return nil
	}
	return &OpenBaoSecretManager{client: client, logger: logger}
}

// Resolve reads KV v2 secrets at kv/data/<key>. Each secret's payload should include
// a "value" field, or a field named like the key, or a single key-value pair.
func (m *OpenBaoSecretManager) GetSecrets(keys []string) (map[string]string, error) {
	if m == nil || m.client == nil {
		return nil, fmt.Errorf("openbao secret manager not initialized")
	}
	ctx := context.Background()
	kv := m.client.KVv2(KvMount)
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		sec, err := kv.Get(ctx, key)
		if err != nil {
			if errors.Is(err, api.ErrSecretNotFound) {
				return nil, fmt.Errorf("secret not found: %s", key)
			}
			return nil, err
		}
		val, err := stringFromKVData(sec, key)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", key, err)
		}
		out[key] = val
	}
	return out, nil
}

// PutSecret writes KV v2 data at kv/data/<key> with data["value"] = value (CLI: bao kv put kv/<key> value=...).
func (m *OpenBaoSecretManager) PutSecret(ctx context.Context, key, value string) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("openbao secret manager not initialized")
	}
	_, err := m.client.KVv2(KvMount).Put(ctx, key, map[string]any{"value": value})
	return err
}

// DeleteSecret soft-deletes the latest version at kv/data/<key>. For full metadata removal, use the API separately.
func (m *OpenBaoSecretManager) DeleteSecret(ctx context.Context, key string) error {
	if m == nil || m.client == nil {
		return fmt.Errorf("openbao secret manager not initialized")
	}
	return m.client.KVv2(KvMount).Delete(ctx, key)
}

func stringFromKVData(sec *api.KVSecret, key string) (string, error) {
	if sec == nil || len(sec.Data) == 0 {
		return "", fmt.Errorf("empty secret payload")
	}
	if v, ok := sec.Data["value"]; ok {
		return stringifyAny(v), nil
	}
	if v, ok := sec.Data[key]; ok {
		return stringifyAny(v), nil
	}
	if len(sec.Data) == 1 {
		for _, v := range sec.Data {
			return stringifyAny(v), nil
		}
	}
	return "", fmt.Errorf("expected a %q field, \"value\", or a single data field", key)
}

func stringifyAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

var _ SecretManager = (*OpenBaoSecretManager)(nil)
