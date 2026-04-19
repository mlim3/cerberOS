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

const (
	traceIDHeader     = "X-Trace-ID"
	traceparentHeader = "traceparent"
)

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

// TraceIDMiddleware generates a TraceID for every request and adds it to the context.
// It also logs an 'ACCESS_GRANTED' event to the system_events table if the request is for the Vault.
func TraceIDMiddleware(logger *slog.Logger, logRepo *storage.LogRepository, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := resolveRequestTraceID(r)
		ctx := context.WithValue(r.Context(), TraceIDKey{}, traceID.String())
		r = r.WithContext(ctx)
		w.Header().Set(traceIDHeader, traceID.String())

		// Create a custom response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
	})
}

func resolveRequestTraceID(r *http.Request) uuid.UUID {
	if fromTraceID, ok := normalizeTraceID(r.Header.Get(traceIDHeader)); ok {
		return fromTraceID
	}
	if fromTraceparent, ok := traceIDFromTraceparent(r.Header.Get(traceparentHeader)); ok {
		return fromTraceparent
	}
	return uuid.New()
}

func traceIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	traceIDStr, ok := ctx.Value(TraceIDKey{}).(string)
	if !ok {
		return uuid.UUID{}, false
	}
	return normalizeTraceID(traceIDStr)
}

func normalizeTraceID(raw string) (uuid.UUID, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return uuid.UUID{}, false
	}
	return id, true
}

// traceparent format: version-traceid-parentid-flags
func traceIDFromTraceparent(header string) (uuid.UUID, bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 {
		return uuid.UUID{}, false
	}
	return normalizeTraceID(parts[1])
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
			json.NewEncoder(w).Encode(ErrorResponse("internal", "Internal server configuration error", nil))
			return
		}

		apiKey := r.Header.Get("X-API-KEY")
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
