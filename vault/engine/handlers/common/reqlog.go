// Package common holds vault HTTP handler helpers shared across handler packages.
package common

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// RequestIDHeader is the canonical incoming-request correlation header.
const RequestIDHeader = "X-Request-ID"

// NewRequestID returns a 16-byte random hex string suitable as a request_id
// when the caller did not provide one. Falls back to a time-based identifier
// if the system RNG is somehow unavailable.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return hex.EncodeToString(b[:])
}

// RequestLogger derives a request-scoped *slog.Logger annotated with the
// canonical correlation/HTTP fields. base should already carry
// component/module (typically component=vault, module=http); when base is
// nil (e.g. test setups that don't wire a logger) the helper falls back to
// slog.Default with explicit component/module so logs still validate
// against docs/logging.md. The returned request_id is the one logged so
// callers can echo it back to the client if they wish.
//
// Per docs/logging.md: never log raw credential values, secret values,
// permission tokens, or operation results — only metadata.
func RequestLogger(base *slog.Logger, r *http.Request, operationType string) (*slog.Logger, string) {
	requestID := r.Header.Get(RequestIDHeader)
	if requestID == "" {
		requestID = NewRequestID()
	}
	if base == nil {
		base = slog.Default().With("component", "vault", "module", "http")
	}
	logger := base.With(
		"request_id", requestID,
		"operation_type", operationType,
		"method", r.Method,
		"path", r.URL.Path,
	)
	if traceID := r.Header.Get("X-Trace-ID"); traceID != "" {
		logger = logger.With("trace_id", traceID)
	}
	return logger, requestID
}
