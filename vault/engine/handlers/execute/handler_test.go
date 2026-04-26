package execute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/execute"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
	"github.com/mlim3/cerberOS/vault/engine/websearch"
)

// --- test doubles ---

// stubSearchProvider is an injectable mock of websearch.SearchProvider.
type stubSearchProvider struct {
	result *websearch.SearchResult
	err    error
}

func (s *stubSearchProvider) Search(_ context.Context, apiKey string, params websearch.SearchParams) (*websearch.SearchResult, error) {
	if apiKey == "" {
		panic("stubSearchProvider: apiKey must not be empty")
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &websearch.SearchResult{Query: params.Query, Results: []websearch.SearchResultItem{}}, nil
}

// newTestHandler wires a handler with a MockSecretManager pre-seeded with
// TAVILY_API_KEY and a stub search provider.
func newTestHandler(t *testing.T, searcher websearch.SearchProvider) (*execute.Handler, *audit.Logger) {
	t.Helper()
	auditor := audit.New(audit.NewJSONExporter(bytes.NewBuffer(nil)))
	mgr := secretmanager.NewMockSecretManager(auditor)
	_ = mgr.PutSecret(context.Background(), execute.SecretKeyTavily, "test-tavily-key")
	return execute.NewWithSearcher(mgr, auditor, searcher), auditor
}

func doRequest(t *testing.T, h *execute.Handler, body OperationRequestFixture) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Execute(rr, req)
	return rr
}

