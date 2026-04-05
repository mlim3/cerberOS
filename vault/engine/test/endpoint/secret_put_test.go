package endpoint_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/test/testutil"
)

func TestSecretPut(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
	}{
		{
			name:       "NewKey_204",
			method:     http.MethodPost,
			body:       `{"agent":"a","key":"BRAND_NEW","value":"fresh"}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "EmptyValue_204",
			method:     http.MethodPost,
			body:       `{"agent":"a","key":"EMPTY_VAL","value":""}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "SpecialCharsInValue_204",
			method:     http.MethodPost,
			body:       `{"agent":"a","key":"SPECIAL","value":"p@$$w0rd!#&\"quotes\""}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "SpecialCharsInKey_204",
			method:     http.MethodPost,
			body:       `{"agent":"a","key":"MY_KEY_123","value":"val"}`,
			wantStatus: http.StatusNoContent,
		},
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

			resp := testutil.DoRequest(t, tt.method, ts.URL+"/secrets/put", tt.body)
			resp.Body.Close()
			testutil.AssertStatus(t, resp, tt.wantStatus)
		})
	}
}

func TestSecretPut_OverwriteExisting(t *testing.T) {
	ts, _, _ := testutil.NewTestServer(t)

	// Overwrite seeded API_KEY
	resp := testutil.PostJSON(t, ts.URL+"/secrets/put", `{"agent":"a","key":"API_KEY","value":"new-value"}`)
	resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	// Verify the new value
	resp = testutil.PostJSON(t, ts.URL+"/secrets/get", `{"agent":"a","keys":["API_KEY"]}`)
	defer resp.Body.Close()
	testutil.AssertStatus(t, resp, http.StatusOK)

	var out secrets.SecretGetResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Secrets["API_KEY"] != "new-value" {
		t.Fatalf("expected new-value, got %q", out.Secrets["API_KEY"])
	}
}

func TestSecretPut_ThenGet_RoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"SimpleValue", "RT_SIMPLE", "hello"},
		{"EmptyValue", "RT_EMPTY", ""},
		{"Whitespace", "RT_WS", "  tabs\tand\nnewlines  "},
		{"LongValue", "RT_LONG", string(make([]byte, 10000))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _, _ := testutil.NewTestServer(t)

			putBody, _ := json.Marshal(map[string]string{
				"agent": "a", "key": tc.key, "value": tc.value,
			})
			resp := testutil.PostJSON(t, ts.URL+"/secrets/put", string(putBody))
			resp.Body.Close()
			testutil.AssertStatus(t, resp, http.StatusNoContent)

			getBody, _ := json.Marshal(map[string]any{
				"agent": "a", "keys": []string{tc.key},
			})
			resp = testutil.PostJSON(t, ts.URL+"/secrets/get", string(getBody))
			defer resp.Body.Close()
			testutil.AssertStatus(t, resp, http.StatusOK)

			var out secrets.SecretGetResponse
			json.NewDecoder(resp.Body).Decode(&out)
			if out.Secrets[tc.key] != tc.value {
				t.Fatalf("round-trip mismatch: got %q", out.Secrets[tc.key])
			}
		})
	}
}
