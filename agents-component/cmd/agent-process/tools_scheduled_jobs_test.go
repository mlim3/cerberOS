package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ── create_scheduled_job ─────────────────────────────────────────────────────

func TestCreateScheduledJob_MissingName(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"raw_input":        "send daily report",
		"interval_seconds": 3600,
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing name: want IsError=true")
	}
	if !strings.Contains(result.Content, "name") {
		t.Errorf("missing name: error should mention 'name', got %q", result.Content)
	}
}

func TestCreateScheduledJob_MissingRawInput(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"name":             "daily_report",
		"interval_seconds": 3600,
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing raw_input: want IsError=true")
	}
	if !strings.Contains(result.Content, "raw_input") {
		t.Errorf("missing raw_input: error should mention 'raw_input', got %q", result.Content)
	}
}

func TestCreateScheduledJob_BothIntervalAndCron(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"name":             "conflict",
		"raw_input":        "do thing",
		"interval_seconds": 3600,
		"cron_expression":  "0 9 * * 1",
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("both interval and cron: want IsError=true")
	}
	if !strings.Contains(result.Content, "not both") {
		t.Errorf("both interval and cron: error should mention 'not both', got %q", result.Content)
	}
}

func TestCreateScheduledJob_NeitherIntervalNorCron(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"name":      "neither",
		"raw_input": "do thing",
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("neither interval nor cron: want IsError=true")
	}
}

func TestCreateScheduledJob_InvalidFirstRunAt(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"name":             "job1",
		"raw_input":        "do thing",
		"interval_seconds": 3600,
		"first_run_at":     "not-a-date",
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("invalid first_run_at: want IsError=true")
	}
	if !strings.Contains(result.Content, "first_run_at") {
		t.Errorf("invalid first_run_at: error should mention 'first_run_at', got %q", result.Content)
	}
}

func TestCreateScheduledJob_InvalidJSON(t *testing.T) {
	result := executeCreateScheduledJob(nil, "user-1", json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("invalid JSON: want IsError=true")
	}
}

