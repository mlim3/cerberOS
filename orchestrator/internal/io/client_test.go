package io_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	ioclient "github.com/mlim3/cerberOS/orchestrator/internal/io"
)

// capturePost starts a test HTTP server, sends one request via f, and returns
// the decoded JSON body plus HTTP status code.
func capturePost(t *testing.T, f func(c *ioclient.Client)) (map[string]any, int) {
	t.Helper()

	var (
		body   map[string]any
		status int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status = http.StatusOK
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := ioclient.New(srv.URL)
	f(c)
	return body, status
}

// ── PushSkillActivity ─────────────────────────────────────────────────────────

func TestPushSkillActivity_PostsCorrectEventType(t *testing.T) {
	body, _ := capturePost(t, func(c *ioclient.Client) {
		_ = c.PushSkillActivity(ioclient.SkillActivityPayload{
			TaskID:         "task-1",
			AgentID:        "agent-abc",
			Domain:         "web",
			Command:        "web.search",
			ElapsedMS:      150,
			VaultDelegated: false,
			Outcome:        "success",
			Timestamp:      1700000000000,
		})
	})

	if body == nil {
		t.Fatal("no body received")
	}
	if body["type"] != "skill_activity" {
		t.Fatalf("type = %q, want skill_activity", body["type"])
	}
}

func TestPushSkillActivity_PayloadFieldsMatchInput(t *testing.T) {
	body, _ := capturePost(t, func(c *ioclient.Client) {
		_ = c.PushSkillActivity(ioclient.SkillActivityPayload{
			TaskID:         "task-42",
			AgentID:        "agent-xyz",
			Domain:         "data",
			Command:        "data.query",
			ElapsedMS:      7500,
			VaultDelegated: true,
			Outcome:        "success",
			Timestamp:      1700000001234,
		})
	})

	payload, ok := body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("body[\"payload\"] not a map: %v", body)
	}

	checks := map[string]any{
		"taskId":         "task-42",
		"agentId":        "agent-xyz",
		"domain":         "data",
		"command":        "data.query",
		"elapsedMs":      float64(7500),
		"vaultDelegated": true,
		"outcome":        "success",
	}
	for k, want := range checks {
		if payload[k] != want {
			t.Errorf("payload[%q] = %v, want %v", k, payload[k], want)
		}
	}
}

func TestPushSkillActivity_Disabled_IsNoop(t *testing.T) {
	// A client created with an empty base URL must not panic or attempt HTTP.
	c := ioclient.New("")
	if !c.Disabled() {
		t.Fatal("expected Disabled() = true for empty base URL")
	}
	if err := c.PushSkillActivity(ioclient.SkillActivityPayload{
		TaskID:  "task-1",
		Command: "web.search",
	}); err != nil {
		t.Fatalf("PushSkillActivity() on disabled client returned error: %v", err)
	}
}

// ── PushStatus (regression) ───────────────────────────────────────────────────

func TestPushStatus_PostsCorrectEventType(t *testing.T) {
	body, _ := capturePost(t, func(c *ioclient.Client) {
		_ = c.PushStatus("task-1", "working", "Starting...", nil)
	})

	if body["type"] != "status" {
		t.Fatalf("type = %q, want status", body["type"])
	}
}

// ── PushCredentialRequest (regression) ───────────────────────────────────────

func TestPushCredentialRequest_PostsCorrectEventType(t *testing.T) {
	body, _ := capturePost(t, func(c *ioclient.Client) {
		_ = c.PushCredentialRequest(ioclient.CredentialRequestPayload{
			TaskID:    "task-1",
			RequestID: "req-1",
			UserID:    "user-1",
			KeyName:   "api_key",
			Label:     "API Key",
		})
	})

	if body["type"] != "credential_request" {
		t.Fatalf("type = %q, want credential_request", body["type"])
	}
}
