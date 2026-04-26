package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestWithTraceID_SeedsOpenTelemetryTraceID(t *testing.T) {
	tidStr := "61067515-b057-4819-8aa7-7e7ff552df71"
	ctx := WithTraceID(context.Background(), tidStr)
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Fatal("expected valid span context from UUID trace id")
	}
	want, err := trace.TraceIDFromHex("61067515b05748198aa77e7ff552df71")
	if err != nil {
		t.Fatal(err)
	}
	if sc.TraceID() != want {
		t.Fatalf("TraceID: got %s want %s", sc.TraceID(), want)
	}
}

func TestWithTraceID_32HexNoDashes(t *testing.T) {
	hex := "61067515b05748198aa77e7ff552df71"
	ctx := WithTraceID(context.Background(), hex)
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Fatal("expected valid span context")
	}
	want, err := trace.TraceIDFromHex(hex)
	if err != nil {
		t.Fatal(err)
	}
	if sc.TraceID() != want {
		t.Fatalf("TraceID: got %s want %s", sc.TraceID(), want)
	}
}

func TestWithTraceID_NonHexSkipsOTEL(t *testing.T) {
	ctx := WithTraceID(context.Background(), "trace-abc-123")
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		t.Fatalf("expected no OTEL span context for non-hex id, got trace %s", sc.TraceID())
	}
	if got := TraceIDFrom(ctx); got != "trace-abc-123" {
		t.Fatalf("TraceIDFrom: got %q", got)
	}
}

func TestWithTraceID_WrongLengthSkipsOTEL(t *testing.T) {
	ctx := WithTraceID(context.Background(), "abcd")
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		t.Fatal("expected invalid span context for short id")
	}
}
