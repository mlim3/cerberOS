// Package telemetry configures OpenTelemetry for the Agents component when
// OTEL_EXPORTER_OTLP_ENDPOINT (or the OTLP traces override) is set. When no
// endpoint is configured every call in this package is a safe no-op so local
// development without Tempo continues to work unchanged.
package telemetry

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "aegis-agents"

// Tracer returns the Agents-component tracer. Before Init installs a real
// provider this is a no-op tracer, so callers never need to nil-check.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// Enabled reports whether OTLP export is configured via the standard env vars.
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

// HTTPRoundTripper wraps rt with otelhttp so outbound HTTP calls inject
// `traceparent` when telemetry is enabled.
func HTTPRoundTripper(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if !Enabled() {
		return rt
	}
	return otelhttp.NewTransport(rt)
}

// ContextWithTraceID seeds ctx with a span context built from a 32-char hex
// trace_id carried on an inbound TaskSpec (or similar envelope). This lets an
// in-process span continue an upstream trace even when the inbound transport
// is NATS (which does not carry W3C traceparent headers). A random span_id is
// minted for the child span so the parent/child relationship is well-formed.
//
// If traceID is not a valid 32-char hex string, ctx is returned unchanged.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	if len(traceID) != 32 {
		return ctx
	}
	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return ctx
	}
	var sid trace.SpanID
	if _, err := rand.Read(sid[:]); err != nil {
		return ctx
	}
	if !sid.IsValid() {
		// Extremely unlikely but required by the SDK contract.
		sid[0] = 0x01
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}