func decodeResult(t *testing.T, rr *httptest.ResponseRecorder) execute.OperationResult {
	t.Helper()
	var result execute.OperationResult
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// OperationRequestFixture is a local helper so tests can construct requests
// without importing the types package.
type OperationRequestFixture = execute.OperationRequest

// --- tests ---

func TestExecute_WebSearch_Success(t *testing.T) {
	stub := &stubSearchProvider{result: &websearch.SearchResult{
		Query: "golang concurrency",
		Results: []websearch.SearchResultItem{
			{Title: "Go Concurrency Patterns", URL: "https://go.dev/blog/pipelines", Content: "Pipelines and cancellation.", Score: 0.97},
		},
	}}
	h, _ := newTestHandler(t, stub)

	params, _ := json.Marshal(websearch.SearchParams{Query: "golang concurrency", MaxResults: 3})
	rr := doRequest(t, h, execute.OperationRequest{
		RequestID:       "req-1",
		AgentID:         "agent-1",
		TaskID:          "task-1",
		PermissionToken: "tok-abc",
		OperationType:   execute.OperationTypeWebSearch,
		OperationParams: params,
		TimeoutSeconds:  10,
		CredentialType:  execute.CredentialTypeSearchAPIKey,
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	result := decodeResult(t, rr)
	if result.Status != execute.StatusSuccess {
		t.Errorf("expected status 'success', got %q (err: %s)", result.Status, result.ErrorMessage)
	}
	if result.RequestID != "req-1" {
		t.Errorf("request_id not echoed: %q", result.RequestID)
	}
	if result.AgentID != "agent-1" {
		t.Errorf("agent_id not echoed: %q", result.AgentID)
	}
	if result.ElapsedMS < 0 {
		t.Errorf("elapsed_ms should be non-negative: %d", result.ElapsedMS)
	}

	// Deserialise operation_result and verify structure.
	var sr websearch.SearchResult
	if err := json.Unmarshal(result.OperationResult, &sr); err != nil {
		t.Fatalf("unmarshal operation_result: %v", err)
	}
	if len(sr.Results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(sr.Results))
	}
	if sr.Results[0].Title != "Go Concurrency Patterns" {
		t.Errorf("unexpected result title: %q", sr.Results[0].Title)
	}
}

func TestExecute_WebSearch_CredentialNotInResult(t *testing.T) {
	stub := &stubSearchProvider{result: &websearch.SearchResult{Query: "q", Results: nil}}
	h, _ := newTestHandler(t, stub)

	params, _ := json.Marshal(websearch.SearchParams{Query: "q"})
	rr := doRequest(t, h, execute.OperationRequest{
		RequestID: "req-2", AgentID: "agent-2", PermissionToken: "tok",
		OperationType: execute.OperationTypeWebSearch, OperationParams: params,
	})

	body := rr.Body.String()
	if strings.Contains(body, "test-tavily-key") {
		t.Error("credential leaked into response body")
	}
}

func TestExecute_WebSearch_MissingQuery_ExecError(t *testing.T) {
	stub := &stubSearchProvider{}
	h, _ := newTestHandler(t, stub)

	params, _ := json.Marshal(websearch.SearchParams{Query: ""}) // empty query
	rr := doRequest(t, h, execute.OperationRequest{
		RequestID: "req-3", AgentID: "agent-3", PermissionToken: "tok",
		OperationType: execute.OperationTypeWebSearch, OperationParams: params,
	})

	result := decodeResult(t, rr)
	if result.Status != execute.StatusExecError {
		t.Errorf("expected execution_error for empty query, got %q", result.Status)
	}
	if result.ErrorCode != "INVALID_PARAMS" {
		t.Errorf("expected error_code INVALID_PARAMS, got %q", result.ErrorCode)
	}
}

func TestExecute_WebSearch_CredentialUnavailable(t *testing.T) {
	auditor := audit.New(audit.NewJSONExporter(bytes.NewBuffer(nil)))
	// Manager with NO secrets seeded — TAVILY_API_KEY not present.
	mgr := secretmanager.NewMockSecretManager(auditor)
	stub := &stubSearchProvider{}
	h := execute.NewWithSearcher(mgr, auditor, stub)

	params, _ := json.Marshal(websearch.SearchParams{Query: "q"})
	rr := doRequest(t, h, execute.OperationRequest{
		RequestID: "req-4", AgentID: "agent-4", PermissionToken: "tok",
		OperationType: execute.OperationTypeWebSearch, OperationParams: params,
	})

	result := decodeResult(t, rr)
	if result.Status != execute.StatusExecError {
		t.Errorf("expected execution_error when credential missing, got %q", result.Status)
	}
	if result.ErrorCode != "CREDENTIAL_UNAVAILABLE" {
		t.Errorf("expected CREDENTIAL_UNAVAILABLE, got %q", result.ErrorCode)
	}
	// Error message must not contain vault paths or key names.
	if strings.Contains(result.ErrorMessage, "TAVILY") || strings.Contains(result.ErrorMessage, "kv/") {
		t.Errorf("error message exposes vault internals: %q", result.ErrorMessage)
	}
}

func TestExecute_UnsupportedOperationType_ScopeViolation(t *testing.T) {
	stub := &stubSearchProvider{}
	h, _ := newTestHandler(t, stub)

	rr := doRequest(t, h, execute.OperationRequest{
		RequestID: "req-5", AgentID: "agent-5", PermissionToken: "tok",
		OperationType:   "unsupported_op",
		OperationParams: json.RawMessage(`{}`),
	})

	result := decodeResult(t, rr)
	if result.Status != execute.StatusScopeViolation {
		t.Errorf("expected scope_violation for unknown operation type, got %q", result.Status)
	}
	if result.ErrorCode != "UNSUPPORTED_OPERATION" {
		t.Errorf("expected UNSUPPORTED_OPERATION, got %q", result.ErrorCode)
	}
}

func TestExecute_MissingRequiredFields_ExecError(t *testing.T) {
	stub := &stubSearchProvider{}
	h, _ := newTestHandler(t, stub)

	// No request_id, agent_id, or permission_token.
	rr := doRequest(t, h, execute.OperationRequest{
		OperationType:   execute.OperationTypeWebSearch,
		OperationParams: json.RawMessage(`{"query":"q"}`),
	})

	result := decodeResult(t, rr)
	if result.Status != execute.StatusExecError {
		t.Errorf("expected execution_error for missing fields, got %q", result.Status)
	}
}

func TestExecute_NonPOST_MethodNotAllowed(t *testing.T) {
	stub := &stubSearchProvider{}
	h, _ := newTestHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/execute", nil)
	rr := httptest.NewRecorder()
	h.Execute(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestExecute_InvalidJSON_ExecError(t *testing.T) {
	stub := &stubSearchProvider{}
	h, _ := newTestHandler(t, stub)

	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader("{not json}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Execute(rr, req)

	result := decodeResult(t, rr)
	if result.Status != execute.StatusExecError {
		t.Errorf("expected execution_error for invalid JSON, got %q", result.Status)
	}
}

func TestExecute_SearchProviderError_ExecError(t *testing.T) {
	stub := &stubSearchProvider{err: context.DeadlineExceeded}
	h, _ := newTestHandler(t, stub)

	params, _ := json.Marshal(websearch.SearchParams{Query: "q"})
	rr := doRequest(t, h, execute.OperationRequest{
		RequestID: "req-9", AgentID: "agent-9", PermissionToken: "tok",
		OperationType: execute.OperationTypeWebSearch, OperationParams: params,
		TimeoutSeconds: 1,
	})

	result := decodeResult(t, rr)
	// DeadlineExceeded from the provider with a live context on the handler means
	// the underlying operation timed out.
	if result.Status != execute.StatusTimedOut && result.Status != execute.StatusExecError {
		t.Errorf("expected timed_out or execution_error, got %q", result.Status)
	}
}

func TestExecute_Register_MountsOnExecutePath(t *testing.T) {
	stub := &stubSearchProvider{}
	auditor := audit.New(audit.NewJSONExporter(bytes.NewBuffer(nil)))
	mgr := secretmanager.NewMockSecretManager(auditor)
	_ = mgr.PutSecret(context.Background(), execute.SecretKeyTavily, "key")
	h := execute.NewWithSearcher(mgr, auditor, stub)

	mux := http.NewServeMux()
	h.Register(mux)

	params, _ := json.Marshal(websearch.SearchParams{Query: "q"})
	b, _ := json.Marshal(execute.OperationRequest{
		RequestID: "r", AgentID: "a", PermissionToken: "t",
		OperationType: execute.OperationTypeWebSearch, OperationParams: params,
	})
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(b))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 via mux, got %d", rr.Code)
	}
}
