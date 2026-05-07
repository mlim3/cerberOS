package endpoint_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/handlers/inject"
	"github.com/mlim3/cerberOS/vault/engine/test/testutil"
)

func TestInject(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       string
		setup      func(t *testing.T, tsURL string) // optional pre-test setup (e.g. put a secret)
		wantStatus int
		check      func(t *testing.T, resp *http.Response)
	}{
		{
			name:       "HappyPath_SinglePlaceholder",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"echo {{API_KEY}}"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "echo mock-api-key-12345" {
					t.Fatalf("script = %q", out.Script)
				}
				if out.Agent != "a" {
					t.Fatalf("agent = %q", out.Agent)
				}
			},
		},
		{
			name:       "HappyPath_MultiplePlaceholders",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"{{API_KEY}} {{DB_PASS}}"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "mock-api-key-12345 mock-db-password" {
					t.Fatalf("script = %q", out.Script)
				}
			},
		},
		{
			name:       "HappyPath_DuplicatePlaceholder",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"{{API_KEY}} and {{API_KEY}}"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "mock-api-key-12345 and mock-api-key-12345" {
					t.Fatalf("script = %q", out.Script)
				}
			},
		},
		{
			name:       "NoPlaceholders_ScriptUnchanged",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"echo hello world"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "echo hello world" {
					t.Fatalf("script = %q", out.Script)
				}
			},
		},
		{
			name:       "EmptyScript",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":""}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "" {
					t.Fatalf("script = %q, want empty", out.Script)
				}
			},
		},
		{
			name:       "UnknownSecret_403",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"echo {{NONEXISTENT}}"}`,
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
			name:       "AtomicFailure_OneValidOneInvalid",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"{{API_KEY}} {{NOPE}}"}`,
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
			name:       "WrongMethod_GET_405",
			method:     http.MethodGet,
			body:       "",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "WrongMethod_PUT_405",
			method:     http.MethodPut,
			body:       `{"agent":"a","script":"x"}`,
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "MalformedJSON_400",
			method:     http.MethodPost,
			body:       `{broken`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "EmptyBody_400",
			method:     http.MethodPost,
			body:       "",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "SpecialCharsInValue",
			method: http.MethodPost,
			body:   `{"agent":"a","script":"{{SPECIAL}}"}`,
			setup: func(t *testing.T, tsURL string) {
				resp := testutil.PostJSON(t, tsURL+"/secrets/put", `{"agent":"a","key":"SPECIAL","value":"p@$$w0rd!#&<>"}`)
				resp.Body.Close()
				testutil.AssertStatus(t, resp, http.StatusNoContent)
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != `p@$$w0rd!#&<>` {
					t.Fatalf("script = %q", out.Script)
				}
			},
		},
		{
			name:       "UnicodeInScript",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"こんにちは {{API_KEY}} 🔑"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "こんにちは mock-api-key-12345 🔑" {
					t.Fatalf("script = %q", out.Script)
				}
			},
		},
		{
			name:       "LargeScript",
			method:     http.MethodPost,
			body:       fmt.Sprintf(`{"agent":"a","script":"%s{{API_KEY}}"}`, strings.Repeat("x", 100000)),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if !strings.HasSuffix(out.Script, "mock-api-key-12345") {
					t.Fatal("placeholder not substituted in large script")
				}
				if len(out.Script) < 100000 {
					t.Fatalf("script too short: %d", len(out.Script))
				}
			},
		},
		{
			name:       "PlaceholderLikeButInvalid",
			method:     http.MethodPost,
			body:       `{"agent":"a","script":"{API_KEY} {{123BAD}} {{ SPACE }}"}`,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, resp *http.Response) {
				var out inject.InjectResponse
				json.NewDecoder(resp.Body).Decode(&out)
				if out.Script != "{API_KEY} {{123BAD}} {{ SPACE }}" {
					t.Fatalf("script = %q", out.Script)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, _, _ := testutil.NewTestServer(t)

			if tt.setup != nil {
				tt.setup(t, ts.URL)
			}

			resp := testutil.DoRequest(t, tt.method, ts.URL+"/inject", tt.body)
			defer resp.Body.Close()
			testutil.AssertStatus(t, resp, tt.wantStatus)

			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}
}

func TestInject_AuditEvents(t *testing.T) {
	t.Run("EmitsInjectionEvent", func(t *testing.T) {
		ts, _, cap := testutil.NewTestServer(t)
		cap.Reset()

		resp := testutil.PostJSON(t, ts.URL+"/inject", `{"agent":"audit-agent","script":"echo hello"}`)
		resp.Body.Close()
		testutil.AssertStatus(t, resp, http.StatusOK)

		events := cap.FindByKind(audit.KindInjection)
		if len(events) == 0 {
			t.Fatal("no injection audit event emitted")
		}
		if events[0].Agent != "audit-agent" {
			t.Fatalf("agent = %q", events[0].Agent)
		}
	})

	t.Run("EmitsSecretAccessEvent", func(t *testing.T) {
		ts, _, cap := testutil.NewTestServer(t)
		cap.Reset()

		resp := testutil.PostJSON(t, ts.URL+"/inject", `{"agent":"audit-agent","script":"{{API_KEY}}"}`)
		resp.Body.Close()
		testutil.AssertStatus(t, resp, http.StatusOK)

		events := cap.FindByKind(audit.KindSecretAccess)
		if len(events) == 0 {
			t.Fatal("no secret_access audit event emitted")
		}
		found := false
		for _, e := range events {
			for _, k := range e.Keys {
				if k == "API_KEY" {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("API_KEY not in audit event keys")
		}
	})

	t.Run("FailedInject_NoSecretAccessEvent", func(t *testing.T) {
		ts, _, cap := testutil.NewTestServer(t)
		cap.Reset()

		resp := testutil.PostJSON(t, ts.URL+"/inject", `{"agent":"a","script":"{{NOPE}}"}`)
		resp.Body.Close()
		testutil.AssertStatus(t, resp, http.StatusForbidden)

		// Should have injection event but not secret_access (preprocessor fails before audit)
		events := cap.FindByKind(audit.KindSecretAccess)
		if len(events) != 0 {
			t.Fatalf("expected no secret_access events on failure, got %d", len(events))
		}
	})
}
