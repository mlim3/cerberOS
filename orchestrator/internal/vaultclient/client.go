// Package vaultclient implements interfaces.VaultClient against the vault engine
// HTTP API (credential broker at VAULT_ENGINE_URL).
//
// ValidateAndScope: checks which of the user's credential types are registered
// in the vault, populating PolicyScope.AvailableCredTypes. Domain policy is
// permissive for now — if the vault engine is reachable, the task is allowed.
//
// Execute: forwards vault.execute.request payloads to POST /execute on the vault
// engine. The orchestrator adds user_id from trusted task state before forwarding.
package vaultclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// domainCredTypes maps each skill domain to the credential types it requires.
// Mirrors the mapping in mocks/vault_mock.go — keep them in sync.
var domainCredTypes = map[string][]string{
	"web":           {"web_api_key", "search_api_key"},
	"data":          {"data_read_key", "data_write_key"},
	"comms":         {"comms_api_key"},
	"storage":       {"storage_credential"},
	"google_search": {"serper_api_key"},
	"github":        {"github_token"},
}

// Client is the real implementation of interfaces.VaultClient.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New returns a Client targeting the vault engine at baseURL (e.g. "http://vault:8000").
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 35 * time.Second,
		},
	}
}

// ValidateAndScope checks which credential types the user has registered in the
// vault for ALL known domains (not just requested ones), and returns a PolicyScope.
// scope.Domains is expanded to include any domain the user has credentials for,
// so the planner can use credentialed skills even when required_skill_domains only
// lists "general". Domain policy is permissive: if the vault engine is reachable,
// the task is allowed.
func (c *Client) ValidateAndScope(userID string, requiredSkillDomains []string, timeoutSeconds int) (types.PolicyScope, error) {
	if err := c.HealthCheck(); err != nil {
		return types.PolicyScope{}, fmt.Errorf("vault engine unreachable: %w", err)
	}

	// Scan ALL known domains to find every credential the user has registered.
	seen := map[string]bool{}
	var available []string
	for _, credTypes := range domainCredTypes {
		for _, credType := range credTypes {
			if seen[credType] {
				continue
			}
			seen[credType] = true
			if c.hasCredential(userID, credType) {
				available = append(available, credType)
			}
		}
	}

	// Build allowed domains: always "general" + requested + any domain whose
	// credential the user actually has in the vault.
	domainsSet := map[string]bool{"general": true}
	for _, d := range requiredSkillDomains {
		domainsSet[d] = true
	}
	for domain, credTypes := range domainCredTypes {
		for _, ct := range credTypes {
			for _, avail := range available {
				if ct == avail {
					domainsSet[domain] = true
					break
				}
			}
		}
	}
	allowedDomains := make([]string, 0, len(domainsSet))
	for d := range domainsSet {
		allowedDomains = append(allowedDomains, d)
	}

	now := time.Now()
	return types.PolicyScope{
		Domains:            allowedDomains,
		TokenRef:           fmt.Sprintf("vault-engine-%s-%d", userID, now.UnixNano()),
		IssuedAt:           now,
		ExpiresAt:          now.Add(time.Duration(timeoutSeconds+300) * time.Second),
		AvailableCredTypes: available,
		Metadata:           map[string]string{"user_id": userID},
	}, nil
}

// hasCredential returns true if the user has the given credential type registered.
func (c *Client) hasCredential(userID, credentialType string) bool {
	body, _ := json.Marshal(map[string]string{
		"user_id":         userID,
		"credential_type": credentialType,
	})
	resp, err := c.httpClient.Post(c.baseURL+"/credentials/get", "application/json", bytes.NewReader(body))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Execute forwards a vault.execute.request to the vault engine's POST /execute.
// userID is supplied by the Orchestrator from trusted task state.
func (c *Client) Execute(ctx context.Context, userID string, req types.VaultExecuteRequest) (types.VaultExecuteResult, error) {
	var params map[string]any
	if len(req.OperationParams) > 0 {
		_ = json.Unmarshal(req.OperationParams, &params)
	}

	payload := map[string]any{
		"request_id":       req.RequestID,
		"agent_id":         req.AgentID,
		"task_id":          req.TaskID,
		"user_id":          userID,
		"permission_token": req.PermissionToken,
		"operation_type":   req.OperationType,
		"operation_params": params,
		"credential_type":  req.CredentialType,
		"timeout_seconds":  req.TimeoutSeconds,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return types.VaultExecuteResult{}, fmt.Errorf("marshal execute request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/execute", bytes.NewReader(body))
	if err != nil {
		return types.VaultExecuteResult{}, fmt.Errorf("build execute request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return types.VaultExecuteResult{}, fmt.Errorf("vault execute: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return types.VaultExecuteResult{}, fmt.Errorf("read execute response: %w", err)
	}

	var result types.VaultExecuteResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return types.VaultExecuteResult{}, fmt.Errorf("parse execute response: %w", err)
	}

	return result, nil
}

func (c *Client) VerifyScopeStillValid(scope types.PolicyScope) error {
	if time.Now().After(scope.ExpiresAt) {
		return fmt.Errorf("scope expired")
	}
	return nil
}

func (c *Client) RevokeCredentials(_ string) error {
	// TODO: implement credential revocation via vault engine
	return nil
}

func (c *Client) HealthCheck() error {
	resp, err := c.httpClient.Get(c.baseURL + "/healthz")
	if err != nil {
		return fmt.Errorf("vault engine health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault engine unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}
