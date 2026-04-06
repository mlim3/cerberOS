// Package telemetry configures OpenTelemetry when OTEL_EXPORTER_OTLP_ENDPOINT (or OTLP defaults) is set.
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

// Tracer returns the aegis-databus tracer (no-op until Init sets a real provider).
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

const tracerName = "aegis-databus"

// Enabled reports whether OTLP trace export is configured (endpoint env or default discovery).
func Enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}

// Init installs a global TracerProvider and W3C propagator when OTLP is configured.
// Call shutdown on process exit.
func Init(ctx context.Context) (shutdown func(context.Context) error, err error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// HTTPRoundTripper wraps the transport with otelhttp when telemetry is enabled.
func HTTPRoundTripper(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if !Enabled() {
		return rt
	}
	return otelhttp.NewTransport(rt)
}
