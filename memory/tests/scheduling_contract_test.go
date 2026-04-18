package tests

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestScheduledJobsContract_FutureBlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	jobName := "fact-decay-scan"
	nextRunAt := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)

	var internalJobID string
	var externalJobID string

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
		}, nil)

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
		}, nil)

		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
		externalJobID = assertAndExtractNonEmptyStringField(t, env.Data, "id")
		assertJSONContainsStringField(t, env.Data, "jobType", "external_dispatch")
		assertJSONContainsStringField(t, env.Data, "targetKind", "external")
		assertJSONContainsStringField(t, env.Data, "targetService", "orchestrator")
	})

	t.Run("run_due_executes_jobs_and_returns_run_history", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs/run_due", map[string]any{}, nil)

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertJSONHasArrayField(t, env.Data, "runs")
	})

	t.Run("internal_job_run_history_is_queryable", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/scheduled_jobs/"+internalJobID+"/runs", nil, nil)

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertJSONHasArrayField(t, env.Data, "runs")
	})

	t.Run("external_job_run_records_dispatch_result", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/scheduled_jobs/"+externalJobID+"/runs", nil, nil)

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
}
