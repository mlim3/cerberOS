//go:generate go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d .,../../internal/api -o ../../docs --parseInternal

// Package main runs the memory service API.
//
// @title Memory Service API
// @version v1
// @description REST API for CerberOS memory, chat, personal info, system event, vault, and agent execution services.
// @BasePath /
// @schemes http
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-Internal-API-Key
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	docs "github.com/mlim3/cerberOS/memory/docs"
	"github.com/mlim3/cerberOS/memory/internal/api"
	"github.com/mlim3/cerberOS/memory/internal/heartbeat"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/mlim3/cerberOS/memory/internal/telemetry"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	httpSwagger "github.com/swaggo/http-swagger"
)

// newHealthzHandler reports process and database health.
// @Summary Health check
// @Description Returns service health and database connectivity status
// @Tags system
// @Produce json
// @Success 200 {object} map[string]interface{} "Healthy"
// @Failure 503 {object} map[string]interface{} "Degraded"
// @Router /api/v1/healthz [get]
func newHealthzHandler(db *storage.PostgresDB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := "healthy"
		dbStatus := "connected"

		if err := db.Ping(r.Context()); err != nil {
			logger.Error("database ping failed", "error", err)
			status = "degraded"
			dbStatus = "disconnected"
		}

		resp := map[string]any{
			"status":    status,
			"database":  dbStatus,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}

		w.Header().Set("Content-Type", "application/json")
		if status == "degraded" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		json.NewEncoder(w).Encode(api.SuccessResponse(resp))
	}
}

