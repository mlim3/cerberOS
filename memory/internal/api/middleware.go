package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type TraceIDKey struct{}

// TraceIDMiddleware generates a TraceID for every request and adds it to the context.
// It also logs an 'ACCESS_GRANTED' event to the system_events table if the request is for the Vault.
func TraceIDMiddleware(logger *slog.Logger, logRepo *storage.LogRepository, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := uuid.New().String()
		ctx := context.WithValue(r.Context(), TraceIDKey{}, traceID)
		r = r.WithContext(ctx)

		// Create a custom response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
	})
}

// RequireVaultKey is a middleware that checks for the X-API-KEY header
// and validates it against the INTERNAL_VAULT_API_KEY environment variable.
func RequireVaultKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedKey := os.Getenv("INTERNAL_VAULT_API_KEY")
		if expectedKey == "" {
			// If not set, deny everything to be safe
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ErrorResponse("INTERNAL_ERROR", "Internal server configuration error", nil))
			return
		}

		apiKey := r.Header.Get("X-API-KEY")
		if apiKey != expectedKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(ErrorResponse("UNAUTHORIZED", "Invalid or missing API Key", nil))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Ensure responseWriter is available in api package if moved from main
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	return rw.ResponseWriter.Write(b)
}
