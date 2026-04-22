package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestOrchestratorRecordsRequireInternalKey(t *testing.T) {
	status, env := apiJSONRequest(t, http.MethodGet, blackboxBaseURL()+"/api/v1/orchestrator/records?data_type=task_state&task_id=test-task", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	assertErrorCode(t, env, "invalid_argument")
}

func TestOrchestratorWriteRejectsInvalidDataType(t *testing.T) {
	status, env := apiJSONRequest(t, http.MethodPost, blackboxBaseURL()+"/api/v1/orchestrator/records", map[string]any{
		"orchestrator_task_ref": "orch-invalid",
		"task_id":               "task-invalid",
		"data_type":             "not_real",
		"timestamp":             time.Now().UTC().Format(time.RFC3339),
		"payload":               map[string]any{"state": "RECEIVED"},
	}, map[string]string{
		"X-Internal-API-Key": vaultKey,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	assertErrorCode(t, env, "invalid_argument")
}

func TestOrchestratorTaskStateUpserts(t *testing.T) {
	taskID := fmt.Sprintf("task-upsert-%d", time.Now().UnixNano())
	orchRef := fmt.Sprintf("orch-upsert-%d", time.Now().UnixNano())
	traceID := fmt.Sprintf("trace-upsert-%d", time.Now().UnixNano())
	base := blackboxBaseURL()
	headers := map[string]string{"X-Internal-API-Key": vaultKey}

	firstTS := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	secondTS := time.Now().UTC().Format(time.RFC3339)

	for _, tc := range []struct {
		timestamp string
		state     string
		retry     int
	}{
		{timestamp: firstTS, state: "RECEIVED", retry: 0},
		{timestamp: secondTS, state: "PLAN_ACTIVE", retry: 1},
	} {
		status, env := apiJSONRequest(t, http.MethodPost, base+"/api/v1/orchestrator/records", map[string]any{
			"orchestrator_task_ref": orchRef,
			"task_id":               taskID,
			"trace_id":              traceID,
			"data_type":             "task_state",
			"timestamp":             tc.timestamp,
			"payload": map[string]any{
				"task_id":     taskID,
				"state":       tc.state,
				"retry_count": tc.retry,
			},
		}, headers)
		if status != http.StatusCreated {
			t.Fatalf("write status = %d, want %d (env=%+v)", status, http.StatusCreated, env)
		}
		assertSuccessEnvelope(t, env)
	}

	status, env := apiJSONRequest(t, http.MethodGet, base+"/api/v1/orchestrator/records?data_type=task_state&task_id="+taskID, nil, headers)
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want %d", status, http.StatusOK)
	}
	assertSuccessEnvelope(t, env)

	var payload struct {
		Records []struct {
			TaskID    string                 `json:"task_id"`
			DataType  string                 `json:"data_type"`
			Timestamp time.Time              `json:"timestamp"`
			Payload   map[string]interface{} `json:"payload"`
		} `json:"records"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("unmarshal records payload: %v", err)
	}
	if len(payload.Records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(payload.Records))
	}
	if payload.Records[0].TaskID != taskID {
		t.Fatalf("task_id = %q, want %q", payload.Records[0].TaskID, taskID)
	}
	if got := payload.Records[0].Payload["state"]; got != "PLAN_ACTIVE" {
		t.Fatalf("payload.state = %v, want PLAN_ACTIVE", got)
	}

	status, env = apiJSONRequest(t, http.MethodGet, base+"/api/v1/orchestrator/records/latest?task_id="+taskID+"&data_type=task_state", nil, headers)
	if status != http.StatusOK {
		t.Fatalf("latest status = %d, want %d", status, http.StatusOK)
	}
	assertSuccessEnvelope(t, env)

	var latest struct {
		Record struct {
			TaskID  string                 `json:"task_id"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"record"`
	}
	if err := json.Unmarshal(env.Data, &latest); err != nil {
		t.Fatalf("unmarshal latest payload: %v", err)
	}
	if latest.Record.TaskID != taskID {
		t.Fatalf("latest task_id = %q, want %q", latest.Record.TaskID, taskID)
	}
	if got := latest.Record.Payload["state"]; got != "PLAN_ACTIVE" {
		t.Fatalf("latest payload.state = %v, want PLAN_ACTIVE", got)
	}
}

func TestOrchestratorAuditLogAppends(t *testing.T) {
	taskID := fmt.Sprintf("task-audit-%d", time.Now().UnixNano())
	orchRef := fmt.Sprintf("orch-audit-%d", time.Now().UnixNano())
	base := blackboxBaseURL()
	headers := map[string]string{"X-Internal-API-Key": vaultKey}

	for i, outcome := range []string{"success", "failed"} {
		status, env := apiJSONRequest(t, http.MethodPost, base+"/api/v1/orchestrator/records", map[string]any{
			"orchestrator_task_ref": orchRef,
			"task_id":               taskID,
			"data_type":             "audit_log",
			"timestamp":             time.Now().UTC().Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			"payload": map[string]any{
				"event_type":        "task_received",
				"initiating_module": "TaskDispatcher",
				"outcome":           outcome,
				"task_id":           taskID,
				"orchestrator_ref":  orchRef,
				"timestamp":         time.Now().UTC().Format(time.RFC3339),
			},
		}, headers)
		if status != http.StatusCreated {
			t.Fatalf("write status = %d, want %d (env=%+v)", status, http.StatusCreated, env)
		}
		assertSuccessEnvelope(t, env)
	}

	status, env := apiJSONRequest(t, http.MethodGet, base+"/api/v1/orchestrator/records?data_type=audit_log&task_id="+taskID, nil, headers)
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want %d", status, http.StatusOK)
	}
	assertSuccessEnvelope(t, env)

	var payload struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("unmarshal records payload: %v", err)
	}
	if len(payload.Records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(payload.Records))
	}
}

func TestOrchestratorQueryNotTerminalFiltersTerminalStates(t *testing.T) {
	base := blackboxBaseURL()
	headers := map[string]string{"X-Internal-API-Key": vaultKey}

	activeTaskID := fmt.Sprintf("task-active-%d", time.Now().UnixNano())
	activeRef := fmt.Sprintf("orch-active-%d", time.Now().UnixNano())
	terminalTaskID := fmt.Sprintf("task-terminal-%d", time.Now().UnixNano())
	terminalRef := fmt.Sprintf("orch-terminal-%d", time.Now().UnixNano())

	for _, tc := range []struct {
		taskID string
		orch   string
		state  string
	}{
		{taskID: activeTaskID, orch: activeRef, state: "RUNNING"},
		{taskID: terminalTaskID, orch: terminalRef, state: "COMPLETED"},
	} {
		status, env := apiJSONRequest(t, http.MethodPost, base+"/api/v1/orchestrator/records", map[string]any{
			"orchestrator_task_ref": tc.orch,
			"task_id":               tc.taskID,
			"data_type":             "task_state",
			"timestamp":             time.Now().UTC().Format(time.RFC3339),
			"payload": map[string]any{
				"task_id": tc.taskID,
				"state":   tc.state,
			},
		}, headers)
		if status != http.StatusCreated {
			t.Fatalf("write status = %d, want %d (env=%+v)", status, http.StatusCreated, env)
		}
		assertSuccessEnvelope(t, env)
	}

	status, env := apiJSONRequest(t, http.MethodGet, base+"/api/v1/orchestrator/records?data_type=task_state&state_filter=not_terminal", nil, headers)
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want %d", status, http.StatusOK)
	}
	assertSuccessEnvelope(t, env)

	var payload struct {
		Records []struct {
			TaskID  string                 `json:"task_id"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"records"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("unmarshal records payload: %v", err)
	}

	var sawActive, sawTerminal bool
	for _, rec := range payload.Records {
		switch rec.TaskID {
		case activeTaskID:
			sawActive = true
		case terminalTaskID:
			sawTerminal = true
		}
	}
	if !sawActive {
		t.Fatalf("active task %q missing from not_terminal results", activeTaskID)
	}
	if sawTerminal {
		t.Fatalf("terminal task %q unexpectedly present in not_terminal results", terminalTaskID)
	}
}