func main() {
	// Initialize structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("component", "memory", "module", "server")
	slog.SetDefault(logger)

	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		logger.Info("No .env file found or error loading it, proceeding with environment variables")
	}

	// Fast-fail if required vault keys are missing
	if os.Getenv("VAULT_MASTER_KEY") == "" {
		logger.Error("VAULT_MASTER_KEY environment variable is missing")
		os.Exit(1)
	}
	if os.Getenv("INTERNAL_VAULT_API_KEY") == "" {
		logger.Error("INTERNAL_VAULT_API_KEY environment variable is missing")
		os.Exit(1)
	}

	// 1. Initialize Database root context that listens for signals
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// OTLP tracing — no-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset.
	traceShutdown, err := telemetry.Init(ctx)
	if err != nil {
		logger.Warn("telemetry init failed — continuing without traces", "error", err)
	} else if telemetry.Enabled() {
		logger.Info("telemetry initialized", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	defer func() {
		if traceShutdown != nil {
			_ = traceShutdown(context.Background())
		}
	}()

	// 1. Initialize the Postgres connection pool
	// In a real application, these would be loaded from environment variables
	dbConfig := storage.Config{
		Host:     getEnvOrDefault("DB_HOST", "localhost"),
		Port:     getEnvOrDefault("DB_PORT", "5432"),
		User:     getEnvOrDefault("DB_USER", "postgres"),
		Password: getEnvOrDefault("DB_PASSWORD", "postgres"),
		Database: getEnvOrDefault("DB_NAME", "memory_os"),
	}

	logger.Info("connecting to database", "host", dbConfig.Host, "database", dbConfig.Database)

	db, err := storage.NewPostgresDB(ctx, dbConfig)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	logger.Info("database connection established")

	// 2. Initialize the Repositories
	pool := db.GetPool()
	chatRepo := storage.NewChatRepository(pool)
	if err := chatRepo.EnsureSchema(ctx); err != nil {
		logger.Error("failed to ensure chat schema", "error", err)
		os.Exit(1)
	}
	orchestratorRepo := storage.NewOrchestratorRepository(pool)
	if err := orchestratorRepo.EnsureSchema(ctx); err != nil {
		logger.Error("failed to ensure orchestrator schema", "error", err)
		os.Exit(1)
	}
	logRepo := storage.NewLogRepository(pool)
	vaultRepo := storage.NewVaultRepository(pool)
	agentLogsRepo := storage.NewAgentLogsRepository(pool)
	schedulerRepo := storage.NewSchedulerRepository(pool)

	// Initialize Vault Manager
	vaultManager, err := logic.NewVaultManager()
	if err != nil {
		logger.Error("failed to initialize vault manager", "error", err)
		os.Exit(1)
	}

	// Note: We'll implement a proper repository wrapper for Personal Info
	piRepo := &storage.BaseRepository{Pool: pool}

	var embedder logic.Embedder
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		logger.Info("using OpenAI embedder")
		embedder = logic.NewOpenAIEmbedder(apiKey)
	} else {
		logger.Warn("OPENAI_API_KEY not set, using local embedder")
		embedder = &logic.LocalEmbedder{}
	}
	piProcessor := logic.NewProcessor(piRepo, embedder)

	// 3. Initialize the Handlers
	chatHandler := api.NewChatHandler(chatRepo)
	orchestratorHandler := api.NewOrchestratorHandler(orchestratorRepo)
	logHandler := api.NewSystemLogHandler(logRepo)
	piHandler := api.NewPersonalInfoHandler(piProcessor, piRepo)
	vaultHandler := api.NewVaultHandler(vaultRepo, vaultManager, logRepo)
	agentHandler := api.NewAgentHandler(agentLogsRepo)
	scheduledJobsHandler := api.NewScheduledJobsHandler(schedulerRepo)

	// Set up the router using Go 1.22's enhanced mux
	mux := http.NewServeMux()

	docs.SwaggerInfo.Title = "Memory Service API"
	docs.SwaggerInfo.Description = "REST API for CerberOS memory, chat, personal info, system event, vault, and agent execution services."
	docs.SwaggerInfo.Version = "v1"
	docs.SwaggerInfo.BasePath = "/"
	docs.SwaggerInfo.Schemes = []string{"http"}

	// Healthz endpoint
	mux.HandleFunc("GET /api/v1/healthz", newHealthzHandler(db, logger))

	// Chat endpoints
	mux.HandleFunc("GET /api/v1/conversations", chatHandler.HandleListConversations)
	mux.HandleFunc("POST /api/v1/conversations", chatHandler.HandleCreateConversation)
	mux.HandleFunc("POST /api/v1/tasks", chatHandler.HandleCreateTask)
	mux.HandleFunc("GET /api/v1/tasks/{taskId}", chatHandler.HandleGetTask)
	mux.HandleFunc("POST /api/v1/chat/{conversationId}/messages", chatHandler.HandleCreateMessage)
	mux.HandleFunc("GET /api/v1/chat/{conversationId}/messages", chatHandler.HandleListMessages)

	// Orchestrator persistence endpoints (Internal Only)
	orchestratorMux := http.NewServeMux()
	orchestratorMux.HandleFunc("POST /api/v1/orchestrator/records", orchestratorHandler.HandleWriteRecord)
	orchestratorMux.HandleFunc("GET /api/v1/orchestrator/records", orchestratorHandler.HandleQueryRecords)
	orchestratorMux.HandleFunc("GET /api/v1/orchestrator/records/latest", orchestratorHandler.HandleReadLatest)
	mux.Handle("/api/v1/orchestrator/", http.StripPrefix("", api.RequireVaultKey(orchestratorMux)))

	// Personal Info endpoints
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/save", piHandler.Save)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/query", piHandler.Query)
	mux.HandleFunc("GET /api/v1/personal_info/{userId}/all", piHandler.GetAll)
	mux.HandleFunc("PUT /api/v1/personal_info/{userId}/facts/{factId}", piHandler.UpdateFact)
	mux.HandleFunc("DELETE /api/v1/personal_info/{userId}/facts/{factId}", piHandler.DeleteFact)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/facts/{factId}/archive", piHandler.ArchiveFact)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/facts/{factId}/supersede", piHandler.SupersedeFact)

	// System Log endpoints
	mux.HandleFunc("POST /api/v1/system/events", logHandler.HandleCreateSystemEvent)
	mux.HandleFunc("GET /api/v1/system/events", logHandler.HandleListSystemEvents)

	// Scheduled Jobs endpoints
	mux.HandleFunc("POST /api/v1/scheduled_jobs", scheduledJobsHandler.HandleCreateScheduledJob)
	mux.HandleFunc("POST /api/v1/scheduled_jobs/run_due", scheduledJobsHandler.HandleRunDueJobs)
	mux.HandleFunc("GET /api/v1/scheduled_jobs/{jobId}/runs", scheduledJobsHandler.HandleListScheduledJobRuns)

	// Vault endpoints (Internal Only)
	vaultMux := http.NewServeMux()
	vaultMux.HandleFunc("POST /api/v1/vault/{userId}/secrets", vaultHandler.HandleSaveSecret)
	vaultMux.HandleFunc("PUT /api/v1/vault/{userId}/secrets/{keyName}", vaultHandler.HandleUpdateSecret)
	vaultMux.HandleFunc("GET /api/v1/vault/{userId}/secrets", vaultHandler.HandleGetSecret)
	vaultMux.HandleFunc("DELETE /api/v1/vault/{userId}/secrets/{keyName}", vaultHandler.HandleDeleteSecret)
	mux.Handle("/api/v1/vault/", http.StripPrefix("", api.RequireVaultKey(vaultMux)))

	// Agent Log endpoints
	mux.HandleFunc("POST /api/v1/agent/{taskId}/executions", agentHandler.HandleCreateTaskExecution)
	mux.HandleFunc("GET /api/v1/agent/{taskId}/executions", agentHandler.HandleGetExecutions)
	// Legacy routes retained temporarily for backward compatibility.
	mux.HandleFunc("POST /api/v1/agents/tasks/{taskId}/executions", agentHandler.HandleCreateTaskExecution)
	mux.HandleFunc("GET /api/v1/agents/tasks/{taskId}/executions", agentHandler.HandleGetExecutions)

	// Metrics endpoint
	mux.Handle("/internal/metrics", promhttp.Handler())

	// Swagger UI endpoint
	mux.Handle("/swagger/", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"), //The url pointing to API definition
	))

	// 4. Start the HTTP server. otelhttp.NewHandler is the outermost wrapper
	// so server spans start before the trace-id middleware runs; the middleware
	// then reconciles the span's trace_id with the incoming `traceparent`.
	port := getEnvOrDefault("PORT", "8080")
	handler := api.TraceIDMiddleware(logger, logRepo, loggingMiddleware(logger, api.MetricsMiddleware(mux)))
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: telemetry.WrapHandler(handler, "memory-api"),
	}

	// Heartbeat emitter — non-fatal if NATS is unavailable or unset.
	// The orchestrator's heartbeat sweeper subscribes to
	// aegis.heartbeat.service.* and uses these beats to track memory's
	// liveness. See docs/heartbeat.md.
	if natsURL := os.Getenv("NATS_URL"); natsURL != "" {
		nc, err := nats.Connect(natsURL,
			nats.Name("memory-heartbeat"),
			nats.RetryOnFailedConnect(true),
			nats.MaxReconnects(-1),
			nats.ReconnectWait(500*time.Millisecond),
		)
		if err != nil {
			logger.Warn("heartbeat: NATS connect failed — liveness will not be published", "error", err)
		} else {
			defer nc.Close()
			emitter := heartbeat.New(nc, "memory", logger)
			go emitter.Start(ctx)
		}
	} else {
		logger.Info("heartbeat: NATS_URL unset — emitter disabled")
	}

	// Start server in a goroutine
	go func() {
		logger.Info("starting server", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal
	<-ctx.Done()
	logger.Info("shutting down gracefully...")

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped")
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// loggingMiddleware adds basic request logging
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a custom response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.statusCode,
			"duration", duration,
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
