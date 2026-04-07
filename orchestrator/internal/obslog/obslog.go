// Package obslog provides JSON structured logging for the orchestrator (Loki-friendly).
package obslog

import (
	"log/slog"
	"os"
)

const Service = "orchestrator"

// NewLogger returns a slog JSON logger with service + component attributes.
func NewLogger(component string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With("service", Service, "component", component)
}

// AppendTrace appends trace_id when non-empty (W3C 32-hex from I/O).
func AppendTrace(args []any, traceID string) []any {
	if traceID == "" {
		return args
	}
	return append(append([]any{}, args...), "trace_id", traceID)
}
