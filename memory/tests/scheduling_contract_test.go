package tests

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func internalAPIKeyHeaders() map[string]string {
	k := os.Getenv("INTERNAL_VAULT_API_KEY")
	if k == "" {
		k = "test-vault-key"
	}
	return map[string]string{"X-Internal-API-Key": k}
}

func TestScheduledJobsContract_BlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	jobName := "fact-decay-scan"
	nextRunAt := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	futureRunAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)

	var internalJobID string
	var externalJobID string
	var futureJobID string
	var userCronJobID string

	t.Run("create_internal_scheduled_job", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":         "fact_decay_scan",
			"targetKind":      "internal",
			"targetService":   "memory",
			"status":          "active",
			"scheduleKind":    "interval",
			"intervalSeconds": 21600,
			"name":            jobName,
			"userId":          testUserID,
			"payload": map[string]any{
				"batchSize": 100,
			},
			"nextRunAt": nextRunAt,
		}, internalAPIKeyHeaders())

		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
		internalJobID = assertAndExtractNonEmptyStringField(t, env.Data, "id")
		assertJSONContainsStringField(t, env.Data, "jobType", "fact_decay_scan")
		assertJSONContainsStringField(t, env.Data, "targetKind", "internal")
		assertJSONContainsStringField(t, env.Data, "targetService", "memory")
	})

	t.Run("create_external_scheduled_job", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":         "external_dispatch",
			"targetKind":      "external",
			"targetService":   "orchestrator",
			"status":          "active",
			"scheduleKind":    "interval",
			"intervalSeconds": 300,
			"name":            "orchestrator-dispatch",
			"userId":          testUserID,
			"payload": map[string]any{
				"eventType": "memory.job_due",
				"taskId":    uuid.NewString(),
			},
			"nextRunAt": nextRunAt,
		}, internalAPIKeyHeaders())

		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
		externalJobID = assertAndExtractNonEmptyStringField(t, env.Data, "id")
		assertJSONContainsStringField(t, env.Data, "jobType", "external_dispatch")
		assertJSONContainsStringField(t, env.Data, "targetKind", "external")
		assertJSONContainsStringField(t, env.Data, "targetService", "orchestrator")
	})

	t.Run("create_subminute_interval_job_is_accepted", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":         "user_cron",
			"targetKind":      "user",
			"targetService":   "orchestrator",
			"status":          "active",
			"scheduleKind":    "interval",
			"intervalSeconds": 30,
			"name":            "subminute-user-cron",
			"userId":          testUserID,
			"payload": map[string]any{
				"userId":   testUserID,
				"rawInput": "Check for a fresh signal every 30 seconds.",
			},
			"nextRunAt": nextRunAt,
		}, internalAPIKeyHeaders())

		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
		userCronJobID = assertAndExtractNonEmptyStringField(t, env.Data, "id")
		assertJSONContainsStringField(t, env.Data, "jobType", "user_cron")
	})

	t.Run("idempotency_claim_and_complete_update_job_state", func(t *testing.T) {
		actionKey := "flight_watcher:user-1:2026-05-13:price_drop:" + uuid.NewString()
		claimStatus, claimEnv := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/idempotency/claim", map[string]any{
			"key":        actionKey,
			"agentId":    "agent-flight-1",
			"jobId":      userCronJobID,
			"runId":      uuid.NewString(),
			"ttlSeconds": 86400,
		}, internalAPIKeyHeaders())
		if claimStatus != http.StatusOK {
			t.Fatalf("claim status = %d, want %d", claimStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, claimEnv)
		assertJSONContainsBoolField(t, claimEnv.Data, "claimed", true)

		completeStatus, completeEnv := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/idempotency/complete", map[string]any{
			"key":    actionKey,
			"status": "completed",
			"jobId":  userCronJobID,
			"result": map[string]any{
				"price":  540,
				"action": "notified",
			},
			"jobState": map[string]any{
				"last_checked_price": 540,
				"notified":           true,
				"best_price_seen":    540,
			},
		}, internalAPIKeyHeaders())
		if completeStatus != http.StatusOK {
			t.Fatalf("complete status = %d, want %d", completeStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, completeEnv)

		listStatus, listEnv := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/user_crons?userId="+testUserID, nil, internalAPIKeyHeaders())
		if listStatus != http.StatusOK {
			t.Fatalf("list user crons status = %d, want %d", listStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, listEnv)

		var payload map[string]any
		if err := json.Unmarshal(listEnv.Data, &payload); err != nil {
			t.Fatalf("unmarshal user_crons payload: %v", err)
		}
		jobs, ok := payload["jobs"].([]any)
		if !ok || len(jobs) == 0 {
			t.Fatalf("jobs missing or empty: %s", string(listEnv.Data))
		}
		found := false
		for _, item := range jobs {
			job, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if asString(job["id"]) != userCronJobID {
				continue
			}
			state, ok := job["state"].(map[string]any)
			if !ok {
				t.Fatalf("job state missing: %#v", job)
			}
			if state["notified"] != true {
				t.Fatalf("state.notified = %#v, want true", state["notified"])
			}
			if state["last_checked_price"] != float64(540) {
				t.Fatalf("state.last_checked_price = %#v, want 540", state["last_checked_price"])
			}
			found = true
		}
		if !found {
			t.Fatalf("user_cron job %s not found in jobs payload: %s", userCronJobID, string(listEnv.Data))
		}

		noStateStatus, noStateEnv := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/idempotency/complete", map[string]any{
			"key":    actionKey,
			"status": "completed",
			"jobId":  userCronJobID,
			"result": map[string]any{
				"price":  535,
				"action": "already_notified",
			},
		}, internalAPIKeyHeaders())
		if noStateStatus != http.StatusOK {
			t.Fatalf("complete without jobState status = %d, want %d", noStateStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, noStateEnv)

		listStatus, listEnv = apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/user_crons?userId="+testUserID, nil, internalAPIKeyHeaders())
		if listStatus != http.StatusOK {
			t.Fatalf("list user crons after no-state complete status = %d, want %d", listStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, listEnv)
		if err := json.Unmarshal(listEnv.Data, &payload); err != nil {
			t.Fatalf("unmarshal user_crons payload after no-state complete: %v", err)
		}
		jobs, ok = payload["jobs"].([]any)
		if !ok || len(jobs) == 0 {
			t.Fatalf("jobs missing or empty after no-state complete: %s", string(listEnv.Data))
		}
		for _, item := range jobs {
			job, ok := item.(map[string]any)
			if !ok || asString(job["id"]) != userCronJobID {
				continue
			}
			state, ok := job["state"].(map[string]any)
			if !ok {
				t.Fatalf("job state missing after no-state complete: %#v", job)
			}
			if state["notified"] != true || state["last_checked_price"] != float64(540) {
				t.Fatalf("state changed after complete without jobState: %#v", state)
			}
			return
		}
		t.Fatalf("user_cron job %s not found after no-state complete: %s", userCronJobID, string(listEnv.Data))
	})

	t.Run("create_job_with_invalid_timestamp_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":       "fact_decay_scan",
			"targetKind":    "internal",
			"targetService": "memory",
			"status":        "active",
			"scheduleKind":  "interval",
			"name":          "bad-timestamp",
			"nextRunAt":     "not-a-timestamp",
		}, internalAPIKeyHeaders())

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("create_job_missing_required_field_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":    "fact_decay_scan",
			"targetKind": "internal",
			"status":     "active",
			"name":       "missing-target-service",
			"nextRunAt":  nextRunAt,
		}, internalAPIKeyHeaders())

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("create_future_scheduled_job", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":         "fact_decay_scan",
			"targetKind":      "internal",
			"targetService":   "memory",
			"status":          "active",
			"scheduleKind":    "interval",
			"intervalSeconds": 600,
			"name":            "future-job",
			"userId":          testUserID,
			"payload":         map[string]any{},
			"nextRunAt":       futureRunAt,
		}, internalAPIKeyHeaders())

		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
		futureJobID = assertAndExtractNonEmptyStringField(t, env.Data, "id")
	})

	t.Run("run_due_executes_jobs_and_returns_run_history", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs/run_due", map[string]any{}, internalAPIKeyHeaders())

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d; error = %#v; data = %s", status, http.StatusOK, env.Error, string(env.Data))
		}
		assertSuccessEnvelope(t, env)
		assertJSONHasArrayField(t, env.Data, "runs")
	})

	t.Run("internal_job_run_history_is_queryable", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/scheduled_jobs/"+internalJobID+"/runs", nil, internalAPIKeyHeaders())

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertJSONHasArrayField(t, env.Data, "runs")
	})

	t.Run("external_job_run_records_dispatch_result", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/scheduled_jobs/"+externalJobID+"/runs", nil, internalAPIKeyHeaders())

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		var payload map[string]any
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal scheduled job run payload: %v", err)
		}
		runs, ok := payload["runs"].([]any)
		if !ok || len(runs) == 0 {
			t.Fatalf("runs missing or empty: %s", string(env.Data))
		}
		firstRun, ok := runs[0].(map[string]any)
		if !ok {
			t.Fatalf("first run was not an object: %#v", runs[0])
		}
		if strings.TrimSpace(asString(firstRun["status"])) == "" {
			t.Fatalf("run status was empty: %#v", firstRun)
		}
		if strings.TrimSpace(asString(firstRun["targetService"])) == "" {
			t.Fatalf("targetService was empty: %#v", firstRun)
		}
	})

	t.Run("future_job_run_history_is_empty_before_due", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/scheduled_jobs/"+futureJobID+"/runs", nil, internalAPIKeyHeaders())

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		var payload map[string]any
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal future runs payload: %v", err)
		}
		runs, ok := payload["runs"].([]any)
		if !ok {
			t.Fatalf("runs missing or not an array: %s", string(env.Data))
		}
		if len(runs) != 0 {
			t.Fatalf("future job runs length = %d, want 0", len(runs))
		}
	})

	t.Run("invalid_job_id_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/scheduled_jobs/not-a-uuid/runs", nil, internalAPIKeyHeaders())

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})
}
