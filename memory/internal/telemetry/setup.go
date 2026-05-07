// Package telemetry configures OpenTelemetry for the Memory service when
// OTEL_EXPORTER_OTLP_ENDPOINT (or the OTLP traces override) is set. When no
// endpoint is configured every call is a safe no-op so local dev without
// Tempo is unaffected.
package telemetry

import (
	"context"
	"net/http"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "memory-api"

// Tracer returns the Memory service tracer. Before Init installs a real
// provider this is a no-op tracer, so callers never need to nil-check.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Enabled reports whether OTLP trace export is configured via the standard
// env vars.
func Enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// Init installs a global TracerProvider and the W3C trace-context propagator
// when OTLP is configured. The returned shutdown function must be called on
// process exit to flush pending spans.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// WrapHandler wraps h with otelhttp so every inbound HTTP request produces a
// server span named operation. When telemetry is disabled h is returned
// unchanged.
func WrapHandler(h http.Handler, operation string) http.Handler {
	if !Enabled() {
		return h
	}
	return otelhttp.NewHandler(h, operation)
}
