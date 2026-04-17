package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("aegis-orchestrator")

// InitTracer sets up the OTLP gRPC trace exporter and registers it as the
// global OTel TracerProvider. Call once from main.go on startup.
//
// Returns a shutdown function that must be called on graceful exit to flush
// pending spans. The orchestrator continues to start even if Tempo is
// unreachable — the OTLP exporter retries in the background.
func InitTracer(ctx context.Context, endpoint string, nodeID string) (func(context.Context) error, error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("orchestrator"),
			semconv.ServiceInstanceID(nodeID),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	tracer = tp.Tracer("aegis-orchestrator")

	return tp.Shutdown, nil
}

// StartSpan starts a new child span named name under the current span in ctx.
// The caller MUST call span.End() (typically via defer).
//
// Example:
//
//	ctx, span := observability.StartSpan(ctx, "policy_validation")
//	defer span.End()
func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return tracer.Start(ctx, name)
}

// SpanSetTaskAttributes attaches standard task-level attributes to a span.
// Call immediately after StartSpan for task-scoped spans.
func SpanSetTaskAttributes(span trace.Span, taskID, userID string) {
	span.SetAttributes(
		attribute.String("task_id", taskID),
		attribute.String("user_id", userID),
	)
}

// SpanRecordError marks a span as errored and records the error.
// Returns err unchanged so it can be used inline.
func SpanRecordError(span trace.Span, err error) error {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
