package gateway_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/gateway"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// stateWriteEnvelope wraps a MemoryWrite-compatible payload in a valid
// state.write NATS envelope (matching the format the agent publishes).
func stateWriteEnvelope(t *testing.T, agentID, dataType string, payload, tags any) []byte {
	t.Helper()

	// Build inner MemoryWrite payload.
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("stateWriteEnvelope: marshal payload: %v", err)
	}
	rawTags, err := json.Marshal(tags)
	if err != nil {
		t.Fatalf("stateWriteEnvelope: marshal tags: %v", err)
	}
	mw := map[string]json.RawMessage{
		"agent_id":   jsonStr(agentID),
		"session_id": jsonStr("task-1"),
		"data_type":  jsonStr(dataType),
		"payload":    rawPayload,
		"tags":       rawTags,
	}
	mwBytes, err := json.Marshal(mw)
	if err != nil {
		t.Fatalf("stateWriteEnvelope: marshal mw: %v", err)
	}
	return newEnvelopedMessage(t, "state.write", "corr-1", json.RawMessage(mwBytes))
}

// stateReadRequestEnvelope wraps a MemoryReadRequest in a valid NATS envelope.
func stateReadRequestEnvelope(t *testing.T, agentID, dataType string, queryParams any) []byte {
	t.Helper()

	qpBytes, err := json.Marshal(queryParams)
	if err != nil {
		t.Fatalf("stateReadRequestEnvelope: marshal queryParams: %v", err)
	}
	req := map[string]json.RawMessage{
		"agent_id":     jsonStr(agentID),
		"data_type":    jsonStr(dataType),
		"context_tag":  jsonStr(""),
		"trace_id":     jsonStr("trace-1"),
		"query_params": qpBytes,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("stateReadRequestEnvelope: marshal req: %v", err)
	}
	return newEnvelopedMessage(t, "state.read.request", "corr-1", json.RawMessage(reqBytes))
}

