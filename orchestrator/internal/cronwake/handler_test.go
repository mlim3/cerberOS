package cronwake

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

type fakeIngester struct {
	lastTask types.UserTask
	err      error
}

func (f *fakeIngester) HandleInboundTask(_ context.Context, task types.UserTask) error {
	f.lastTask = task
	return f.err
}

func TestHandler_RejectsMissingSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.OrchestratorConfig{CronWakeSecret: "s3cret"}
	ing := &fakeIngester{}
	h := NewHandler(cfg, ing)

	req := httptest.NewRequest(http.MethodPost, "/v1/cron/wake", bytes.NewReader([]byte(`{}`)))
	req.Header.Set(headerCronSecret, "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandler_AcceptsValidSecretAndDispatchesMaintenanceTask(t *testing.T) {
	t.Parallel()
	cfg := &config.OrchestratorConfig{
		CronWakeSecret:          "s3cret",
		CronWakeUserID:          "system",
		CronWakeCallbackTopic:   "aegis.orchestrator.cron.wake.results",
		CronWakeSystemPrompt:    "Default sys",
		CronWakeRawInput:        "Default work",
	}
	ing := &fakeIngester{}
	h := NewHandler(cfg, ing)

	body := map[string]any{
		"system_prompt": "Override sys",
		"raw_input":     "Override work",
		"job_name":      "fact_decay",
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/cron/wake", bytes.NewReader(raw))
	req.Header.Set(headerCronSecret, "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if !isValidUUID(ing.lastTask.TaskID) {
		t.Fatalf("task_id not uuid: %q", ing.lastTask.TaskID)
	}
	if ing.lastTask.UserID != "system" {
		t.Fatalf("user_id = %q", ing.lastTask.UserID)
	}
	if ing.lastTask.CallbackTopic != cfg.CronWakeCallbackTopic {
		t.Fatalf("callback_topic = %q", ing.lastTask.CallbackTopic)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(ing.lastTask.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	var sys, rin, job string
	_ = json.Unmarshal(payload["system_prompt"], &sys)
	_ = json.Unmarshal(payload["raw_input"], &rin)
	_ = json.Unmarshal(payload["job_name"], &job)
	if sys != "Override sys" {
		t.Fatalf("system_prompt = %q", sys)
	}
	if rin != "Override work" {
		t.Fatalf("raw_input = %q", rin)
	}
	if job != "fact_decay" {
		t.Fatalf("job_name = %q", job)
	}
	var maint bool
	_ = json.Unmarshal(payload["maintenance"], &maint)
	if !maint {
		t.Fatal("maintenance flag not set")
	}
}

func TestHandler_DisabledWithoutSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.OrchestratorConfig{CronWakeSecret: ""}
	h := NewHandler(cfg, &fakeIngester{})
	req := httptest.NewRequest(http.MethodPost, "/v1/cron/wake", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestHandler_RejectsMalformedJSON ensures a non-empty, non-JSON body fails
// loudly with 400 rather than silently falling back to defaults.
func TestHandler_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	cfg := &config.OrchestratorConfig{CronWakeSecret: "s3cret"}
	ing := &fakeIngester{}
	h := NewHandler(cfg, ing)

	req := httptest.NewRequest(http.MethodPost, "/v1/cron/wake", bytes.NewReader([]byte(`{not-json`)))
	req.Header.Set(headerCronSecret, "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if ing.lastTask.TaskID != "" {
		t.Fatalf("expected no task dispatched on malformed body, got task_id=%q", ing.lastTask.TaskID)
	}
}

// TestHandler_AcceptsEmptyBodyUsesDefaults preserves the cron-friendly path
// where the caller sends no body at all and the handler uses configured defaults.
func TestHandler_AcceptsEmptyBodyUsesDefaults(t *testing.T) {
	t.Parallel()
	cfg := &config.OrchestratorConfig{
		CronWakeSecret:        "s3cret",
		CronWakeUserID:        "system",
		CronWakeCallbackTopic: "aegis.orchestrator.cron.wake.results",
		CronWakeRawInput:      "configured maintenance work",
	}
	ing := &fakeIngester{}
	h := NewHandler(cfg, ing)

	req := httptest.NewRequest(http.MethodPost, "/v1/cron/wake", http.NoBody)
	req.Header.Set(headerCronSecret, "s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if !isValidUUID(ing.lastTask.TaskID) {
		t.Fatalf("task_id not uuid: %q", ing.lastTask.TaskID)
	}
}

func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	dashAt := map[int]bool{8: true, 13: true, 18: true, 23: true}
	for i, c := range s {
		if dashAt[i] {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
