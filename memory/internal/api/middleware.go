package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
)

type TraceIDKey struct{}

// MetricsMiddleware records HTTP request metrics
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()

		// Use r.URL.Path or a cleaned up version of it
		// In Go 1.22 mux, r.Pattern might be useful, but r.URL.Path is simpler for now
		path := r.URL.Path

		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rw.statusCode)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}

// TraceIDMiddleware resolves a TraceID for every request and adds it to the
// context. When the request carries a W3C `traceparent` header (from IO,
// Orchestrator, or Agents via otelhttp) its 32-char trace_id is reused so
// Memory spans nest under the upstream trace. When no `traceparent` is
// present a fresh UUID is generated for backward compatibility with legacy
// callers and direct curl requests.
//
// It also logs an 'ACCESS_GRANTED' event to the system_events table if the
// request is for the Vault.
func TraceIDMiddleware(logger *slog.Logger, logRepo *storage.LogRepository, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := extractTraceparentID(r.Header.Get("traceparent"))
		if traceID == "" {
			traceID = uuid.New().String()
		}
		ctx := context.WithValue(r.Context(), TraceIDKey{}, traceID)
		r = r.WithContext(ctx)

		// Create a custom response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
	})
}

// extractTraceparentID returns the 32-char trace_id segment of a W3C
// `traceparent` header (`00-<trace_id>-<span_id>-<flags>`) when it is
// well-formed and non-zero. Returns "" when the header is absent, malformed,
// or carries the all-zero trace_id which MUST be rejected per the spec.
func extractTraceparentID(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return ""
	}
	if len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return ""
	}
	tid := parts[1]
	if tid == "00000000000000000000000000000000" {
		return ""
	}
	return tid
}

// RequireVaultKey is a middleware that checks for the X-Internal-API-Key
// header and validates it against the INTERNAL_VAULT_API_KEY environment
// variable.
func RequireVaultKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedKey := os.Getenv("INTERNAL_VAULT_API_KEY")
		if expectedKey == "" {
			// If not set, deny everything to be safe
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ErrorResponse("internal", "Internal server configuration error", nil))
			return
		}

		apiKey := r.Header.Get("X-Internal-API-Key")
		if apiKey != expectedKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "Invalid or missing API Key", nil))
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