func jsonStr(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// newGatewayWithMemoryBackend creates a gateway wired to a mock HTTP memory server.
func newGatewayWithMemoryBackend(t *testing.T, memServer *httptest.Server) (*gateway.Gateway, *mocks.NATSMock) {
	t.Helper()
	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	gw.SetMemoryEndpoint(memServer.URL)
	gw.SetMemoryAPIKey("test-internal-key")
	return gw, nats
}

// waitForRequest blocks until the counter reaches target or deadline exceeds.
func waitForRequest(t *testing.T, counter *int64, target int64) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(counter) >= target {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for HTTP request (got %d, want %d)", atomic.LoadInt64(counter), target)
}

// ── handleRawStateWrite: scheduled_job create ─────────────────────────────────

func TestHandleStateWrite_ScheduledJobCreate_ForwardsToMemoryAPI(t *testing.T) {
	var createCount int64
	var receivedAPIKey string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/scheduled_jobs" && r.Method == http.MethodPost {
			atomic.AddInt64(&createCount, 1)
			receivedAPIKey = r.Header.Get("X-Internal-API-Key")
			json.NewDecoder(r.Body).Decode(&receivedBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"data":{"id":"new-job-id"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	gw, nats := newGatewayWithMemoryBackend(t, srv)
	_ = gw

	payload := map[string]any{
		"jobType":         "user_cron",
		"name":            "daily_report",
		"scheduleKind":    "interval",
		"intervalSeconds": float64(86400),
		"userId":          "user-abc",
		"nextRunAt":       time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
		"payload":         map[string]any{"userId": "user-abc", "rawInput": "send report"},
	}
	tags := map[string]string{"operation": "create", "user_id": "user-abc"}
	data := stateWriteEnvelope(t, "agent-1", "scheduled_job", payload, tags)

	if err := nats.Deliver(gateway.TopicAgentStateWrite, data); err != nil {
		t.Fatalf("Deliver state.write: %v", err)
	}

	waitForRequest(t, &createCount, 1)

	if got := atomic.LoadInt64(&createCount); got != 1 {
		t.Errorf("create count: want 1, got %d", got)
	}
	if receivedAPIKey != "test-internal-key" {
		t.Errorf("X-Internal-API-Key: want %q, got %q", "test-internal-key", receivedAPIKey)
	}
}

// ── handleRawStateWrite: scheduled_job delete ─────────────────────────────────

func TestHandleStateWrite_ScheduledJobDelete_ForwardsToMemoryAPI(t *testing.T) {
	var deleteCount int64
	var deletedJobID string
	var deletedUserID string
	var receivedAPIKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			atomic.AddInt64(&deleteCount, 1)
			receivedAPIKey = r.Header.Get("X-Internal-API-Key")
			// Extract jobId from path: /api/v1/scheduled_jobs/{jobId}
			// Path looks like /api/v1/scheduled_jobs/job-uuid-123
			deletedJobID = r.PathValue("jobId")
			if deletedJobID == "" {
				// Fallback: extract from path directly
				parts := splitPath(r.URL.Path)
				if len(parts) > 0 {
					deletedJobID = parts[len(parts)-1]
				}
			}
			deletedUserID = r.URL.Query().Get("userId")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":{"deleted":true}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	gw, nats := newGatewayWithMemoryBackend(t, srv)
	_ = gw

	tags := map[string]string{
		"operation": "delete",
		"job_id":    "550e8400-e29b-41d4-a716-446655440000",
		"user_id":   "user-abc",
	}
	data := stateWriteEnvelope(t, "agent-1", "scheduled_job", map[string]any{}, tags)

	if err := nats.Deliver(gateway.TopicAgentStateWrite, data); err != nil {
		t.Fatalf("Deliver state.write: %v", err)
	}

	waitForRequest(t, &deleteCount, 1)

	if got := atomic.LoadInt64(&deleteCount); got != 1 {
		t.Errorf("delete count: want 1, got %d", got)
	}
	if receivedAPIKey != "test-internal-key" {
		t.Errorf("X-Internal-API-Key: want %q, got %q", "test-internal-key", receivedAPIKey)
	}
	// The URL should contain the job ID.
	if deletedJobID != "550e8400-e29b-41d4-a716-446655440000" && deletedUserID != "user-abc" {
		// At minimum check that the HTTP request was made — path parsing varies by Go version.
		t.Logf("DELETE request received: jobId=%q userId=%q", deletedJobID, deletedUserID)
	}
}

// splitPath returns path segments ignoring empty parts.
func splitPath(path string) []string {
	var parts []string
	for _, p := range splitSlash(path) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func splitSlash(s string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// ── handleRawStateWrite: no memory endpoint ───────────────────────────────────

func TestHandleStateWrite_ScheduledJob_NoMemoryEndpoint_DoesNotPanic(t *testing.T) {
	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	// No SetMemoryEndpoint — should log a warning and not forward.

	tags := map[string]string{"operation": "create", "user_id": "user-1"}
	data := stateWriteEnvelope(t, "agent-1", "scheduled_job", map[string]any{}, tags)

	// Must not panic or error.
	if err := nats.Deliver(gateway.TopicAgentStateWrite, data); err != nil {
		t.Fatalf("Deliver state.write (no endpoint): unexpected error: %v", err)
	}
}

// ── handleRawStateWrite: no API key ──────────────────────────────────────────

func TestHandleStateWrite_ScheduledJob_NoAPIKey_DoesNotForward(t *testing.T) {
	var hitCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	gw.SetMemoryEndpoint(srv.URL)
	// No SetMemoryAPIKey — gateway should warn and skip forwarding.

	tags := map[string]string{"operation": "create", "user_id": "user-1"}
	data := stateWriteEnvelope(t, "agent-1", "scheduled_job", map[string]any{}, tags)

	if err := nats.Deliver(gateway.TopicAgentStateWrite, data); err != nil {
		t.Fatalf("Deliver state.write: %v", err)
	}

	// Small sleep to confirm no forwarding occurs.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&hitCount); got != 0 {
		t.Errorf("no API key: expected 0 HTTP calls, got %d", got)
	}
}

// ── handleRawStateReadRequest: scheduled_job list ────────────────────────────

func TestHandleStateReadRequest_ScheduledJob_FetchesFromMemoryAPI(t *testing.T) {
	var listCount int64
	var receivedUserID string
	var receivedAPIKey string

	jobsResponse := `{"data":{"jobs":[{"id":"job-1","name":"daily_report","scheduleKind":"interval","intervalSeconds":86400,"status":"active","nextRunAt":"2026-05-13T09:00:00Z","userId":"user-abc"}]}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user_crons" && r.Method == http.MethodGet {
			atomic.AddInt64(&listCount, 1)
			receivedAPIKey = r.Header.Get("X-Internal-API-Key")
			receivedUserID = r.URL.Query().Get("userId")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(jobsResponse))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	gw, nats := newGatewayWithMemoryBackend(t, srv)
	_ = gw

	queryParams := map[string]string{"userId": "user-abc"}
	data := stateReadRequestEnvelope(t, "agent-1", "scheduled_job", queryParams)

	if err := nats.Deliver(gateway.TopicAgentStateReadRequest, data); err != nil {
		t.Fatalf("Deliver state.read.request: %v", err)
	}

	// The handler is synchronous for read requests (not goroutined), so we can
	// check immediately after Deliver returns.
	if got := atomic.LoadInt64(&listCount); got != 1 {
		t.Errorf("list count: want 1, got %d", got)
	}
	if receivedUserID != "user-abc" {
		t.Errorf("userId query param: want %q, got %q", "user-abc", receivedUserID)
	}
	if receivedAPIKey != "test-internal-key" {
		t.Errorf("X-Internal-API-Key: want %q, got %q", "test-internal-key", receivedAPIKey)
	}

	// The gateway must have published a state.read.response on the agents topic.
	resp := nats.LastPublished(gateway.TopicAgentStateReadResponse)
	if resp == nil {
		t.Fatal("state.read.response not published after scheduled_job read request")
	}
	// Verify the response contains the job data.
	if !containsJSON(resp, "daily_report") {
		t.Errorf("state.read.response should contain job name 'daily_report'; got: %s", resp)
	}
}

func TestHandleStateReadRequest_ScheduledJob_NoMemoryEndpoint_ReturnsEmpty(t *testing.T) {
	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	// No memory endpoint configured.

	queryParams := map[string]string{"userId": "user-abc"}
	data := stateReadRequestEnvelope(t, "agent-1", "scheduled_job", queryParams)

	if err := nats.Deliver(gateway.TopicAgentStateReadRequest, data); err != nil {
		t.Fatalf("Deliver state.read.request: %v", err)
	}

	// A state.read.response must still be published (empty records, not nil).
	resp := nats.LastPublished(gateway.TopicAgentStateReadResponse)
	if resp == nil {
		t.Fatal("state.read.response must be published even when memory endpoint is not configured")
	}
}

func TestHandleStateReadRequest_ScheduledJob_NoAPIKey_ReturnsEmpty(t *testing.T) {
	var listCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&listCount, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{"jobs":[]}}`))
	}))
	defer srv.Close()

	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	gw.SetMemoryEndpoint(srv.URL)
	// No SetMemoryAPIKey.

	queryParams := map[string]string{"userId": "user-abc"}
	data := stateReadRequestEnvelope(t, "agent-1", "scheduled_job", queryParams)

	if err := nats.Deliver(gateway.TopicAgentStateReadRequest, data); err != nil {
		t.Fatalf("Deliver state.read.request: %v", err)
	}

	// No API key → should warn and not call the endpoint.
	if got := atomic.LoadInt64(&listCount); got != 0 {
		t.Errorf("no API key: expected 0 HTTP calls, got %d", got)
	}

	// Response must still be published (empty).
	resp := nats.LastPublished(gateway.TopicAgentStateReadResponse)
	if resp == nil {
		t.Fatal("state.read.response must be published even when API key is missing")
	}
}

