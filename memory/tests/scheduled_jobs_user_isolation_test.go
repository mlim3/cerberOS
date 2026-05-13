package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// secondSchedulingTestUserID is a second seeded user used to assert MT-5 (#186)
// tenant isolation on scheduled_jobs. Distinct from the orchestrator-records
// isolation user (in orchestrator_user_isolation_test.go) so the two suites
// don't accidentally share fixture state.
const secondSchedulingTestUserID = "00000000-0000-0000-0000-000000000003"

func ensureSchedulingSecondTestUser(t *testing.T) {
	t.Helper()
	_, err := dbPool.Exec(context.Background(), `
INSERT INTO identity_schema.users (id, email, role)
VALUES ($1, 'mt5-isolation@example.com', 'user')
ON CONFLICT (id) DO NOTHING`,
		secondSchedulingTestUserID)
	if err != nil {
		t.Fatalf("seed second scheduling test user: %v", err)
	}
}

// TestScheduledJobsAreTenantIsolated is the MT-5 (#186) AC test: a user_cron
// scheduled by user A is invisible to user B's listing call, even when both
// users hit the same /api/v1/user_crons endpoint with the same vault API key.
func TestScheduledJobsAreTenantIsolated(t *testing.T) {
	ensureSchedulingSecondTestUser(t)

	baseURL := blackboxBaseURL()
	headers := internalAPIKeyHeaders()
	cronName := fmt.Sprintf("mt5-iso-cron-%d", time.Now().UnixNano())
	nextRunAt := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)

	status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
		"jobType":        "user_cron",
		"targetKind":     "user",
		"targetService":  "orchestrator",
		"status":         "active",
		"scheduleKind":   "cron",
		"cronExpression": "0 9 * * MON",
		"name":           cronName,
		"userId":         testUserID,
		"payload":        map[string]any{"userId": testUserID},
		"nextRunAt":      nextRunAt,
	}, headers)
	if status != http.StatusCreated {
		t.Fatalf("create as A status = %d, want %d (env=%+v)", status, http.StatusCreated, env)
	}

	listAsB := func(t *testing.T, userID string) []map[string]any {
		t.Helper()
		status, env := apiJSONRequest(t, http.MethodGet,
			baseURL+"/api/v1/user_crons?userId="+userID,
			nil, headers)
		if status != http.StatusOK {
			t.Fatalf("list user_crons status = %d, want %d", status, http.StatusOK)
		}
		var payload struct {
			Jobs []map[string]any `json:"jobs"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal user_crons: %v", err)
		}
		return payload.Jobs
	}

	jobsA := listAsB(t, testUserID)
	sawA := false
	for _, j := range jobsA {
		if j["name"] == cronName {
			sawA = true
		}
	}
	if !sawA {
		t.Fatalf("user A's listing missing the cron we just created: %+v", jobsA)
	}

	jobsB := listAsB(t, secondSchedulingTestUserID)
	for _, j := range jobsB {
		if j["name"] == cronName {
			t.Fatalf("user B saw user A's cron: %+v", j)
		}
	}
}

// TestScheduledJobsCreateRejectsMissingUserID confirms the handler rejects
// blank userId regardless of jobType (MT-5 tightened this from user_cron-only
// to a universal requirement so that every row carries a real FK target).
func TestScheduledJobsCreateRejectsMissingUserID(t *testing.T) {
	baseURL := blackboxBaseURL()
	headers := internalAPIKeyHeaders()

	status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/scheduled_jobs", map[string]any{
		"jobType":         "fact_decay_scan",
		"targetKind":      "internal",
		"targetService":   "memory",
		"status":          "active",
		"scheduleKind":    "interval",
		"intervalSeconds": 600,
		"name":            "mt5-missing-userid",
		"payload":         map[string]any{},
		"nextRunAt":       time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
	}, headers)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	assertErrorCode(t, env, "invalid_argument")
}
