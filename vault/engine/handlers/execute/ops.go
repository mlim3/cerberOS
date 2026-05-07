package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// opResult is returned by each operation executor.
type opResult struct {
	result map[string]any
	err    error
	code   string // error code on failure
}

// dispatchOperation routes to the correct executor based on operationType.
// credential is the resolved raw API key/token — never logged or returned to callers.
func dispatchOperation(ctx context.Context, operationType, credential string, params map[string]any) opResult {
	switch operationType {
	case "vault_google_search":
		return execGoogleSearch(ctx, credential, params)
	case "vault_github_request":
		return execGitHubRequest(ctx, credential, params)
	case "vault_web_fetch":
		return execWebFetch(ctx, credential, params)
	case "vault_data_read":
		return opResult{err: fmt.Errorf("vault_data_read not yet implemented"), code: ErrCodeUnsupportedOp}
	case "vault_data_write":
		return opResult{err: fmt.Errorf("vault_data_write not yet implemented"), code: ErrCodeUnsupportedOp}
	case "vault_comms_send":
		return opResult{err: fmt.Errorf("vault_comms_send not yet implemented"), code: ErrCodeUnsupportedOp}
	case "vault_storage_read", "vault_storage_write", "vault_storage_list":
		return opResult{err: fmt.Errorf("%s not yet implemented", operationType), code: ErrCodeUnsupportedOp}
	default:
		return opResult{err: fmt.Errorf("unknown operation type: %s", operationType), code: ErrCodeUnsupportedOp}
	}
}

// execGoogleSearch calls the Serper Google Search API.
// credential is the Serper API key. params must include "query"; "num_results" is optional (default 5, max 10).
func execGoogleSearch(ctx context.Context, credential string, params map[string]any) opResult {
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return opResult{err: fmt.Errorf("query parameter is required"), code: ErrCodeInvalidParams}
	}

	numResults := 5
	if nr, ok := params["num_results"]; ok {
		switch v := nr.(type) {
		case float64:
			numResults = int(v)
		case int:
			numResults = v
		}
		if numResults < 1 {
			numResults = 1
		}
		if numResults > 10 {
			numResults = 10
		}
	}

	reqBody, _ := json.Marshal(map[string]any{"q": query, "num": numResults})
	headers := map[string]string{
		"X-API-KEY":    credential,
		"Content-Type": "application/json",
	}

	body, statusCode, err := httpRequest(ctx, "POST", "https://google.serper.dev/search", headers, strings.NewReader(string(reqBody)))
	if err != nil {
		return opResult{err: fmt.Errorf("google search request failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("google search returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return opResult{err: fmt.Errorf("failed to parse google search response"), code: ErrCodeUpstreamError}
	}

	// Extract only titles, URLs, and snippets from organic results.
	items, _ := raw["organic"].([]any)
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			results = append(results, map[string]any{
				"title":   m["title"],
				"link":    m["link"],
				"snippet": m["snippet"],
			})
		}
	}

	return opResult{result: map[string]any{"results": results, "total_results": len(results)}}
}

// execGitHubRequest makes an authenticated GitHub REST API request.
// credential is the GitHub personal access token. params must include "path".
func execGitHubRequest(ctx context.Context, credential string, params map[string]any) opResult {
	path, ok := params["path"].(string)
	if !ok || path == "" {
		return opResult{err: fmt.Errorf("path parameter is required"), code: ErrCodeInvalidParams}
	}
	if !strings.HasPrefix(path, "/") {
		return opResult{err: fmt.Errorf("path must start with /"), code: ErrCodeInvalidParams}
	}

	method := "GET"
	if m, ok := params["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	var bodyReader io.Reader
	if bodyStr, ok := params["body"].(string); ok && bodyStr != "" {
		bodyReader = strings.NewReader(bodyStr)
	}

	apiURL := "https://api.github.com" + path
	headers := map[string]string{
		"Authorization":        "Bearer " + credential,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}

	body, statusCode, err := httpRequest(ctx, method, apiURL, headers, bodyReader)
	if err != nil {
		return opResult{err: fmt.Errorf("github request failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("github returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		// Non-JSON response (e.g. 204 No Content) — return status only.
		return opResult{result: map[string]any{"status_code": statusCode}}
	}

	return opResult{result: map[string]any{"status_code": statusCode, "body": result}}
}

// execWebFetch performs an authenticated HTTP GET or POST using an API key injected
// as an Authorization Bearer header.
func execWebFetch(ctx context.Context, credential string, params map[string]any) opResult {
	rawURL, ok := params["url"].(string)
	if !ok || rawURL == "" {
		return opResult{err: fmt.Errorf("url parameter is required"), code: ErrCodeInvalidParams}
	}

	method := "GET"
	if m, ok := params["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	headers := map[string]string{
		"Authorization": "Bearer " + credential,
	}

	body, statusCode, err := httpRequest(ctx, method, rawURL, headers, nil)
	if err != nil {
		return opResult{err: fmt.Errorf("web fetch failed: %w", err), code: ErrCodeUpstreamError}
	}
	if statusCode >= 400 {
		return opResult{err: fmt.Errorf("upstream returned HTTP %d", statusCode), code: ErrCodeUpstreamError}
	}

	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		return opResult{result: map[string]any{"status_code": statusCode, "body": parsed}}
	}
	return opResult{result: map[string]any{"status_code": statusCode, "body": string(body)}}
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func httpGET(ctx context.Context, rawURL string, headers map[string]string) ([]byte, int, error) {
	return httpRequest(ctx, http.MethodGet, rawURL, headers, nil)
}

func httpRequest(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	// Cap response at 1 MB to prevent unbounded memory use.
	const maxBody = 1 << 20
	limited := io.LimitReader(resp.Body, maxBody)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	return respBody, resp.StatusCode, nil
}
