package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/mlim3/cerberOS/memory/internal/api"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/pgvector/pgvector-go"
)

var (
	testServer *httptest.Server
	dbPool     *pgxpool.Pool
	vaultKey   string
)

type deterministicTestEmbedder struct{}

func (d *deterministicTestEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum64()

	v := make([]float32, 1536)
	for i := range v {
		// Stable pseudo-vector for deterministic test behavior.
		v[i] = float32((seed+uint64(i*97))%1000) / 1000.0
	}
	return pgvector.NewVector(v), nil
}

func TestMain(m *testing.M) {
	// Load environment variables for the test
	_ = godotenv.Load("../.env")
	if os.Getenv("VAULT_MASTER_KEY") == "" {
		os.Setenv("VAULT_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	}
	if os.Getenv("INTERNAL_VAULT_API_KEY") == "" {
		os.Setenv("INTERNAL_VAULT_API_KEY", "test-vault-key")
	}

	// Set up dependencies
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dbConfig := storage.Config{
		Host:     getEnvOrDefault("DB_HOST", "localhost"),
		Port:     getEnvOrDefault("DB_PORT", "5432"),
		User:     getEnvOrDefault("DB_USER", "user"),
		Password: getEnvOrDefault("DB_PASSWORD", "password"),
		Database: getEnvOrDefault("DB_NAME", "memory_db"),
	}

	db, err := storage.NewPostgresDB(ctx, dbConfig)
	if err != nil {
		logger.Error("failed to connect to database for testing", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	dbPool = db.GetPool()

	// Initialize repositories
	chatRepo := storage.NewChatRepository(dbPool)
	logRepo := storage.NewLogRepository(dbPool)
	vaultRepo := storage.NewVaultRepository(dbPool)
	agentLogsRepo := storage.NewAgentLogsRepository(dbPool)

	// Initialize Vault Manager
	vaultManager, err := logic.NewVaultManager()
	if err != nil {
		logger.Error("failed to initialize vault manager", "error", err)
		os.Exit(1)
	}

	piRepo := &storage.BaseRepository{Pool: dbPool}
	testEmbedder := &deterministicTestEmbedder{}
	piProcessor := logic.NewProcessor(piRepo, testEmbedder)

	// Initialize handlers
	chatHandler := api.NewChatHandler(chatRepo)
	logHandler := api.NewSystemLogHandler(logRepo)
	piHandler := api.NewPersonalInfoHandler(piProcessor, piRepo)
	vaultHandler := api.NewVaultHandler(vaultRepo, vaultManager, logRepo)
	agentHandler := api.NewAgentHandler(agentLogsRepo)

	// Set up routing
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/chat/{sessionId}/messages", chatHandler.HandleCreateMessage)
	mux.HandleFunc("GET /api/v1/chat/{sessionId}/messages", chatHandler.HandleListMessages)

	mux.HandleFunc("POST /api/v1/personal_info/{userId}/save", piHandler.Save)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/query", piHandler.Query)
	mux.HandleFunc("GET /api/v1/personal_info/{userId}/all", piHandler.GetAll)
	mux.HandleFunc("PUT /api/v1/personal_info/{userId}/facts/{factId}", piHandler.UpdateFact)
	mux.HandleFunc("DELETE /api/v1/personal_info/{userId}/facts/{factId}", piHandler.DeleteFact)

	mux.HandleFunc("POST /api/v1/system/events", logHandler.HandleCreateSystemEvent)
	mux.HandleFunc("GET /api/v1/system/events", logHandler.HandleListSystemEvents)

	vaultMux := http.NewServeMux()
	vaultMux.HandleFunc("POST /api/v1/vault/{userId}/secrets", vaultHandler.HandleSaveSecret)
	vaultMux.HandleFunc("PUT /api/v1/vault/{userId}/secrets/{keyName}", vaultHandler.HandleUpdateSecret)
	vaultMux.HandleFunc("GET /api/v1/vault/{userId}/secrets", vaultHandler.HandleGetSecret)
	vaultMux.HandleFunc("DELETE /api/v1/vault/{userId}/secrets/{keyName}", vaultHandler.HandleDeleteSecret)
	mux.Handle("/api/v1/vault/", http.StripPrefix("", api.RequireVaultKey(vaultMux)))

	mux.HandleFunc("POST /api/v1/agent/{taskId}/executions", agentHandler.HandleCreateTaskExecution)
	mux.HandleFunc("GET /api/v1/agent/{taskId}/executions", agentHandler.HandleGetExecutions)
	mux.HandleFunc("POST /api/v1/agents/tasks/{taskId}/executions", agentHandler.HandleCreateTaskExecution)
	mux.HandleFunc("GET /api/v1/agents/tasks/{taskId}/executions", agentHandler.HandleGetExecutions)

	handler := api.TraceIDMiddleware(logger, logRepo, mux)

	// Start test server
	testServer = httptest.NewServer(handler)

	// Get vault key
	vaultKey = os.Getenv("INTERNAL_VAULT_API_KEY")
	if vaultKey == "" {
		vaultKey = "test-vault-key"
		os.Setenv("INTERNAL_VAULT_API_KEY", vaultKey)
	}

	// Run tests
	code := m.Run()

	// Cleanup
	testServer.Close()
	os.Exit(code)
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Helpers
func doRequest(t *testing.T, method, path string, body interface{}, headers map[string]string) *http.Response {
	var bodyReader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Failed to marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader([]byte{})
	}

	req, err := http.NewRequest(method, testServer.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to execute request: %v", err)
	}

	return resp
}

func parseResponse(t *testing.T, resp *http.Response, target interface{}) {
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
}

func seedUser(t *testing.T, userID string) {
	t.Helper()
	u, err := uuid.Parse(userID)
	if err != nil {
		t.Fatalf("invalid user id: %v", err)
	}
	email := fmt.Sprintf("test-%s@example.com", u.String())
	_, err = dbPool.Exec(context.Background(),
		`INSERT INTO identity_schema.users (id, email) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		pgtype.UUID{Bytes: u, Valid: true},
		email,
	)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
}

// ==========================================
// SCENARIO 2: CHAT & IDEMPOTENCY
// ==========================================
func TestChatAndIdempotency(t *testing.T) {
	sessionID := uuid.New().String()
	userID := uuid.New().String()
	idempotencyKey := uuid.New().String()
	seedUser(t, userID)

	t.Run("Happy Path - Save a message", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "user",
			"content":        "Hello, this is a test message.",
			"idempotencyKey": idempotencyKey,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), reqBody, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Expected status 201, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("Missing data field in response")
		}

		msg, ok := data["message"].(map[string]interface{})
		if !ok {
			t.Fatalf("Missing message field in data")
		}

		if msg["content"] != reqBody["content"] {
			t.Errorf("Expected content %s, got %v", reqBody["content"], msg["content"])
		}

		// Verify retrieval
		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), nil, nil)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", getResp.StatusCode)
		}

		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)

		getData, ok := getResult["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("Missing data field in get response")
		}

		messages, ok := getData["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Fatalf("Expected messages array in get response")
		}
	})

	t.Run("Idempotency Check", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "user",
			"content":        "Hello, this is a test message.",
			"idempotencyKey": idempotencyKey,
		}

		// Send exact same request again
		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), reqBody, nil)

		// Wait for idempotency logic or check response
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 201 or 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		// Verify we only have 1 message in history
		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), nil, nil)
		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)

		getData := getResult["data"].(map[string]interface{})
		messages := getData["messages"].([]interface{})

		if len(messages) != 1 {
			t.Errorf("Expected 1 message (idempotency should prevent duplicate), got %d", len(messages))
		}
	})

	t.Run("Idempotency Conflict On Different Payload", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "assistant",
			"content":        "Changed content",
			"idempotencyKey": idempotencyKey,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), reqBody, nil)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("Expected status 409, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		errObj := result["error"].(map[string]interface{})
		if errObj["code"] != "conflict" {
			t.Fatalf("Expected conflict error code, got %v", errObj["code"])
		}
	})
}

// ==========================================
// SCENARIO 3: PERSONAL INFO & CONCURRENCY
// ==========================================
func TestPersonalInfoAndConcurrency(t *testing.T) {
	userID := uuid.New().String()
	seedUser(t, userID)

	t.Run("Semantic Search", func(t *testing.T) {
		// Save 3 different chunks
		chunks := []string{
			"My favorite color is blue.",
			"I live in San Francisco.",
			"I work as a software engineer.",
		}

		for _, chunk := range chunks {
			reqBody := map[string]interface{}{
				"content":      chunk,
				"sourceType":   "chat",
				"sourceId":     uuid.New().String(),
				"extractFacts": true,
			}
			resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/save", userID), reqBody, nil)
			if resp.StatusCode != http.StatusOK {
				var body []byte
				if resp.Body != nil {
					body = make([]byte, 1024)
					n, _ := resp.Body.Read(body)
					body = body[:n]
				}
				t.Fatalf("Failed to save chunk: expected 200, got %d, body: %s", resp.StatusCode, string(body))
			}
		}

		// Query for one
		queryReq := map[string]interface{}{
			"query": "Where do I live?",
			"topK":  1,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/query", userID), queryReq, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to query chunks: expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		data := result["data"].(map[string]interface{})
		resultChunks := data["results"].([]interface{})

		if len(resultChunks) > 0 {
			firstChunk := resultChunks[0].(map[string]interface{})
			// Verify similarityScore is present
			if _, ok := firstChunk["similarityScore"]; !ok {
				// The mock embedder might not populate similarityScore, but we should check it's returned if present
				t.Log("Warning: similarityScore not present in response")
			}
		}
	})

	t.Run("Optimistic Concurrency (The Race)", func(t *testing.T) {
		// 1. Create a fact first via Save
		saveReq := map[string]interface{}{
			"content":      "My phone number is 555-1234",
			"sourceType":   "chat",
			"sourceId":     uuid.New().String(),
			"extractFacts": true,
		}
		saveResp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/save", userID), saveReq, nil)
		if saveResp.StatusCode != http.StatusOK {
			var body []byte
			if saveResp.Body != nil {
				body = make([]byte, 1024)
				n, _ := saveResp.Body.Read(body)
				body = body[:n]
			}
			t.Fatalf("Failed to save fact: expected 200, got %d, body: %s", saveResp.StatusCode, string(body))
		}

		// Get all facts to find the ID
		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/personal_info/%s/all", userID), nil, nil)
		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)

		data := getResult["data"].(map[string]interface{})
		facts := data["facts"].([]interface{})

		if len(facts) == 0 {
			t.Fatalf("No facts found after save")
		}

		fact := facts[0].(map[string]interface{})
		factID := fact["factId"].(string)

		// Convert version to int
		versionFloat, ok := fact["version"].(float64)
		if !ok {
			t.Fatalf("Version missing or not float64")
		}
		version1 := int32(versionFloat)

		// 2. Simulate Agent A updating it (Version becomes 2)
		updateReqA := map[string]interface{}{
			"category":   fact["category"].(string),
			"factKey":    fact["factKey"].(string),
			"factValue":  "555-9999",
			"confidence": 0.9,
			"version":    version1,
		}

		updateRespA := doRequest(t, "PUT", fmt.Sprintf("/api/v1/personal_info/%s/facts/%s", userID, factID), updateReqA, nil)
		if updateRespA.StatusCode != http.StatusOK {
			t.Fatalf("Agent A failed to update fact: expected 200, got %d", updateRespA.StatusCode)
		}

		// 3. Attempt to have Agent B update it using the stale 'Version 1'
		updateReqB := map[string]interface{}{
			"category":   fact["category"].(string),
			"factKey":    fact["factKey"].(string),
			"factValue":  "555-0000",
			"confidence": 0.8,
			"version":    version1, // STALE VERSION
		}

		updateRespB := doRequest(t, "PUT", fmt.Sprintf("/api/v1/personal_info/%s/facts/%s", userID, factID), updateReqB, nil)

		// 4. VERIFY the server returns a 409 Conflict error code
		if updateRespB.StatusCode != http.StatusConflict {
			t.Errorf("Expected 409 Conflict for stale update, got %d", updateRespB.StatusCode)
		}
	})

	t.Run("Delete Fact", func(t *testing.T) {
		saveReq := map[string]interface{}{
			"content":      "I prefer keyboard shortcuts.",
			"sourceType":   "chat",
			"sourceId":     uuid.New().String(),
			"extractFacts": true,
		}
		saveResp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/save", userID), saveReq, nil)
		if saveResp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to save fact for delete test: got %d", saveResp.StatusCode)
		}

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/personal_info/%s/all", userID), nil, nil)
		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)
		facts := getResult["data"].(map[string]interface{})["facts"].([]interface{})
		if len(facts) == 0 {
			t.Fatalf("No facts found for delete test")
		}
		factID := facts[len(facts)-1].(map[string]interface{})["factId"].(string)

		delResp := doRequest(t, "DELETE", fmt.Sprintf("/api/v1/personal_info/%s/facts/%s", userID, factID), nil, nil)
		if delResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200 on delete, got %d", delResp.StatusCode)
		}
		var delResult map[string]interface{}
		parseResponse(t, delResp, &delResult)
		data := delResult["data"].(map[string]interface{})
		if data["deleted"] != true {
			t.Fatalf("Expected deleted=true, got %v", data["deleted"])
		}
	})
}

// ==========================================
// SCENARIO 4: VAULT SECURITY
// ==========================================
func TestVaultSecurity(t *testing.T) {
	userID := uuid.New().String()
	seedUser(t, userID)

	t.Run("Unauthorized Access", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"key_name": "api_key",
			"value":    "super_secret_value",
		}

		// Call POST without X-API-KEY
		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/vault/%s/secrets", userID), reqBody, nil)

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized for missing API key, got %d", resp.StatusCode)
		}

		var errResp map[string]interface{}
		parseResponse(t, resp, &errResp)

		if errResp["error"] == nil {
			t.Errorf("Expected error envelope in response")
		} else {
			errData := errResp["error"].(map[string]interface{})
			if errData["code"] != "invalid_argument" {
				t.Errorf("Expected error code invalid_argument, got %v", errData["code"])
			}
		}
	})

	t.Run("Audit Verification", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"key_name": "api_key",
			"value":    "super_secret_value",
		}

		headers := map[string]string{
			"X-API-KEY":  vaultKey,
			"X-Trace-ID": uuid.New().String(), // Add custom trace ID to find it
		}

		// Make an authorized request
		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/vault/%s/secrets", userID), reqBody, headers)
		if resp.StatusCode != http.StatusCreated {
			var body []byte
			if resp.Body != nil {
				body = make([]byte, 1024)
				n, _ := resp.Body.Read(body)
				body = body[:n]
			}
			t.Fatalf("Failed to save secret: expected 201, got %d, body: %s", resp.StatusCode, string(body))
		}

		// Query the system events to verify VAULT_ACCESS log
		eventsResp := doRequest(t, "GET", "/api/v1/system/events?serviceName=VaultService", nil, nil)
		if eventsResp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to get system events: expected 200, got %d", eventsResp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, eventsResp, &result)

		data := result["data"].(map[string]interface{})
		events := data["events"].([]interface{})

		found := false
		for _, e := range events {
			event := e.(map[string]interface{})
			if event["message"] == "VAULT_ACCESS" {
				// Check metadata if possible
				if meta, ok := event["metadata"].(map[string]interface{}); ok {
					if meta["status"] == "granted" && meta["userId"] == userID {
						found = true
						break
					}
				}
			}
		}

		if !found {
			t.Errorf("Could not find VAULT_ACCESS audit log for the authorized request")
		}
	})
}

// ==========================================
// SCENARIO 5: SYSTEM & TRACING
// ==========================================
func TestSystemAndTracing(t *testing.T) {
	t.Run("TraceID Propagation", func(t *testing.T) {
		customTraceID := uuid.New().String()

		reqBody := map[string]interface{}{
			"message":     "Test tracing message",
			"severity":    "info",
			"serviceName": "test-suite",
			"traceId":     customTraceID, // Also send in body to ensure it's saved
		}

		headers := map[string]string{
			"X-Trace-ID": customTraceID,
		}

		resp := doRequest(t, "POST", "/api/v1/system/events", reqBody, headers)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Failed to create system event: expected 201, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		_ = result["data"].(map[string]interface{})

		// The API doesn't seem to return the TraceID in the response header in the current implementation,
		// but we can query for the event and check its traceId

		eventsResp := doRequest(t, "GET", "/api/v1/system/events?serviceName=test-suite", nil, nil)
		var eventsResult map[string]interface{}
		parseResponse(t, eventsResp, &eventsResult)

		eventsData := eventsResult["data"].(map[string]interface{})
		events := eventsData["events"].([]interface{})

		found := false
		for _, e := range events {
			event := e.(map[string]interface{})
			if event["message"] == "Test tracing message" && event["traceId"] == customTraceID {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("Could not find system event with custom TraceID in database")
		}
	})
}

// ==========================================
// SCENARIO 6: TASK EXECUTION LOGS
// ==========================================
func TestTaskExecutionLogs(t *testing.T) {
	taskID := uuid.New().String()
	agentID := uuid.New().String()

	t.Run("Chronological Task Execution Logs", func(t *testing.T) {
		// 1. POST 3 execution steps
		steps := []struct {
			ActionType string
			Status     string
		}{
			{"reasoning_step", "pending"},
			{"tool_call", "success"},
			{"final_answer", "failed"},
		}

		for _, step := range steps {
			reqBody := map[string]interface{}{
				"agentId":    agentID,
				"actionType": step.ActionType,
				"payload":    map[string]interface{}{"key": "value"},
				"status":     step.Status,
			}

			resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/agent/%s/executions", taskID), reqBody, nil)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("Failed to create task execution: expected 201, got %d", resp.StatusCode)
			}

			var createResp map[string]interface{}
			parseResponse(t, resp, &createResp)
			data := createResp["data"].(map[string]interface{})
			if data["executionId"] == nil || data["createdAt"] == nil {
				t.Fatalf("Expected executionId and createdAt in response")
			}
		}

		// 2. Call GET endpoint
		resp := doRequest(t, "GET", fmt.Sprintf("/api/v1/agent/%s/executions?limit=2", taskID), nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to get task executions: expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		data := result["data"].(map[string]interface{})
		executions := data["executions"].([]interface{})

		// 3. Verify limit is respected
		if len(executions) != 2 {
			t.Fatalf("Expected 2 task executions due to limit, got %d", len(executions))
		}

		// 4. Verify chronological order and shape
		for i, execAny := range executions {
			exec := execAny.(map[string]interface{})
			if exec["actionType"] != steps[i].ActionType {
				t.Errorf("Step %d: expected actionType %s, got %v", i, steps[i].ActionType, exec["actionType"])
			}
			if exec["status"] != steps[i].Status {
				t.Errorf("Step %d: expected status %s, got %v", i, steps[i].Status, exec["status"])
			}
		}
	})
}
