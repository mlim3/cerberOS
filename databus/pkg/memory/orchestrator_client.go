package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aegis-databus/pkg/telemetry"
)

// OrchestratorStorageClient implements MemoryClient by calling Orchestrator's proxy API.
// DataBus → Orchestrator → Memory. Use AEGIS_ORCHESTRATOR_URL.
// Proxy paths: /v1/databus/outbox/*, /v1/databus/audit, /v1/databus/ping
type OrchestratorStorageClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewOrchestratorStorageClient creates a client for Orchestrator's storage proxy.
func NewOrchestratorStorageClient(baseURL string) *OrchestratorStorageClient {
	if baseURL == "" {
		baseURL = "http://localhost:8091"
	}
	return &OrchestratorStorageClient{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: telemetry.HTTPRoundTripper(http.DefaultTransport),
		},
	}
}

func (c *OrchestratorStorageClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	u := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.HTTPClient.Do(req)
}

func (c *OrchestratorStorageClient) GetStreamConfig(ctx context.Context, name string) (StreamConfig, error) {
	return StreamConfig{}, ErrNotFound
}
func (c *OrchestratorStorageClient) ListStreamConfigs(ctx context.Context) ([]StreamConfig, error) {
	return nil, nil
}
func (c *OrchestratorStorageClient) UpsertStreamConfig(ctx context.Context, cfg StreamConfig) error {
	return ErrNotFound
}
func (c *OrchestratorStorageClient) DeleteStreamConfig(ctx context.Context, name string) error {
	return ErrNotFound
}
func (c *OrchestratorStorageClient) GetConsumerState(ctx context.Context, stream, consumer string) (ConsumerState, error) {
	return ConsumerState{}, ErrNotFound
}
func (c *OrchestratorStorageClient) UpdateConsumerAckSeq(ctx context.Context, stream, consumer string, ackSeq uint64) error {
	return ErrNotFound
}
func (c *OrchestratorStorageClient) GetNKey(ctx context.Context, component string) (string, error) {
	return "", ErrNotFound
}

func (c *OrchestratorStorageClient) InsertOutboxEntry(ctx context.Context, entry OutboxEntry) error {
	body, _ := json.Marshal(entry)
	resp, err := c.do(ctx, http.MethodPost, "/v1/databus/outbox", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	return nil
}

func (c *OrchestratorStorageClient) FetchPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	path := fmt.Sprintf("/v1/databus/outbox/pending?limit=%d", limit)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	var list []OutboxEntry
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

func (c *OrchestratorStorageClient) MarkOutboxSent(ctx context.Context, id string, sequence uint64) error {
	body, _ := json.Marshal(map[string]uint64{"sequence": sequence})
	resp, err := c.do(ctx, http.MethodPut, "/v1/databus/outbox/"+url.PathEscape(id)+"/sent", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	return nil
}

func (c *OrchestratorStorageClient) AppendAuditLog(ctx context.Context, entry AuditLogEntry) error {
	body, _ := json.Marshal(entry)
	resp, err := c.do(ctx, http.MethodPost, "/v1/databus/audit", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	return nil
}

func (c *OrchestratorStorageClient) ListAuditLogs(ctx context.Context, limit int) ([]AuditLogEntry, error) {
	path := fmt.Sprintf("/v1/databus/audit?limit=%d", limit)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	var list []AuditLogEntry
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// WasProcessed implements IdempotencyChecker (proxied to Memory).
func (c *OrchestratorStorageClient) WasProcessed(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	resp, err := c.do(ctx, http.MethodGet, "/v1/databus/processed/"+url.PathEscape(messageID), nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	return true, nil
}

// RecordProcessed implements IdempotencyChecker (proxied to Memory).
func (c *OrchestratorStorageClient) RecordProcessed(ctx context.Context, messageID string) error {
	if messageID == "" {
		return nil
	}
	resp, err := c.do(ctx, http.MethodPut, "/v1/databus/processed/"+url.PathEscape(messageID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	return nil
}

func (c *OrchestratorStorageClient) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/v1/databus/ping", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("orchestrator proxy: %s", resp.Status)
	}
	return nil
}
