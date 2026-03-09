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
	"github.com/mlim3/cerberOS/memory/internal/api"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	httpSwagger "github.com/swaggo/http-swagger"
)

func main() {
	// Initialize structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	logRepo := storage.NewLogRepository(pool)
	vaultRepo := storage.NewVaultRepository(pool)
	agentLogsRepo := storage.NewAgentLogsRepository(pool)

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
		logger.Info("OPENAI_API_KEY not set, falling back to MockEmbedder")
		embedder = &logic.MockEmbedder{}
	}

	piProcessor := logic.NewProcessor(piRepo, embedder)

	// 3. Initialize the Handlers
	chatHandler := api.NewChatHandler(chatRepo)
	logHandler := api.NewSystemLogHandler(logRepo)
	piHandler := api.NewPersonalInfoHandler(piProcessor, piRepo)
	vaultHandler := api.NewVaultHandler(vaultRepo, vaultManager, logRepo)
	agentHandler := api.NewAgentHandler(agentLogsRepo)

	// Set up the router using Go 1.22's enhanced mux
	mux := http.NewServeMux()

	// Healthz endpoint
	mux.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
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
	})

	// Chat endpoints
	mux.HandleFunc("POST /api/v1/chat/{sessionId}/messages", chatHandler.HandleCreateMessage)
	mux.HandleFunc("GET /api/v1/chat/{sessionId}/messages", chatHandler.HandleListMessages)

	// Personal Info endpoints
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/save", piHandler.Save)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/query", piHandler.Query)
	mux.HandleFunc("GET /api/v1/personal_info/{userId}/all", piHandler.GetAll)
	mux.HandleFunc("PUT /api/v1/personal_info/{userId}/facts/{factId}", piHandler.UpdateFact)
	mux.HandleFunc("DELETE /api/v1/personal_info/{userId}/facts/{factId}", piHandler.DeleteFact)

	// System Log endpoints
	mux.HandleFunc("POST /api/v1/system/events", logHandler.HandleCreateSystemEvent)
	mux.HandleFunc("GET /api/v1/system/events", logHandler.HandleListSystemEvents)

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
	// Serve static files for swagger docs
	mux.Handle("/swagger/doc.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./docs/swagger.json")
	}))

	// 4. Start the HTTP server
	port := getEnvOrDefault("PORT", "8080")
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: api.TraceIDMiddleware(logger, logRepo, loggingMiddleware(logger, api.MetricsMiddleware(mux))),
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
