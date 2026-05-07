package observability

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
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

// LoggerWithModule returns the default orchestrator logger with the canonical
// component=orchestrator attribute and the supplied module name. Prefer
// LogFromContext when context IDs are available.
func LoggerWithModule(module string) *slog.Logger {
	attrs := []any{"component", "orchestrator", "module", module}
	if nodeID != "" {
		attrs = append(attrs, "node_id", nodeID)
	}
	return defaultLogger.With(attrs...)
}

// LoggerWithComponent is a deprecated alias for LoggerWithModule kept so older
// call sites compile. Its argument is the module name within the orchestrator
// component, despite the legacy name.
//
// Deprecated: use LoggerWithModule.
func LoggerWithComponent(module string) *slog.Logger {
	return LoggerWithModule(module)
}

// LogFromContext returns a *slog.Logger pre-populated with all IDs present in ctx.
//
// Required fields on every line (EDD §15.1):
//   - component       = "orchestrator"
//   - node_id         (from InitLogger)
//   - trace_id        (from context, if set)
//   - task_id         (from context, if set)
//   - conversation_id (from context, if set)
//   - plan_id         (from context, if set)
//   - subtask_id      (from context, if set)
//   - module          (from context, if set)
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
	if v := ConversationIDFrom(ctx); v != "" {
		attrs = append(attrs, "conversation_id", v)
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
	// Note: when the context carries no module, we still emit component=orchestrator
	// so every line has the canonical component label even if the caller has not
	// chosen a module yet.
	base := []any{"component", "orchestrator"}
	if nodeID != "" {
		base = append(base, "node_id", nodeID)
	}
	return defaultLogger.With(base...).With(attrs...)
}

// PreviewWords truncates user-supplied text into a debug-safe preview suitable
// for short metadata fields (titles, reasons, error codes, progress messages,
// voice transcripts).
//
// Caps at maxWords words AND maxChars characters (whichever is hit first) and
// appends "…" when truncation occurred. Whitespace is collapsed and trimmed so
// multi-line input renders as a single readable string.
//
// For long *conversation* content (user chat messages, agent replies), prefer
// PreviewHeadTail — it keeps the start AND the end so the line remains
// recognisable when the message is many paragraphs long. See docs/logging.md
// for the full policy.
func PreviewWords(s string, maxWords, maxChars int) string {
	if maxWords <= 0 {
		maxWords = 20
	}
	if maxChars <= 0 {
		maxChars = 140
	}
	flat := strings.Join(strings.Fields(s), " ")
	if flat == "" {
		return ""
	}
	words := strings.Split(flat, " ")
	truncated := false
	out := flat
	if len(words) > maxWords {
		out = strings.Join(words[:maxWords], " ")
		truncated = true
	}
	if utf8.RuneCountInString(out) > maxChars {
		runes := []rune(out)
		out = strings.TrimRight(string(runes[:maxChars]), " ")
		truncated = true
	}
	if truncated {
		return out + "…"
	}
	return out
}

// PreviewHeadTail builds a debug-safe head+tail preview suitable for long
// conversation messages — typically content_preview (user → agent) and
// result_preview (agent → user).
//
// Format: "<first headWords words> [..N chars..] <last tailWords words>",
// where N is the number of characters omitted from the middle. When the
// string is short enough that head+tail would already cover the whole value,
// the original (whitespace-collapsed) string is returned unchanged.
//
// The motivation is debugger UX: a user might paste a long document and put
// the actual question on the last line; a beginning-only preview would hide
// it. Head+tail also makes the same message recognisable across io,
// orchestrator, and agent logs.
func PreviewHeadTail(s string, headWords, tailWords int) string {
	if headWords <= 0 {
		headWords = 15
	}
	if tailWords <= 0 {
		tailWords = 10
	}
	flat := strings.Join(strings.Fields(s), " ")
	if flat == "" {
		return ""
	}
	words := strings.Split(flat, " ")
	if len(words) <= headWords+tailWords {
		return flat
	}
	head := strings.Join(words[:headWords], " ")
	tail := strings.Join(words[len(words)-tailWords:], " ")
	omitted := utf8.RuneCountInString(flat) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if omitted <= 0 {
		return flat
	}
	return head + " [.." + strconv.Itoa(omitted) + " chars..] " + tail
}