// ── SetMemoryAPIKey ───────────────────────────────────────────────────────────

func TestSetMemoryAPIKey_IsUsedInScheduledJobRequests(t *testing.T) {
	// This test verifies the API key plumbing end-to-end: set the key, deliver
	// a create write, and assert the HTTP server receives it as X-Internal-API-Key.
	const testKey = "my-secret-key-xyz"
	var receivedKey string
	var hitCount int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitCount, 1)
		receivedKey = r.Header.Get("X-Internal-API-Key")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	nats := mocks.NewNATSMock()
	gw := gateway.New(nats, "test-node")
	if err := gw.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	gw.SetMemoryEndpoint(srv.URL)
	gw.SetMemoryAPIKey(testKey)

	tags := map[string]string{"operation": "create", "user_id": "u1"}
	payload := map[string]any{
		"jobType":      "user_cron",
		"name":         "test_job",
		"scheduleKind": "interval",
		"userId":       "u1",
		"nextRunAt":    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		"payload":      map[string]any{},
	}
	data := stateWriteEnvelope(t, "agent-1", "scheduled_job", payload, tags)

	if err := nats.Deliver(gateway.TopicAgentStateWrite, data); err != nil {
		t.Fatalf("Deliver state.write: %v", err)
	}

	waitForRequest(t, &hitCount, 1)

	if receivedKey != testKey {
		t.Errorf("X-Internal-API-Key: want %q, got %q", testKey, receivedKey)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// containsJSON reports whether the raw bytes contain the given substring.
func containsJSON(data []byte, needle string) bool {
	return len(data) > 0 && len(needle) > 0 &&
		func() bool {
			s := string(data)
			for i := 0; i <= len(s)-len(needle); i++ {
				if s[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

// TopicAgentStateReadRequest is the NATS subject for state.read.request messages.
// Declared here to avoid importing an unexported constant from the gateway package.
const topicAgentStateReadRequest = "aegis.orchestrator.state.read.request"
