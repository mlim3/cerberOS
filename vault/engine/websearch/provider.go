package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	tavilyEndpoint    = "https://api.tavily.com/search"
	defaultMaxResults = 5
	maxAllowedResults = 20
	defaultTimeout    = 30 * time.Second
	maxResponseBytes  = 1 * 1024 * 1024 // 1 MB — search results should never exceed this
)

// SearchProvider executes web search operations.
type SearchProvider interface {
	Search(ctx context.Context, apiKey string, params SearchParams) (*SearchResult, error)
}

// TavilyProvider calls the Tavily AI Search API. The API key is accepted per
// call so the provider itself holds no credential state — the vault engine
// resolves the key from SecretManager and passes it in at call time.
type TavilyProvider struct {
	httpClient *http.Client
	endpoint   string // overridable for tests
}

// NewTavilyProvider returns a TavilyProvider with the given HTTP client
// timeout. Pass 0 to use the default (30s).
func NewTavilyProvider(timeout time.Duration) *TavilyProvider {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &TavilyProvider{
		httpClient: &http.Client{Timeout: timeout},
		endpoint:   tavilyEndpoint,
	}
}

// withEndpoint returns a copy of the provider pointed at a custom endpoint.
// Used in tests to redirect calls to an httptest.Server.
func (p *TavilyProvider) withEndpoint(endpoint string) *TavilyProvider {
	return &TavilyProvider{httpClient: p.httpClient, endpoint: endpoint}
}

// Search calls the Tavily API and returns ranked search results. The apiKey
// is injected into the request body and must not appear in any returned value,
// log, or error message.
func (p *TavilyProvider) Search(ctx context.Context, apiKey string, params SearchParams) (*SearchResult, error) {
	if params.Query == "" {
		return nil, fmt.Errorf("websearch: query must not be empty")
	}

	maxResults := params.MaxResults
	if maxResults <= 0 {
		maxResults = defaultMaxResults
	}
	if maxResults > maxAllowedResults {
		maxResults = maxAllowedResults
	}

	reqBody := tavilyRequest{
		APIKey:         apiKey,
		Query:          params.Query,
		MaxResults:     maxResults,
		IncludeDomains: params.IncludeDomains,
		ExcludeDomains: params.ExcludeDomains,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("websearch: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("websearch: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("websearch: read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("websearch: search API authentication failed (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("websearch: search API returned status %d", resp.StatusCode)
	}

	var tavilyResp tavilyResponse
	if err := json.Unmarshal(respBytes, &tavilyResp); err != nil {
		return nil, fmt.Errorf("websearch: decode response: %w", err)
	}
	if tavilyResp.Error != "" {
		return nil, fmt.Errorf("websearch: search API error: %s", tavilyResp.Error)
	}

	result := &SearchResult{
		Query:   tavilyResp.Query,
		Results: make([]SearchResultItem, 0, len(tavilyResp.Results)),
	}
	for _, r := range tavilyResp.Results {
		result.Results = append(result.Results, SearchResultItem{
			Title:   r.Title,
			URL:     r.URL,
			Content: r.Content,
			Score:   r.Score,
		})
	}
	return result, nil
}
