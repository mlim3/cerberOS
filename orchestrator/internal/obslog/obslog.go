// Package obslog provides JSON structured logging for the orchestrator (Loki-friendly).
package obslog

import (
	"log/slog"

	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
)

// Component is the canonical component name for orchestrator logs.
// Sub-units within the orchestrator are emitted as the `module` field.
const Component = "orchestrator"

// NewLogger returns a slog JSON logger with the canonical component attribute
// and the supplied module name.
func NewLogger(module string) *slog.Logger {
	return observability.LoggerWithModule(module)
}

// AppendTrace appends trace_id when non-empty (W3C 32-hex from I/O).
func AppendTrace(args []any, traceID string) []any {
	if traceID == "" {
		return args
	}
	return append(append([]any{}, args...), "trace_id", traceID)
}
