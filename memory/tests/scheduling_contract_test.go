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

	t.Run("create_internal_scheduled_job", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
			"jobType":         "fact_decay_scan",
			"targetKind":      "internal",
			"targetService":   "memory",
			"status":          "active",
			"scheduleKind":    "interval",
			"intervalSeconds": 21600,
			"name":            jobName,
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
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
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
