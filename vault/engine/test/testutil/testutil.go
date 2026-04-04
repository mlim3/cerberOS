// Package testutil provides shared test helpers for the vault engine test suite.
package testutil

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/inject"
	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// NewTestServer creates a fully wired httptest.Server with a MockSecretManager.
// It returns the server, the mock (for seeding/inspecting state), and a
// CaptureExporter that records all audit events.
func NewTestServer(t *testing.T) (*httptest.Server, *secretmanager.MockSecretManager, *CaptureExporter) {
	t.Helper()
	cap := &CaptureExporter{}
	auditor := audit.New(audit.NewJSONExporter(io.Discard), cap)
	manager := secretmanager.NewMockSecretManager(auditor)
	pp := preprocessor.New(manager, auditor)

	mux := http.NewServeMux()
	injHandler := &inject.Handler{PP: pp, Auditor: auditor}
	injHandler.Register(mux)
	secHandler := &secrets.Handler{Manager: manager, Auditor: auditor}
	secHandler.Register(mux)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, manager, cap
}

// PostJSON sends a POST request with JSON content type.
func PostJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// DoRequest sends an HTTP request with the given method and optional body.
func DoRequest(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// AssertStatus checks the response status code and fatals on mismatch.
func AssertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d", resp.StatusCode, want)
	}
}

// CaptureExporter implements audit.Exporter and records all events.
type CaptureExporter struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *CaptureExporter) Export(e audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

// Events returns a copy of all captured events.
func (c *CaptureExporter) Events() []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]audit.Event, len(c.events))
	copy(out, c.events)
	return out
}

// Reset clears captured events.
func (c *CaptureExporter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}

// FindByKind returns all events matching the given kind.
func (c *CaptureExporter) FindByKind(kind audit.EventKind) []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []audit.Event
	for _, e := range c.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}