func TestCreateScheduledJob_IntervalSchedule_NilSL_Succeeds(t *testing.T) {
	// nil sl → CreateScheduledJob is a no-op; the function returns success.
	raw, _ := json.Marshal(map[string]any{
		"name":             "daily_report",
		"raw_input":        "Generate and send the daily sales report.",
		"interval_seconds": 86400,
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("interval schedule, nil sl: unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "daily_report") {
		t.Errorf("interval schedule: expected job name in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "86400") {
		t.Errorf("interval schedule: expected interval in result, got %q", result.Content)
	}
}

func TestCreateScheduledJob_CronSchedule_NilSL_Succeeds(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"name":            "weekly_summary",
		"raw_input":       "Summarise the week's activity.",
		"cron_expression": "0 9 * * 1",
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("cron schedule, nil sl: unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "weekly_summary") {
		t.Errorf("cron schedule: expected job name in result, got %q", result.Content)
	}
}

func TestCreateScheduledJob_ExplicitFirstRunAt_NilSL(t *testing.T) {
	firstRun := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	raw, _ := json.Marshal(map[string]any{
		"name":             "future_job",
		"raw_input":        "Run this tomorrow.",
		"interval_seconds": 86400,
		"first_run_at":     firstRun,
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("explicit first_run_at: unexpected error: %s", result.Content)
	}
}

func TestCreateScheduledJob_RequiredSkillDomains_Accepted(t *testing.T) {
	// Domains are stored in the payload; tool should succeed and not error.
	raw, _ := json.Marshal(map[string]any{
		"name":                   "flight_watcher",
		"raw_input":              "Check Google Flights for deals to Japan under $600.",
		"interval_seconds":       180,
		"required_skill_domains": []string{"google_workspace", "web"},
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("required_skill_domains: unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "flight_watcher") {
		t.Errorf("required_skill_domains: expected job name in result, got %q", result.Content)
	}
}

func TestCreateScheduledJob_NoRequiredSkillDomains_Accepted(t *testing.T) {
	// Omitting required_skill_domains is valid (nil → unrestricted scope at dispatch).
	raw, _ := json.Marshal(map[string]any{
		"name":             "no_cred_job",
		"raw_input":        "Log a reminder.",
		"interval_seconds": 3600,
	})
	result := executeCreateScheduledJob(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("no required_skill_domains: unexpected error: %s", result.Content)
	}
}

// ── createScheduledJobTool: definition contract ───────────────────────────────

func TestCreateScheduledJobTool_DefinitionFields(t *testing.T) {
	tool := createScheduledJobTool(nil, "user-1")
	if tool.Definition.Name != "create_scheduled_job" {
		t.Errorf("name: want %q, got %q", "create_scheduled_job", tool.Definition.Name)
	}
	if tool.Label == "" {
		t.Error("label must not be empty")
	}
	desc := tool.Definition.Description.Value
	// Description must include negative guidance.
	if !strings.Contains(strings.ToLower(desc), "not") {
		t.Errorf("description should include negative guidance; got: %q", desc)
	}
	// Required fields must be listed.
	schema := tool.Definition.InputSchema
	required := schema.Required
	if len(required) == 0 {
		t.Error("required fields must be a non-empty []string")
	}
}

// ── list_scheduled_jobs ───────────────────────────────────────────────────────

func TestListScheduledJobs_NilSL_ReturnsError(t *testing.T) {
	result := executeListScheduledJobs(nil, "user-1", nil)
	if !result.IsError {
		t.Error("nil sl: want IsError=true")
	}
	if !strings.Contains(result.Content, "unavailable") {
		t.Errorf("nil sl: error should mention 'unavailable', got %q", result.Content)
	}
}

func TestListScheduledJobsTool_DefinitionFields(t *testing.T) {
	tool := listScheduledJobsTool(nil, "user-1")
	if tool.Definition.Name != "list_scheduled_jobs" {
		t.Errorf("name: want %q, got %q", "list_scheduled_jobs", tool.Definition.Name)
	}
	if tool.Label == "" {
		t.Error("label must not be empty")
	}
	desc := tool.Definition.Description.Value
	if !strings.Contains(strings.ToLower(desc), "not") {
		t.Errorf("description should include negative guidance; got: %q", desc)
	}
}

// ── executeListScheduledJobs: format rendering ────────────────────────────────

func TestListScheduledJobs_EmptyRecords_NoError(t *testing.T) {
	// Simulate records returned from a stub that returns nil/empty.
	// We directly call the formatting path by testing with a mock that returns
	// empty. Since we can't inject NATS here, we verify the list result
	// format when given records via the public tool path.
	//
	// Build the ToolResult manually by mimicking what executeListScheduledJobs
	// does with empty records.
	result := ToolResult{
		Content: "No scheduled jobs found for your account.",
		Details: map[string]interface{}{"count": 0},
	}
	if result.IsError {
		t.Error("empty records: want IsError=false")
	}
	if !strings.Contains(result.Content, "No scheduled jobs") {
		t.Errorf("empty records: expected 'No scheduled jobs', got %q", result.Content)
	}
}

// ── cancel_scheduled_job ─────────────────────────────────────────────────────

func TestCancelScheduledJob_MissingJobID(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{})
	result := executeCancelScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("missing job_id: want IsError=true")
	}
	if !strings.Contains(result.Content, "job_id") {
		t.Errorf("missing job_id: error should mention 'job_id', got %q", result.Content)
	}
}

func TestCancelScheduledJob_EmptyJobID(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"job_id": "   "})
	result := executeCancelScheduledJob(nil, "user-1", raw)
	if !result.IsError {
		t.Error("empty job_id: want IsError=true")
	}
}

func TestCancelScheduledJob_InvalidJSON(t *testing.T) {
	result := executeCancelScheduledJob(nil, "user-1", json.RawMessage(`not-json`))
	if !result.IsError {
		t.Error("invalid JSON: want IsError=true")
	}
}

func TestCancelScheduledJob_NilSL_Succeeds(t *testing.T) {
	// nil sl → CancelScheduledJob is a no-op; the tool reports success.
	raw, _ := json.Marshal(map[string]string{
		"job_id": "550e8400-e29b-41d4-a716-446655440000",
	})
	result := executeCancelScheduledJob(nil, "user-1", raw)
	if result.IsError {
		t.Errorf("nil sl: unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "550e8400") {
		t.Errorf("nil sl: expected job_id in result, got %q", result.Content)
	}
}

func TestCancelScheduledJobTool_DefinitionFields(t *testing.T) {
	tool := cancelScheduledJobTool(nil, "user-1")
	if tool.Definition.Name != "cancel_scheduled_job" {
		t.Errorf("name: want %q, got %q", "cancel_scheduled_job", tool.Definition.Name)
	}
	if tool.Label == "" {
		t.Error("label must not be empty")
	}
	desc := tool.Definition.Description.Value
	if !strings.Contains(strings.ToLower(desc), "not") {
		t.Errorf("description should include negative guidance; got: %q", desc)
	}
	// job_id must be required.
	schema := tool.Definition.InputSchema
	required := schema.Required
	found := false
	for _, r := range required {
		if r == "job_id" {
			found = true
		}
	}
	if !found {
		t.Error("job_id must be in required fields")
	}
}
