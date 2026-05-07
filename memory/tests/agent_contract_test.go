package tests

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAgentContract_BlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	taskID := uuid.NewString()
	agentID := uuid.NewString()

	postExecution := func(t *testing.T, routePrefix, actionType, status string) apiEnvelope {
		t.Helper()
		httpStatus, env := apiJSONRequest(t, http.MethodPost, baseURL+routePrefix+taskID+"/executions", map[string]any{
			"agentId":    agentID,
			"actionType": actionType,
			"payload": map[string]any{
				"source": "blackbox-agent-test",
			},
			"status": status,
		}, nil)
		if httpStatus != http.StatusCreated {
			t.Fatalf("post %s status = %d, want %d", routePrefix, httpStatus, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
		assertJSONHasNonEmptyStringField(t, env.Data, "executionId")
		assertJSONHasNonEmptyStringField(t, env.Data, "createdAt")
		return env
	}

	assertExecutionsList := func(t *testing.T, routePrefix string) {
		t.Helper()
		httpStatus, env := apiJSONRequest(t, http.MethodGet, baseURL+routePrefix+taskID+"/executions?limit=10", nil, nil)
		if httpStatus != http.StatusOK {
			t.Fatalf("get %s status = %d, want %d", routePrefix, httpStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		var payload map[string]any
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal execution list payload: %v", err)
		}

		executions, ok := payload["executions"].([]any)
		if !ok {
			t.Fatalf("executions field missing or not an array: %s", string(env.Data))
		}
		if len(executions) < 2 {
			t.Fatalf("executions length = %d, want at least 2; data = %s", len(executions), string(env.Data))
		}

		for i, execAny := range executions {
			execObj, ok := execAny.(map[string]any)
			if !ok {
				t.Fatalf("execution %d is not an object: %#v", i, execAny)
			}
			if strings.TrimSpace(asString(execObj["actionType"])) == "" {
				t.Fatalf("execution %d missing actionType: %#v", i, execObj)
			}
			if strings.TrimSpace(asString(execObj["status"])) == "" {
				t.Fatalf("execution %d missing status: %#v", i, execObj)
			}
		}
	}

	t.Run("singular_and_legacy_plural_routes_both_accept_execution_creation", func(t *testing.T) {
		postExecution(t, "/api/v1/agent/", "reasoning_step", "pending")
		postExecution(t, "/api/v1/agents/tasks/", "tool_call", "success")
	})

	t.Run("singular_route_lists_shared_execution_history", func(t *testing.T) {
		assertExecutionsList(t, "/api/v1/agent/")
	})

	t.Run("legacy_plural_route_lists_shared_execution_history", func(t *testing.T) {
		assertExecutionsList(t, "/api/v1/agents/tasks/")
	})
}
