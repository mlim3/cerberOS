package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestResolveRequestTraceID_UI(t *testing.T) {
	fixed := uuid.MustParse("12345678-abcd-abcd-abcd-123456789abc")
	w3cTraceHex := "12345678901234567890123456789012" // 32 hex (W3C trace_id)
	tp := "00-" + w3cTraceHex + "-0123456789abcdef-01"

	t.Run("X-Trace-ID wins when both headers set", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(traceIDHeader, fixed.String())
		req.Header.Set(traceparentHeader, "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
		got := resolveRequestTraceID(req)
		if got != fixed {
			t.Fatalf("got %v want %v", got, fixed)
		}
	})

	t.Run("traceparent when no X-Trace-ID", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(traceparentHeader, tp)
		got := resolveRequestTraceID(req)
		want, err := uuid.Parse(w3cTraceHex)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got %v want %v", got, want)
		}
	})

	t.Run("invalid X-Trace-ID falls through to traceparent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(traceIDHeader, "not-a-uuid")
		req.Header.Set(traceparentHeader, tp)
		got := resolveRequestTraceID(req)
		want, err := uuid.Parse(w3cTraceHex)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got %v want %v", got, want)
		}
	})

	t.Run("no headers generates new id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		got := resolveRequestTraceID(req)
		if got == uuid.Nil {
			t.Fatal("expected non-nil trace id")
		}
	})
}

func TestTraceIDFromContext_backgroundNoTrace(t *testing.T) {
	_, ok := traceIDFromContext(context.Background())
	if ok {
		t.Fatal("background context should not carry a trace (cron / job worker pattern)")
	}
}

func TestTraceIDMiddleware_UIPropagatesHeaders(t *testing.T) {
	fixed := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	h := TraceIDMiddleware(slog.Default(), nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, ok := traceIDFromContext(r.Context())
		if !ok || tid != fixed {
			t.Errorf("context trace id = %v ok=%v want %v", tid, ok, fixed)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
	req.Header.Set(traceIDHeader, fixed.String())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get(traceIDHeader); got != fixed.String() {
		t.Errorf("response %s = %q want %q", traceIDHeader, got, fixed.String())
	}
}

func TestTraceIDMiddleware_cronOrInternalRequestInitializesTrace(t *testing.T) {
	var seen uuid.UUID
	h := TraceIDMiddleware(slog.Default(), nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, ok := traceIDFromContext(r.Context())
		if !ok {
			t.Fatal("middleware must always attach a trace id")
		}
		seen = tid
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/cron", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen == uuid.Nil {
		t.Fatal("expected generated trace id")
	}
	if hdr := rr.Header().Get(traceIDHeader); hdr != seen.String() {
		t.Errorf("response header %q context id %q", hdr, seen.String())
	}
}
