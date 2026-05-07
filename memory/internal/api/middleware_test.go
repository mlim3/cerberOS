package api

import (
	"context"
	"encoding/json"
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

func TestExtractTraceparentID(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "valid traceparent",
			header: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			want:   "4bf92f3577b34da6a3ce929d0e0e4736",
		},
		{
			name:   "empty header",
			header: "",
			want:   "",
		},
		{
			name:   "malformed parts",
			header: "00-not-enough-parts",
			want:   "",
		},
		{
			name:   "all zero trace id rejected",
			header: "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
			want:   "",
		},
		{
			name:   "wrong trace id length",
			header: "00-1234-00f067aa0ba902b7-01",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractTraceparentID(tt.header); got != tt.want {
				t.Fatalf("extractTraceparentID(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestRequireVaultKey(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	t.Run("missing configured key returns internal envelope", func(t *testing.T) {
		t.Setenv("INTERNAL_VAULT_API_KEY", "")

		req := httptest.NewRequest(http.MethodGet, "/vault", nil)
		rec := httptest.NewRecorder()
		RequireVaultKey(next).ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}

		var env ResponseEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if env.Ok || env.Error == nil || env.Error.Code != "internal" {
			t.Fatalf("unexpected envelope: %+v", env)
		}
	})

	t.Run("missing request key returns unauthorized envelope", func(t *testing.T) {
		t.Setenv("INTERNAL_VAULT_API_KEY", "secret")

		req := httptest.NewRequest(http.MethodGet, "/vault", nil)
		rec := httptest.NewRecorder()
		RequireVaultKey(next).ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("valid request key reaches next handler", func(t *testing.T) {
		t.Setenv("INTERNAL_VAULT_API_KEY", "secret")

		req := httptest.NewRequest(http.MethodGet, "/vault", nil)
		req.Header.Set("X-Internal-API-Key", "secret")
		rec := httptest.NewRecorder()
		RequireVaultKey(next).ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
	})
}
