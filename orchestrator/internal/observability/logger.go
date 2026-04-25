package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// nodeID is the node identity set at startup via InitLogger.
var nodeID string

// defaultLogger is the global structured logger, initialized by InitLogger.
var defaultLogger *slog.Logger

func init() {
	// Provide a safe default so tests that don't call InitLogger still get a valid logger.
	defaultLogger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// InitLogger sets up the global structured logger.
// Must be called once from main.go before any log output.
//
//   - level:  "debug" | "info" | "warn" | "warning" | "error" | "fatal" | "critical" (default: "info")
//   - format: accepted for backward compatibility; logs are always JSON
//   - node:   NODE_ID value for the node_id field
func InitLogger(level, format, node string) {
	_ = format
	nodeID = node

	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error", "fatal", "critical":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	defaultLogger = slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

// LoggerWithComponent returns the default orchestrator logger with canonical
// service/component attrs. Prefer LogFromContext when context IDs are available.
func LoggerWithComponent(component string) *slog.Logger {
	attrs := []any{"service", "orchestrator", "component", component}
	if nodeID != "" {
		attrs = append(attrs, "node_id", nodeID)
	}
	return defaultLogger.With(attrs...)
}

// LogFromContext returns a *slog.Logger pre-populated with all IDs present in ctx.
//
// Required fields on every line (EDD §15.1):
//   - component = "orchestrator"
//   - node_id   (from InitLogger)
//   - trace_id  (from context, if set)
//   - task_id   (from context, if set)
//   - plan_id   (from context, if set)
//   - subtask_id (from context, if set)
//   - module    (from context, if set)
//
// FORBIDDEN log content (EDD §15.1):
//   - raw user input (payload.raw_input)
//   - credential values
//   - task result payloads
//   - planner output
func LogFromContext(ctx context.Context) *slog.Logger {
	attrs := []any{}
	if v := TraceIDFrom(ctx); v != "" {
		attrs = append(attrs, "trace_id", v)
	}
	if v := TaskIDFrom(ctx); v != "" {
		attrs = append(attrs, "task_id", v)
	}
	if v := PlanIDFrom(ctx); v != "" {
		attrs = append(attrs, "plan_id", v)
	}
	if v := SubtaskIDFrom(ctx); v != "" {
		attrs = append(attrs, "subtask_id", v)
	}
	if v := ModuleFrom(ctx); v != "" {
		attrs = append(attrs, "module", v)
	}
	return LoggerWithComponent("orchestrator").With(attrs...)
}
