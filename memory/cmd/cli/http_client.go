package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// httpClient implements MemoryClient using REST API calls
type httpClient struct {
	baseURL string
	client  *http.Client
}

// NewHTTPClient initializes an HTTP client for the memory service
func NewHTTPClient(baseURL string) (MemoryClient, error) {
	return &httpClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

func (c *httpClient) QueryFacts(ctx context.Context, userID uuid.UUID, query string, topK int) ([]Fact, error) {
	url := fmt.Sprintf("%s/api/v1/personal_info/%s/query", c.baseURL, userID.String())

	reqBody := map[string]interface{}{
		"query": query,
		"topK":  topK,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp struct {
		Data struct {
			Results []struct {
				ChunkID string `json:"chunkId"`
				Text    string `json:"text"`
			} `json:"results"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var facts []Fact
	for _, r := range apiResp.Data.Results {
		id, _ := uuid.Parse(r.ChunkID)
		facts = append(facts, Fact{
			ID:      id,
			Content: r.Text,
		})
	}
	if facts == nil {
		facts = []Fact{}
	}
	return facts, nil
}

func (c *httpClient) GetAllFacts(ctx context.Context, userID uuid.UUID) ([]Fact, error) {
	url := fmt.Sprintf("%s/api/v1/personal_info/%s/all", c.baseURL, userID.String())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp struct {
		Data struct {
			Facts []struct {
				FactID    string          `json:"factId"`
				FactValue json.RawMessage `json:"factValue"`
			} `json:"facts"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var facts []Fact
	for _, f := range apiResp.Data.Facts {
		id, _ := uuid.Parse(f.FactID)
		facts = append(facts, Fact{
			ID:      id,
			Content: decodeFactContent(f.FactValue),
		})
	}
	if facts == nil {
		facts = []Fact{}
	}
	return facts, nil
}

func (c *httpClient) SaveFact(ctx context.Context, userID uuid.UUID, fact string) error {
	url := fmt.Sprintf("%s/api/v1/personal_info/%s/save", c.baseURL, userID.String())

	reqBody := map[string]interface{}{
		"content":      fact,
		"sourceType":   "chat",
		"sourceId":     uuid.New().String(),
		"extractFacts": true,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

func (c *httpClient) GetChatHistory(ctx context.Context, sessionID uuid.UUID, limit int) ([]Message, error) {
	url := fmt.Sprintf("%s/api/v1/chat/%s/messages?limit=%d", c.baseURL, sessionID.String(), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp struct {
		Data struct {
			Messages []struct {
				ID        string `json:"id"`
				Role      string `json:"role"`
				Content   string `json:"content"`
				CreatedAt string `json:"created_at"`
			} `json:"messages"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var messages []Message
	for _, m := range apiResp.Data.Messages {
		id, _ := uuid.Parse(m.ID)
		messages = append(messages, Message{
			ID:        id,
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt,
		})
	}
	if messages == nil {
		messages = []Message{}
	}

	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages, nil
}

func (c *httpClient) GetAgentExecutions(ctx context.Context, taskID uuid.UUID, limit int) ([]AgentExecution, error) {
	url := fmt.Sprintf("%s/api/v1/agent/%s/executions?limit=%d", c.baseURL, taskID.String(), limit)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp struct {
		Data struct {
			Executions []struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				CreatedAt string `json:"created_at"`
			} `json:"executions"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var execs []AgentExecution
	for _, e := range apiResp.Data.Executions {
		id, _ := uuid.Parse(e.ID)
		execs = append(execs, AgentExecution{
			ID:        id,
			TaskID:    taskID,
			Status:    e.Status,
			CreatedAt: e.CreatedAt,
		})
	}
	if execs == nil {
		execs = []AgentExecution{}
	}
	return execs, nil
}

func (c *httpClient) GetSystemEvents(ctx context.Context, limit int) ([]SystemEvent, error) {
	url := fmt.Sprintf("%s/api/v1/system/events?limit=%d", c.baseURL, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp struct {
		Data struct {
			Events []struct {
				EventID   string `json:"eventId"`
				Severity  string `json:"severity"`
				Message   string `json:"message"`
				CreatedAt string `json:"createdAt"`
			} `json:"events"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var events []SystemEvent
	for _, e := range apiResp.Data.Events {
		id, _ := uuid.Parse(e.EventID)
		events = append(events, SystemEvent{
			ID:        id,
			EventType: e.Severity,
			Message:   e.Message,
			CreatedAt: e.CreatedAt,
		})
	}
	if events == nil {
		events = []SystemEvent{}
	}
	return events, nil
}

func (c *httpClient) ListVaultSecrets(ctx context.Context, userID uuid.UUID) ([]VaultSecret, error) {
	url := fmt.Sprintf("%s/api/v1/vault/%s/secrets", c.baseURL, userID.String())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// This is typically not supported if there is no "List" endpoint for vault
	// Looking at the handlers, there is no HandleListSecrets. The Vault API currently just handles GetSecret by keyName.
	// We'll return an error here to state it's not supported via HTTP.
	return nil, fmt.Errorf("listing vault secrets is not supported by the API")
}

func (c *httpClient) Close() error {
	// Nothing to close for standard HTTP client
	return nil
}
