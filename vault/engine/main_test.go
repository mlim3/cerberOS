package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/handlers/inject"
	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

func newTestHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	auditor := audit.New(audit.NewJSONExporter(io.Discard))
	manager := secretmanager.NewMockSecretManager(auditor)
	pp := preprocessor.New(manager, auditor)
	mux := http.NewServeMux()
	injHandler := &inject.Handler{PP: pp, Auditor: auditor}
	injHandler.Register(mux)
	secHandler := &secrets.Handler{Manager: manager, Auditor: auditor}
	secHandler.Register(mux)
	return httptest.NewServer(mux)
}

func TestSecretGet_OK(t *testing.T) {
	ts := newTestHTTPServer(t)
	t.Cleanup(ts.Close)

	body := `{"agent":"test-agent","keys":["API_KEY"]}`
	resp, err := http.Post(ts.URL+"/secrets/get", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out secrets.SecretGetResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Secrets["API_KEY"] != "mock-api-key-12345" {
		t.Fatalf("secrets: %#v", out.Secrets)
	}
}

func TestSecretGet_MissingKey_403(t *testing.T) {
	ts := newTestHTTPServer(t)
	t.Cleanup(ts.Close)

	body := `{"agent":"a","keys":["NONEXISTENT"]}`
	resp, err := http.Post(ts.URL+"/secrets/get", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var er common.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		t.Fatal(err)
	}
	if er.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestSecretPut_ThenGet(t *testing.T) {
	ts := newTestHTTPServer(t)
	t.Cleanup(ts.Close)

	putBody := `{"agent":"a","key":"EPHEMERAL","value":"stored-value"}`
	putResp, err := http.Post(ts.URL+"/secrets/put", "application/json", bytes.NewReader([]byte(putBody)))
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("put status %d", putResp.StatusCode)
	}

	getResp, err := http.Post(ts.URL+"/secrets/get", "application/json", bytes.NewReader([]byte(`{"agent":"a","keys":["EPHEMERAL"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status %d", getResp.StatusCode)
	}
	var out secrets.SecretGetResponse
	if err := json.NewDecoder(getResp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Secrets["EPHEMERAL"] != "stored-value" {
		t.Fatalf("got %#v", out.Secrets)
	}
}

func TestSecretDelete_ThenGetFails(t *testing.T) {
	ts := newTestHTTPServer(t)
	t.Cleanup(ts.Close)

	key := "API_KEY"
	delBody := `{"agent":"a","key":"` + key + `"}`
	delResp, err := http.Post(ts.URL+"/secrets/delete", "application/json", bytes.NewReader([]byte(delBody)))
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", delResp.StatusCode)
	}

	getResp, err := http.Post(ts.URL+"/secrets/get", "application/json", bytes.NewReader([]byte(`{"agent":"a","keys":["API_KEY"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusForbidden {
		t.Fatalf("get status %d want 403", getResp.StatusCode)
	}
}

func TestSecrets_WrongMethod_405(t *testing.T) {
	ts := newTestHTTPServer(t)
	t.Cleanup(ts.Close)

	for _, path := range []string{"/secrets/get", "/secrets/put", "/secrets/delete"} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d want 405", path, resp.StatusCode)
		}
	}
}

func TestSecrets_MalformedJSON_400(t *testing.T) {
	ts := newTestHTTPServer(t)
	t.Cleanup(ts.Close)

	for _, path := range []string{"/secrets/get", "/secrets/put", "/secrets/delete"} {
		resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader([]byte(`{`)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d want 400", path, resp.StatusCode)
		}
	}
}
