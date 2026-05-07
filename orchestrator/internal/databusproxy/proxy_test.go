package databusproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_forwardsToMemoryPath(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	defer backend.Close()

	h := New(backend.URL + "/v1/memory")
	req := httptest.NewRequest(http.MethodGet, "/v1/databus/outbox/pending?limit=3", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/v1/memory/outbox/pending" {
		t.Fatalf("proxied path = %q, want /v1/memory/outbox/pending", gotPath)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "[]" {
		t.Fatalf("body = %q", body)
	}
}

func TestNew_invalidBaseURL(t *testing.T) {
	h := New("not-a-url")
	req := httptest.NewRequest(http.MethodGet, "/v1/databus/ping", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
