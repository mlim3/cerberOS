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
	"strconv"
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

	dim, err := strconv.Atoi(getEnvOrDefault("EMBEDDING_DIM", "768"))
	if err != nil || dim <= 0 {
		dim = 768
	}

	v := make([]float32, dim)
	for i := range v {
		v[i] = float32((seed+uint64(i*97))%1000) / 1000.0
	}
	return pgvector.NewVector(v), nil
}

func (d *deterministicTestEmbedder) ModelVersion() string {
	return "test-model"
}

func TestMain(m *testing.M) {
	_ = godotenv.Load("../.env")
	if os.Getenv("VAULT_MASTER_KEY") == "" {
		os.Setenv("VAULT_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	}
	if os.Getenv("INTERNAL_VAULT_API_KEY") == "" {
		os.Setenv("INTERNAL_VAULT_API_KEY", "test-vault-key")
	}

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

	if _, err := dbPool.Exec(ctx, schedulingSchemaDDL); err != nil {
		logger.Error("failed to ensure scheduling_schema (for integration tests against older DB volumes)", "error", err)
		os.Exit(1)
	}

	chatRepo := storage.NewChatRepository(dbPool)
	if err := chatRepo.EnsureSchema(ctx); err != nil {
		logger.Error("failed to ensure chat schema for testing", "error", err)
		os.Exit(1)
	}
	orchestratorRepo := storage.NewOrchestratorRepository(dbPool)
	if err := orchestratorRepo.EnsureSchema(ctx); err != nil {
		logger.Error("failed to ensure orchestrator schema for testing", "error", err)
		os.Exit(1)
	}
	logRepo := storage.NewLogRepository(dbPool)
	vaultRepo := storage.NewVaultRepository(dbPool)
	agentLogsRepo := storage.NewAgentLogsRepository(dbPool)
	scheduledJobsRepo := storage.NewScheduledJobsRepository(dbPool)

	vaultManager, err := logic.NewVaultManager()
	if err != nil {
		logger.Error("failed to initialize vault manager", "error", err)
		os.Exit(1)
	}

	piRepo := &storage.BaseRepository{Pool: dbPool}
	testEmbedder := &deterministicTestEmbedder{}
	piProcessor := logic.NewProcessor(piRepo, testEmbedder)

	chatHandler := api.NewChatHandler(chatRepo)
	orchestratorHandler := api.NewOrchestratorHandler(orchestratorRepo)
	logHandler := api.NewSystemLogHandler(logRepo)
	piHandler := api.NewPersonalInfoHandler(piProcessor, piRepo)
	vaultHandler := api.NewVaultHandler(vaultRepo, vaultManager, logRepo)
	agentHandler := api.NewAgentHandler(agentLogsRepo)
	scheduledJobsHandler := api.NewScheduledJobsHandler(scheduledJobsRepo, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/conversations", chatHandler.HandleListConversations)
	mux.HandleFunc("POST /api/v1/conversations", chatHandler.HandleCreateConversation)
	mux.HandleFunc("POST /api/v1/tasks", chatHandler.HandleCreateTask)
	mux.HandleFunc("GET /api/v1/tasks/{taskId}", chatHandler.HandleGetTask)
	mux.HandleFunc("POST /api/v1/chat/{conversationId}/messages", chatHandler.HandleCreateMessage)
	mux.HandleFunc("GET /api/v1/chat/{conversationId}/messages", chatHandler.HandleListMessages)
	mux.HandleFunc("GET /api/v1/chat/{conversationId}/history", chatHandler.HandleGetSessionHistory)
	orchestratorMux := http.NewServeMux()
	orchestratorMux.HandleFunc("POST /api/v1/orchestrator/records", orchestratorHandler.HandleWriteRecord)
	orchestratorMux.HandleFunc("GET /api/v1/orchestrator/records", orchestratorHandler.HandleQueryRecords)
	orchestratorMux.HandleFunc("GET /api/v1/orchestrator/records/latest", orchestratorHandler.HandleReadLatest)
	mux.Handle("/api/v1/orchestrator/", http.StripPrefix("", api.RequireVaultKey(orchestratorMux)))
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/save", piHandler.Save)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/query", piHandler.Query)
	mux.HandleFunc("GET /api/v1/personal_info/{userId}/all", piHandler.GetAll)
	mux.HandleFunc("PUT /api/v1/personal_info/{userId}/facts/{factId}", piHandler.UpdateFact)
	mux.HandleFunc("DELETE /api/v1/personal_info/{userId}/facts/{factId}", piHandler.DeleteFact)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/facts/{factId}/archive", piHandler.ArchiveFact)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/facts/{factId}/supersede", piHandler.SupersedeFact)
	mux.HandleFunc("POST /api/v1/system/events", logHandler.HandleCreateSystemEvent)
	mux.HandleFunc("GET /api/v1/system/events", logHandler.HandleListSystemEvents)
	mux.Handle("POST /api/v1/scheduled_jobs", api.RequireVaultKey(http.HandlerFunc(scheduledJobsHandler.HandleCreateScheduledJob)))
	mux.Handle("POST /api/v1/scheduled_jobs/run_due", api.RequireVaultKey(http.HandlerFunc(scheduledJobsHandler.HandleRunDueJobs)))
	mux.Handle("GET /api/v1/scheduled_jobs/{jobId}/runs", api.RequireVaultKey(http.HandlerFunc(scheduledJobsHandler.HandleListScheduledJobRuns)))
	mux.Handle("GET /api/v1/user_crons", api.RequireVaultKey(http.HandlerFunc(scheduledJobsHandler.HandleListUserCrons)))
	mux.Handle("DELETE /api/v1/scheduled_jobs/{jobId}", api.RequireVaultKey(http.HandlerFunc(scheduledJobsHandler.HandleDeleteUserCron)))
	mux.Handle("POST /api/v1/system/maintenance/run", api.RequireVaultKey(http.HandlerFunc(scheduledJobsHandler.HandleRunSystemMaintenance)))

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
	testServer = httptest.NewServer(handler)

	vaultKey = os.Getenv("INTERNAL_VAULT_API_KEY")
	if vaultKey == "" {
		vaultKey = "test-vault-key"
		os.Setenv("INTERNAL_VAULT_API_KEY", vaultKey)
	}

	code := m.Run()
	testServer.Close()
	os.Exit(code)
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

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

// schedulingSchemaDDL matches memory/scripts/init-db.sql so tests pass if Postgres was created before scheduling landed.
const schedulingSchemaDDL = `
CREATE SCHEMA IF NOT EXISTS scheduling_schema;

CREATE TABLE IF NOT EXISTS scheduling_schema.scheduled_jobs (
    id UUID PRIMARY KEY,
    job_type VARCHAR(100) NOT NULL,
    target_kind VARCHAR(50) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    schedule_kind VARCHAR(50) NOT NULL,
    interval_seconds INT,
    name VARCHAR(255) NOT NULL,
    payload JSONB,
    next_run_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS user_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS time_zone VARCHAR(64) NOT NULL DEFAULT 'UTC';
ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS cron_expression TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_next_run
    ON scheduling_schema.scheduled_jobs (next_run_at)
    WHERE status = 'active';

CREATE TABLE IF NOT EXISTS scheduling_schema.scheduled_job_runs (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES scheduling_schema.scheduled_jobs(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    detail JSONB,
    trace_id UUID,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_job_id ON scheduling_schema.scheduled_job_runs(job_id);
CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_started_at ON scheduling_schema.scheduled_job_runs(started_at DESC);
`

func seedUser(t *testing.T, userID string) {
	t.Helper()
	u, err := uuid.Parse(userID)
	if err != nil {
		t.Fatalf("invalid user id: %v", err)
	}
	email := fmt.Sprintf("test-%s@example.com", u.String())
	_, err = dbPool.Exec(context.Background(),
		`INSERT INTO identity_schema.users (id, email) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		pgtype.UUID{Bytes: [16]byte(u), Valid: true},
		email,
	)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
}
