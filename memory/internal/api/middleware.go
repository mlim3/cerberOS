package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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

		// Check if it's a Vault request
		if strings.HasPrefix(r.URL.Path, "/api/v1/vault") {
			// Log ACCESS_GRANTED event
			eventID, _ := uuid.NewRandom()
			traceUUID, _ := uuid.Parse(traceID)
			
			now := pgtype.Timestamptz{}
			now.Valid = true
			now.Time = time.Now()

			_, err := logRepo.CreateSystemEvent(ctx, storage.CreateSystemEventParams{
				ID: pgtype.UUID{Bytes: eventID, Valid: true},
				TraceID: pgtype.UUID{Bytes: traceUUID, Valid: true},
				ServiceName: pgtype.Text{String: "VaultService", Valid: true},
				Severity: pgtype.Text{String: "INFO", Valid: true},
				Message: "ACCESS_GRANTED",
				Metadata: []byte(`{"path": "` + r.URL.Path + `"}`),
				CreatedAt: now,
			})

			if err != nil {
				logger.Error("failed to log vault access event", "error", err, "traceID", traceID)
			}
		}

		// Create a custom response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
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
