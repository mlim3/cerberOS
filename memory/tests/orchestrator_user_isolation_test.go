package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// secondTestUserID is a second seeded user used to assert MT-4 (#185) tenant
// isolation: a record written as user A must not surface in queries scoped to
// user B, even when both records share the same task_id.
const secondTestUserID = "00000000-0000-0000-0000-000000000002"

func ensureSecondTestUser(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := dbPool.Exec(ctx, `
INSERT INTO identity_schema.users (id, email, role)
VALUES ($1, 'mt4-isolation@example.com', 'user')
ON CONFLICT (id) DO NOTHING`,
		secondTestUserID)
	if err != nil {
		t.Fatalf("seed second test user: %v", err)
	}
}

// TestOrchestratorRecordsAreTenantIsolated is the MT-4 (#185) AC check:
// writing the same task_id as two different users must produce two distinct
// rows, and each user must only see their own.
func TestOrchestratorRecordsAreTenantIsolated(t *testing.T) {
	ensureSecondTestUser(t)

	base := blackboxBaseURL()
	headers := map[string]string{"X-Internal-API-Key": vaultKey}
	taskID := fmt.Sprintf("task-mt4-iso-%d", time.Now().UnixNano())
	orchRefA := "orch-A-" + uuid.NewString()
	orchRefB := "orch-B-" + uuid.NewString()

	writeFor := func(t *testing.T, userID, orchRef, state string) {
		t.Helper()
		status, env := apiJSONRequest(t, http.MethodPost, base+"/api/v1/orchestrator/records", map[string]any{
			"user_id":               userID,
			"orchestrator_task_ref": orchRef,
			"task_id":               taskID,
			"data_type":             "task_state",
			"timestamp":             time.Now().UTC().Format(time.RFC3339Nano),
			"payload": map[string]any{
				"task_id": taskID,
				"state":   state,
				"owner":   userID,
			},
		}, headers)
		if status != http.StatusCreated {
			t.Fatalf("write status = %d, want %d (env=%+v)", status, http.StatusCreated, env)
		}
	}

	writeFor(t, testUserID, orchRefA, "RUNNING_A")
	writeFor(t, secondTestUserID, orchRefB, "RUNNING_B")

	queryFor := func(t *testing.T, userID string) []map[string]any {
		t.Helper()
		status, env := apiJSONRequest(t, http.MethodGet,
			base+"/api/v1/orchestrator/records?data_type=task_state&user_id="+userID+"&task_id="+taskID,
			nil, headers)
		if status != http.StatusOK {
			t.Fatalf("query status = %d, want %d", status, http.StatusOK)
		}
		var payload struct {
			Records []map[string]any `json:"records"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal records: %v", err)
		}
		return payload.Records
	}

	recsA := queryFor(t, testUserID)
	if len(recsA) != 1 {
		t.Fatalf("user A records = %d, want 1", len(recsA))
	}
	if got := recsA[0]["payload"].(map[string]any)["state"]; got != "RUNNING_A" {
		t.Fatalf("user A payload.state = %v, want RUNNING_A", got)
	}

	recsB := queryFor(t, secondTestUserID)
	if len(recsB) != 1 {
		t.Fatalf("user B records = %d, want 1", len(recsB))
	}
	if got := recsB[0]["payload"].(map[string]any)["state"]; got != "RUNNING_B" {
		t.Fatalf("user B payload.state = %v, want RUNNING_B", got)
	}

	// Cross-user query: user C (the second user) asking for user A's data type
	// must NOT see user A's row even though task_id matches.
	status, env := apiJSONRequest(t, http.MethodGet,
		base+"/api/v1/orchestrator/records/latest?user_id="+secondTestUserID+"&task_id="+taskID+"&data_type=task_state",
		nil, headers)
	if status != http.StatusOK {
		t.Fatalf("latest(B) status = %d, want %d", status, http.StatusOK)
	}
	var latest struct {
		Record struct {
			Payload map[string]any `json:"payload"`
		} `json:"record"`
	}
	if err := json.Unmarshal(env.Data, &latest); err != nil {
		t.Fatalf("unmarshal latest: %v", err)
	}
	if got := latest.Record.Payload["state"]; got != "RUNNING_B" {
		t.Fatalf("latest(B).state = %v, want RUNNING_B (must not leak user A's row)", got)
	}
}

// TestOrchestratorWriteRejectsMissingUserID guards the validation path: an
// orchestrator record write without a user_id must be rejected at the HTTP
// boundary so that the storage layer never sees a NULL-tenant insert.
func TestOrchestratorWriteRejectsMissingUserID(t *testing.T) {
	base := blackboxBaseURL()
	headers := map[string]string{"X-Internal-API-Key": vaultKey}
	taskID := fmt.Sprintf("task-mt4-missing-uid-%d", time.Now().UnixNano())

	status, env := apiJSONRequest(t, http.MethodPost, base+"/api/v1/orchestrator/records", map[string]any{
		"orchestrator_task_ref": "orch-missing-uid",
		"task_id":               taskID,
		"data_type":             "task_state",
		"timestamp":             time.Now().UTC().Format(time.RFC3339),
		"payload":               map[string]any{"state": "RUNNING"},
	}, headers)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	assertErrorCode(t, env, "invalid_argument")
}

// TestOrchestratorQueryRejectsMissingUserID guards the read path equivalent
// of the above — reads without user_id are programming errors and must 400.
func TestOrchestratorQueryRejectsMissingUserID(t *testing.T) {
	base := blackboxBaseURL()
	headers := map[string]string{"X-Internal-API-Key": vaultKey}

	status, env := apiJSONRequest(t, http.MethodGet,
		base+"/api/v1/orchestrator/records?data_type=task_state&task_id=anything",
		nil, headers)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	assertErrorCode(t, env, "invalid_argument")
}
