package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrOpenBaoNotConfigured is returned when OpenBao integration is not available.
var ErrOpenBaoNotConfigured = errors.New("OpenBao integration not configured; set AEGIS_NKEY_SEED or OPENBAO_ADDR")

// GetNKeyFromOpenBao fetches NKey seed from OpenBao (SR-DB-004).
//
// When OPENBAO_ADDR is set: fetches from secret/data/aegis/nkeys/<component>.
// Auth: VAULT_TOKEN or OPENBAO_TOKEN. KV v2 path.
// Fallback: AEGIS_NKEY_SEED env for demo.
func GetNKeyFromOpenBao(ctx context.Context, component string) (string, error) {
	// Fallback: env for demo
	if seed := os.Getenv("AEGIS_NKEY_SEED"); seed != "" {
		return seed, nil
	}
	addr := os.Getenv("OPENBAO_ADDR")
	if addr == "" {
		return "", ErrOpenBaoNotConfigured
	}
	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		token = os.Getenv("OPENBAO_TOKEN")
	}
	if token == "" {
		return "", fmt.Errorf("OPENBAO_ADDR set but VAULT_TOKEN/OPENBAO_TOKEN not set")
	}
	addr = strings.TrimSuffix(addr, "/")
	path := fmt.Sprintf("%s/v1/secret/data/aegis/nkeys/%s", addr, component)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Vault-Token", token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openbao request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openbao status %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Data struct {
				Seed string `json:"seed"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("openbao decode: %w", err)
	}
	seed := out.Data.Data.Seed
	if seed == "" {
		return "", fmt.Errorf("openbao: no seed in secret/aegis/nkeys/%s", component)
	}
	return seed, nil
}
