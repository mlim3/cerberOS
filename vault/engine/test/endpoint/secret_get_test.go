package endpoint_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/handlers/secrets"
	"github.com/mlim3/cerberOS/vault/engine/test/testutil"
)

func TestSecretGet(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
		check      func(t *testing.T, resp *http.Response)
	}{
		{
			name:       "SingleKey_OK",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":["API_KEY"]}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out secrets.SecretGetResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Secrets["API_KEY"] != "mock-api-key-12345" {
					t.Fatalf("secrets = %v", out.Secrets)
				}
			},
		},
		{
			name:       "MultipleKeys_OK",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":["API_KEY","DB_PASS"]}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out secrets.SecretGetResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Secrets["API_KEY"] != "mock-api-key-12345" || out.Secrets["DB_PASS"] != "mock-db-password" {
					t.Fatalf("secrets = %v", out.Secrets)
				}
			},
		},
		{
			name:       "AllSeededKeys",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":["API_KEY","DB_PASS","SECRET_KEY","TEST_SECRET"]}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out secrets.SecretGetResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if len(out.Secrets) != 4 {
					t.Fatalf("expected 4 secrets, got %d", len(out.Secrets))
				}
			},
		},
		{
			name:       "MissingKey_403",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":["NONEXISTENT"]}`,
			wantStatus: http.StatusForbidden,
			check: func(t *testing.T, resp *http.Response) {
				var er common.ErrorResponse
				json.NewDecoder(resp.Body).Decode(&er)
				if er.Error == "" {
					t.Fatal("expected error message")
				}
			},
		},
		{
			name:       "AtomicFailure_MixedKeys",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":["API_KEY","NONEXISTENT"]}`,
			wantStatus: http.StatusForbidden,
			check: func(t *testing.T, resp *http.Response) {
				var er common.ErrorResponse
				json.NewDecoder(resp.Body).Decode(&er)
				if er.Error == "" {
					t.Fatal("expected error message for atomic failure")
				}
			},
		},
		{
			name:       "EmptyKeysArray",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":[]}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out secrets.SecretGetResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if len(out.Secrets) != 0 {
					t.Fatalf("expected empty map, got %v", out.Secrets)
				}
			},
		},
		{
			name:       "WrongMethod_GET_405",
			method:     http.MethodGet,
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "WrongMethod_DELETE_405",
			method:     http.MethodDelete,
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "MalformedJSON_400",
			method:     http.MethodPost,
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "EmptyBody_400",
			method:     http.MethodPost,
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "MissingAgentField",
			method:     http.MethodPost,
			body:       `{"keys":["API_KEY"]}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out secrets.SecretGetResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Secrets["API_KEY"] != "mock-api-key-12345" {
					t.Fatalf("secrets = %v", out.Secrets)
				}
			},
		},
		{
			name:       "DuplicateKeysInArray",
			method:     http.MethodPost,
			body:       `{"agent":"a","keys":["API_KEY","API_KEY"]}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out secrets.SecretGetResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Secrets["API_KEY"] != "mock-api-key-12345" {
					t.Fatalf("secrets = %v", out.Secrets)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _, _ := testutil.NewTestServer(t)

			resp := testutil.DoRequest(t, tt.method, ts.URL+"/secrets/get", tt.body)
			defer resp.Body.Close()
			testutil.AssertStatus(t, resp, tt.wantStatus)

			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}
