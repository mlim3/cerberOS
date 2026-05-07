package tests

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestTaskExecutionLogs(t *testing.T) {
	taskID := uuid.New().String()
	agentID := uuid.New().String()

	t.Run("Chronological Task Execution Logs", func(t *testing.T) {
		steps := []struct {
			ActionType string
			Status     string
		}{
			{"reasoning_step", "pending"},
			{"tool_call", "success"},
			{"final_answer", "failed"},
		}

		for _, step := range steps {
			reqBody := map[string]interface{}{
				"agentId":    agentID,
				"actionType": step.ActionType,
				"payload":    map[string]interface{}{"key": "value"},
				"status":     step.Status,
			}

			resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/agent/%s/executions", taskID), reqBody, nil)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("Failed to create task execution: expected 201, got %d", resp.StatusCode)
			}

			var createResp map[string]interface{}
			parseResponse(t, resp, &createResp)
			data := createResp["data"].(map[string]interface{})
			if data["executionId"] == nil || data["createdAt"] == nil {
				t.Fatalf("Expected executionId and createdAt in response")
			}
		}

		resp := doRequest(t, "GET", fmt.Sprintf("/api/v1/agent/%s/executions?limit=2", taskID), nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to get task executions: expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		data := result["data"].(map[string]interface{})
		executions := data["executions"].([]interface{})

		if len(executions) != 2 {
			t.Fatalf("Expected 2 task executions due to limit, got %d", len(executions))
		}

		for i, execAny := range executions {
			exec := execAny.(map[string]interface{})
			if exec["actionType"] != steps[i].ActionType {
				t.Errorf("Step %d: expected actionType %s, got %v", i, steps[i].ActionType, exec["actionType"])
			}
			if exec["status"] != steps[i].Status {
				t.Errorf("Step %d: expected status %s, got %v", i, steps[i].Status, exec["status"])
			}
		}
	})
}
