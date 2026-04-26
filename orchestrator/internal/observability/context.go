// Package observability provides structured logging and distributed tracing
// for the Orchestrator (EDD §15).
//
// Usage pattern:
//
//	ctx = observability.WithTraceID(ctx, traceID)
//	ctx = observability.WithTaskID(ctx, task.TaskID)
//	ctx = observability.WithModule(ctx, "task_dispatcher")
//	log := observability.LogFromContext(ctx)
//	log.Info("task received", "priority", task.Priority)
package observability

import "context"

// ctxKey is an unexported type for context keys in this package.
type ctxKey int

const (
	traceIDKey        ctxKey = iota // trace_id — root correlation ID across all components
	taskIDKey                       // task_id — user-facing task identifier
	conversationIDKey               // conversation_id — stable thread ID linking tasks in the same chat
	planIDKey                       // plan_id — execution plan identifier
	subtaskIDKey                    // subtask_id — individual subtask identifier
	moduleKey                       // module — name of the module generating the log line
)

// WithTraceID returns a context carrying the given trace ID.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// WithTaskID returns a context carrying the given task ID.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey, id)
}

// WithConversationID returns a context carrying the given conversation ID.
// Empty IDs are dropped so callers can pass through optional values without a
// guard at every call site.
func WithConversationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, conversationIDKey, id)
}

// WithPlanID returns a context carrying the given plan ID.
func WithPlanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, planIDKey, id)
}

// WithSubtaskID returns a context carrying the given subtask ID.
func WithSubtaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, subtaskIDKey, id)
}

// WithModule returns a context carrying the given module name.
func WithModule(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, moduleKey, name)
}

// TraceIDFrom extracts the trace ID from the context, or "" if not set.
func TraceIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(traceIDKey).(string)
	return v
}

// TaskIDFrom extracts the task ID from the context, or "" if not set.
func TaskIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(taskIDKey).(string)
	return v
}

// ConversationIDFrom extracts the conversation ID from the context, or "" if not set.
func ConversationIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(conversationIDKey).(string)
	return v
}

// PlanIDFrom extracts the plan ID from the context, or "" if not set.
func PlanIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(planIDKey).(string)
	return v
}

// SubtaskIDFrom extracts the subtask ID from the context, or "" if not set.
func SubtaskIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(subtaskIDKey).(string)
	return v
}

// ModuleFrom extracts the module name from the context, or "" if not set.
func ModuleFrom(ctx context.Context) string {
	v, _ := ctx.Value(moduleKey).(string)
	return v
}
