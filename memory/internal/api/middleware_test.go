package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
