package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"aegis-databus/pkg/telemetry"
)

const defaultTimeout = 10 * time.Second

// HTTPClient implements MemoryClient by calling the Memory/Storage component REST API.
// BaseURL: http://localhost:8090/v1/memory (set via AEGIS_MEMORY_URL).
// Auth: optional Authorization header from AEGIS_MEMORY_TOKEN.
type HTTPClient struct {
	BaseURL    string
	HTTPClient *http.Client
	AuthToken  string
}

// NewHTTPClient creates an HTTP client for the Memory API.
func NewHTTPClient(baseURL string) *HTTPClient {
	if baseURL == "" {
		baseURL = "http://localhost:8090/v1/memory"
	}
	return &HTTPClient{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout:   defaultTimeout,
			Transport: telemetry.HTTPRoundTripper(http.DefaultTransport),
		},
		AuthToken: os.Getenv("AEGIS_MEMORY_TOKEN"),
	}
}

func (h *HTTPClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	u := h.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+h.AuthToken)
	}
	return h.HTTPClient.Do(req)
}

func (h *HTTPClient) GetStreamConfig(ctx context.Context, name string) (StreamConfig, error) {
	resp, err := h.do(ctx, http.MethodGet, "/streams/"+url.PathEscape(name), nil)
	if err != nil {
		return StreamConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return StreamConfig{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return StreamConfig{}, fmt.Errorf("memory API: %s", resp.Status)
	}
	var cfg StreamConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return StreamConfig{}, err
	}
	return cfg, nil
}

func (h *HTTPClient) ListStreamConfigs(ctx context.Context) ([]StreamConfig, error) {
	resp, err := h.do(ctx, http.MethodGet, "/streams", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("memory API: %s", resp.Status)
	}
	var list []StreamConfig
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

func (h *HTTPClient) UpsertStreamConfig(ctx context.Context, cfg StreamConfig) error {
	body, _ := json.Marshal(cfg)
	resp, err := h.do(ctx, http.MethodPut, "/streams/"+url.PathEscape(cfg.Name), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) DeleteStreamConfig(ctx context.Context, name string) error {
	resp, err := h.do(ctx, http.MethodDelete, "/streams/"+url.PathEscape(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) GetConsumerState(ctx context.Context, stream, consumer string) (ConsumerState, error) {
	path := "/consumers/" + url.PathEscape(stream) + "/" + url.PathEscape(consumer)
	resp, err := h.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ConsumerState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ConsumerState{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ConsumerState{}, fmt.Errorf("memory API: %s", resp.Status)
	}
	var state ConsumerState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return ConsumerState{}, err
	}
	return state, nil
}

func (h *HTTPClient) UpdateConsumerAckSeq(ctx context.Context, stream, consumer string, ackSeq uint64) error {
	path := "/consumers/" + url.PathEscape(stream) + "/" + url.PathEscape(consumer)
	body, _ := json.Marshal(map[string]uint64{"ack_seq": ackSeq})
	resp, err := h.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) InsertOutboxEntry(ctx context.Context, entry OutboxEntry) error {
	body, _ := json.Marshal(entry)
	resp, err := h.do(ctx, http.MethodPost, "/outbox", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) FetchPendingOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	path := fmt.Sprintf("/outbox/pending?limit=%d", limit)
	resp, err := h.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("memory API: %s", resp.Status)
	}
	var list []OutboxEntry
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

func (h *HTTPClient) MarkOutboxSent(ctx context.Context, id string, sequence uint64) error {
	body, _ := json.Marshal(map[string]uint64{"sequence": sequence})
	resp, err := h.do(ctx, http.MethodPut, "/outbox/"+url.PathEscape(id)+"/sent", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) AppendAuditLog(ctx context.Context, entry AuditLogEntry) error {
	body, _ := json.Marshal(entry)
	resp, err := h.do(ctx, http.MethodPost, "/audit", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) ListAuditLogs(ctx context.Context, limit int) ([]AuditLogEntry, error) {
	path := fmt.Sprintf("/audit?limit=%d", limit)
	resp, err := h.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("memory API: %s", resp.Status)
	}
	var list []AuditLogEntry
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

func (h *HTTPClient) GetNKey(ctx context.Context, component string) (string, error) {
	resp, err := h.do(ctx, http.MethodGet, "/nkeys/"+url.PathEscape(component), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("memory API: %s", resp.Status)
	}
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return "", err
	}
	seed, ok := m["seed"]
	if !ok {
		return "", ErrNotFound
	}
	return seed, nil
}

// WasProcessed implements IdempotencyChecker via GET /processed/{id} (200 = processed).
func (h *HTTPClient) WasProcessed(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	resp, err := h.do(ctx, http.MethodGet, "/processed/"+url.PathEscape(messageID), nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("memory API: %s", resp.Status)
	}
	return true, nil
}

// RecordProcessed implements IdempotencyChecker via PUT /processed/{id}.
func (h *HTTPClient) RecordProcessed(ctx context.Context, messageID string) error {
	if messageID == "" {
		return nil
	}
	resp, err := h.do(ctx, http.MethodPut, "/processed/"+url.PathEscape(messageID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}

func (h *HTTPClient) Ping(ctx context.Context) error {
	resp, err := h.do(ctx, http.MethodGet, "/ping", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("memory API: %s", resp.Status)
	}
	return nil
}
