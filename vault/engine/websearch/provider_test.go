package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockTavilyServer starts an httptest.Server that responds with the given
// tavilyResponse payload and HTTP status code.
func mockTavilyServer(t *testing.T, statusCode int, body tavilyResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type: application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Verify the API key is present in the request body but not echoed back.
		var req tavilyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.APIKey == "" {
			t.Error("expected non-empty api_key in request body")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func newTestProvider(t *testing.T, srv *httptest.Server) *TavilyProvider {
	t.Helper()
	p := NewTavilyProvider(5 * time.Second)
	return p.withEndpoint(srv.URL)
}

func TestSearch_Success(t *testing.T) {
	srv := mockTavilyServer(t, http.StatusOK, tavilyResponse{
		Query: "golang testing",
		Results: []tavilyResultItem{
			{Title: "Go Testing Guide", URL: "https://example.com/go-test", Content: "How to write tests in Go.", Score: 0.95},
			{Title: "Table-Driven Tests", URL: "https://example.com/table", Content: "Best practices.", Score: 0.88},
		},
	})
	defer srv.Close()

	p := newTestProvider(t, srv)
	result, err := p.Search(context.Background(), "test-api-key", SearchParams{Query: "golang testing", MaxResults: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Query != "golang testing" {
		t.Errorf("expected query 'golang testing', got %q", result.Query)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	if result.Results[0].Title != "Go Testing Guide" {
		t.Errorf("unexpected first result title: %q", result.Results[0].Title)
	}
	if result.Results[0].Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", result.Results[0].Score)
	}
}

func TestSearch_APIKeyNotEchoedInResult(t *testing.T) {
	srv := mockTavilyServer(t, http.StatusOK, tavilyResponse{
		Query:   "secret query",
		Results: []tavilyResultItem{{Title: "Result", URL: "https://x.com", Content: "c", Score: 0.5}},
	})
	defer srv.Close()

	p := newTestProvider(t, srv)
	result, err := p.Search(context.Background(), "super-secret-api-key", SearchParams{Query: "secret query"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Serialise and scan for the API key — it must not appear anywhere in the output.
	b, _ := json.Marshal(result)
	if contains(string(b), "super-secret-api-key") {
		t.Error("API key leaked into search result JSON")
	}
}

func TestSearch_EmptyQuery_Error(t *testing.T) {
	p := NewTavilyProvider(5 * time.Second)
	_, err := p.Search(context.Background(), "key", SearchParams{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestSearch_MaxResultsClamped(t *testing.T) {
	var capturedMaxResults int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tavilyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		capturedMaxResults = req.MaxResults
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tavilyResponse{Query: "q"})
	}))
	defer srv.Close()

	p := newTestProvider(t, srv)
	_, _ = p.Search(context.Background(), "key", SearchParams{Query: "q", MaxResults: 999})
	if capturedMaxResults != maxAllowedResults {
		t.Errorf("expected max_results clamped to %d, got %d", maxAllowedResults, capturedMaxResults)
	}
}

func TestSearch_DefaultMaxResults(t *testing.T) {
	var capturedMaxResults int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tavilyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		capturedMaxResults = req.MaxResults
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tavilyResponse{Query: "q"})
	}))
	defer srv.Close()

	p := newTestProvider(t, srv)
	_, _ = p.Search(context.Background(), "key", SearchParams{Query: "q", MaxResults: 0})
	if capturedMaxResults != defaultMaxResults {
		t.Errorf("expected default max_results %d, got %d", defaultMaxResults, capturedMaxResults)
	}
}

func TestSearch_IncludeExcludeDomains_Forwarded(t *testing.T) {
	var captured tavilyRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tavilyResponse{Query: "q"})
	}))
	defer srv.Close()

	p := newTestProvider(t, srv)
	_, _ = p.Search(context.Background(), "key", SearchParams{
		Query:          "q",
		IncludeDomains: []string{"docs.go.dev"},
		ExcludeDomains: []string{"reddit.com"},
	})
	if len(captured.IncludeDomains) != 1 || captured.IncludeDomains[0] != "docs.go.dev" {
		t.Errorf("include_domains not forwarded correctly: %v", captured.IncludeDomains)
	}
	if len(captured.ExcludeDomains) != 1 || captured.ExcludeDomains[0] != "reddit.com" {
		t.Errorf("exclude_domains not forwarded correctly: %v", captured.ExcludeDomains)
	}
}

func TestSearch_AuthFailure_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := newTestProvider(t, srv)
	_, err := p.Search(context.Background(), "bad-key", SearchParams{Query: "q"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestSearch_NonOKStatus_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProvider(t, srv)
	_, err := p.Search(context.Background(), "key", SearchParams{Query: "q"})
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestSearch_TavilyErrorField_ReturnsError(t *testing.T) {
	srv := mockTavilyServer(t, http.StatusOK, tavilyResponse{Error: "Invalid API key"})
	defer srv.Close()

	p := newTestProvider(t, srv)
	_, err := p.Search(context.Background(), "bad-key", SearchParams{Query: "q"})
	if err == nil {
		t.Fatal("expected error when Tavily returns error field")
	}
}

func TestSearch_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response — selects so cleanup can force-close connections.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	// CloseClientConnections before Close so the handler goroutine unblocks.
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	p := newTestProvider(t, srv)
	_, err := p.Search(ctx, "key", SearchParams{Query: "q"})
	if err == nil {
		t.Fatal("expected error on context timeout")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
