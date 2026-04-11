package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// setupTestLogger replaces the global logger with one that writes to a buffer,
// then returns the buffer and a cleanup function.
func setupTestLogger(t *testing.T, format string) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(buf, opts)
	} else {
		h = slog.NewJSONHandler(buf, opts)
	}
	defaultLogger = slog.New(h)
	nodeID = "test-node"
	t.Cleanup(func() {
		defaultLogger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		nodeID = ""
	})
	return buf
}

func TestLogFromContext_WithAllIDs(t *testing.T) {
	buf := setupTestLogger(t, "json")

	ctx := context.Background()
	ctx = WithTraceID(ctx, "trace-abc-123")
	ctx = WithTaskID(ctx, "task-xyz-456")
	ctx = WithPlanID(ctx, "plan-789")
	ctx = WithSubtaskID(ctx, "sub-001")
	ctx = WithModule(ctx, "task_dispatcher")

	LogFromContext(ctx).Info("test message")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	mustHave := map[string]string{
		"component":  "orchestrator",
		"node_id":    "test-node",
		"trace_id":   "trace-abc-123",
		"task_id":    "task-xyz-456",
		"plan_id":    "plan-789",
		"subtask_id": "sub-001",
		"module":     "task_dispatcher",
		"msg":        "test message",
	}
	for k, want := range mustHave {
		got, ok := entry[k]
		if !ok {
			t.Errorf("field %q missing from log output", k)
			continue
		}
		if got != want {
			t.Errorf("field %q = %v, want %v", k, got, want)
		}
	}
}

func TestLogFromContext_EmptyContext(t *testing.T) {
	buf := setupTestLogger(t, "json")

	ctx := context.Background()
	LogFromContext(ctx).Info("empty context message")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	// Required fields must still be present.
	for _, k := range []string{"component", "node_id", "msg"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("field %q missing from log output with empty context", k)
		}
	}
	// Optional IDs should not appear when not set.
	for _, k := range []string{"trace_id", "task_id", "plan_id", "subtask_id", "module"} {
		if _, ok := entry[k]; ok {
			t.Errorf("field %q should not appear when not set in context", k)
		}
	}
}

func TestLogFromContext_NestedContext(t *testing.T) {
	buf := setupTestLogger(t, "json")

	ctx := context.Background()
	ctx = WithTraceID(ctx, "trace-root")
	ctx = WithTaskID(ctx, "task-parent")

	// Derive a child context with more IDs.
	childCtx := WithPlanID(ctx, "plan-child")
	childCtx = WithSubtaskID(childCtx, "sub-child")

	LogFromContext(childCtx).Info("nested context message")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, buf.String())
	}

	// Both parent and child IDs must appear.
	checks := map[string]string{
		"trace_id":   "trace-root",
		"task_id":    "task-parent",
		"plan_id":    "plan-child",
		"subtask_id": "sub-child",
	}
	for k, want := range checks {
		got, ok := entry[k]
		if !ok {
			t.Errorf("field %q missing in nested context", k)
			continue
		}
		if got != want {
			t.Errorf("field %q = %v, want %v", k, got, want)
		}
	}
}

func TestLogFromContext_JSONOutputHasRequiredFields(t *testing.T) {
	buf := setupTestLogger(t, "json")

	ctx := WithTraceID(context.Background(), "trace-field-check")
	LogFromContext(ctx).Warn("field check", "extra_key", "extra_value")

	line := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(line, "{") {
		t.Fatalf("expected JSON output, got: %s", line)
	}

	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}

	for _, required := range []string{"time", "level", "msg", "component", "trace_id"} {
		if _, ok := entry[required]; !ok {
			t.Errorf("required field %q missing from JSON log output", required)
		}
	}
}

func TestContextHelpers_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = WithTraceID(ctx, "t1")
	ctx = WithTaskID(ctx, "t2")
	ctx = WithPlanID(ctx, "t3")
	ctx = WithSubtaskID(ctx, "t4")
	ctx = WithModule(ctx, "t5")

	if got := TraceIDFrom(ctx); got != "t1" {
		t.Errorf("TraceIDFrom = %q, want %q", got, "t1")
	}
	if got := TaskIDFrom(ctx); got != "t2" {
		t.Errorf("TaskIDFrom = %q, want %q", got, "t2")
	}
	if got := PlanIDFrom(ctx); got != "t3" {
		t.Errorf("PlanIDFrom = %q, want %q", got, "t3")
	}
	if got := SubtaskIDFrom(ctx); got != "t4" {
		t.Errorf("SubtaskIDFrom = %q, want %q", got, "t4")
	}
	if got := ModuleFrom(ctx); got != "t5" {
		t.Errorf("ModuleFrom = %q, want %q", got, "t5")
	}
}
