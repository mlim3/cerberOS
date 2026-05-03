package http

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyToNATSMonitoring_NonGET(t *testing.T) {
	proxy := ProxyToNATSMonitoring("http://localhost:8222")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/varz", nil)
	proxy(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: want 405, got %d", w.Code)
	}
}

func TestProxyToNATSMonitoring_GET(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"server_id":"test"}`))
	}))
	defer backend.Close()

	proxy := ProxyToNATSMonitoring(backend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/varz", nil)
	proxy(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET: want 200, got %d", w.Code)
	}
	if w.Body.String() != `{"server_id":"test"}` {
		t.Errorf("proxy body: got %q", w.Body.String())
	}
}

func TestProxyToNATSMonitoring_QueryString(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "subs=1" {
			t.Errorf("expected query subs=1, got %q", r.URL.RawQuery)
		}
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	proxy := ProxyToNATSMonitoring(backend.URL)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/connz?subs=1", nil)
	proxy(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET with query: want 200, got %d", w.Code)
	}
}
