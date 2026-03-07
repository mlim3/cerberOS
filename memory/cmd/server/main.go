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

	"github.com/mlim3/cerberOS/memory/internal/api"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

func main() {
	// Initialize structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Create root context that listens for signals
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
	
	// Note: We'll implement a proper repository wrapper for Personal Info
	piRepo := &storage.BaseRepository{Pool: pool}
	mockEmbedder := &logic.MockEmbedder{}
	piProcessor := logic.NewProcessor(piRepo, mockEmbedder)

	// 3. Initialize the Handlers
	chatHandler := api.NewChatHandler(chatRepo)
	logHandler := api.NewSystemLogHandler(logRepo)
	piHandler := api.NewPersonalInfoHandler(piProcessor, piRepo)

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

	// System Log endpoints
	mux.HandleFunc("POST /api/v1/system/events", logHandler.HandleCreateSystemEvent)
	mux.HandleFunc("GET /api/v1/system/events", logHandler.HandleListSystemEvents)

	// 4. Start the HTTP server
	port := getEnvOrDefault("PORT", "8080")
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", port),
		Handler: loggingMiddleware(logger, mux),
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
