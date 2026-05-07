package endpoint_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/test/testutil"
)

func TestSecretDelete(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		{
			name:       "WrongMethod_GET_405",
			method:     http.MethodGet,
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "MalformedJSON_400",
			method:     http.MethodPost,
			body:       `{bad`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "EmptyBody_400",
			method:     http.MethodPost,
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _, _ := testutil.NewTestServer(t)

			resp := testutil.DoRequest(t, tt.method, ts.URL+"/secrets/delete", tt.body)
			resp.Body.Close()
			testutil.AssertStatus(t, resp, tt.wantStatus)
		})
	}
}

func TestSecretDelete_ExistingKey(t *testing.T) {
	ts, _, _ := testutil.NewTestServer(t)

	// Delete a seeded key
	resp := testutil.PostJSON(t, ts.URL+"/secrets/delete", `{"agent":"a","key":"API_KEY"}`)
	resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	// Verify it's gone
	resp = testutil.PostJSON(t, ts.URL+"/secrets/get", `{"agent":"a","keys":["API_KEY"]}`)
	resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusForbidden)
}

func TestSecretDelete_NonexistentKey(t *testing.T) {
	ts, _, _ := testutil.NewTestServer(t)

	// Delete a key that was never set — mock silently no-ops
	resp := testutil.PostJSON(t, ts.URL+"/secrets/delete", `{"agent":"a","key":"GHOST"}`)
	resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusNoContent)
}

func TestSecretDelete_ThenPut(t *testing.T) {
	ts, _, _ := testutil.NewTestServer(t)

	// Delete API_KEY
	resp := testutil.PostJSON(t, ts.URL+"/secrets/delete", `{"agent":"a","key":"API_KEY"}`)
	resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	// Re-create with a new value
	resp = testutil.PostJSON(t, ts.URL+"/secrets/put", `{"agent":"a","key":"API_KEY","value":"resurrected"}`)
	resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	// Verify new value
	resp = testutil.PostJSON(t, ts.URL+"/secrets/get", `{"agent":"a","keys":["API_KEY"]}`)
	defer resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusOK)

	var out secrets.SecretGetResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Secrets["API_KEY"] != "resurrected" {
		t.Fatalf("expected resurrected, got %q", out.Secrets["API_KEY"])
	}
}

func TestSecretDelete_Idempotent(t *testing.T) {
	ts, _, _ := testutil.NewTestServer(t)

	// Delete API_KEY twice — both should succeed
	for i := 0; i < 2; i++ {
		resp := testutil.PostJSON(t, ts.URL+"/secrets/delete", `{"agent":"a","key":"API_KEY"}`)
		resp.Body.Close()
		testutil.AssertStatus(t, resp, http.StatusNoContent)
	}
}
