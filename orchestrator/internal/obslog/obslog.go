// Package obslog provides JSON structured logging for the orchestrator (Loki-friendly).
package obslog

import (
	"log/slog"

	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
)

const Service = "orchestrator"

// NewLogger returns a slog JSON logger with service + component attributes.
func NewLogger(component string) *slog.Logger {
	return observability.LoggerWithComponent(component)
}

// AppendTrace appends trace_id when non-empty (W3C 32-hex from I/O).
func AppendTrace(args []any, traceID string) []any {
	if traceID == "" {
		return args
	}
	return append(append([]any{}, args...), "trace_id", traceID)
}
