package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

func claimActionTool(sl *SessionLog) SkillTool {
	return SkillTool{
		Label:                   "Claim Action",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "claim_action",
			Description: anthropic.String(
				"Atomically claim a side effect before performing it. " +
					"Use this right before actions that must not happen twice across overlapping recurring runs " +
					"(for example sending an email, creating a calendar event, or posting a notification). " +
					"Do NOT call this after the side effect already happened.",
			),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"key": map[string]any{
						"type":        "string",
						"description": "Deterministic idempotency key for the action being guarded.",
					},
					"ttl_seconds": map[string]any{
						"type":        "integer",
						"description": "How long the action should remain claimed/completed before allowing it again.",
					},
					"job_id": map[string]any{
						"type":        "string",
						"description": "Optional scheduled job ID associated with this action.",
					},
					"run_id": map[string]any{
						"type":        "string",
						"description": "Optional scheduled run ID associated with this action.",
					},
				},
				Required: []string{"key", "ttl_seconds"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeClaimAction(sl, raw)
		},
	}
}

func executeClaimAction(sl *SessionLog, raw json.RawMessage) ToolResult {
	var params struct {
		Key        string `json:"key"`
		TTLSeconds int    `json:"ttl_seconds"`
		JobID      string `json:"job_id"`
		RunID      string `json:"run_id"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("claim_action: invalid parameters: %v", err), IsError: true}
	}
	params.Key = strings.TrimSpace(params.Key)
	if params.Key == "" {
		return ToolResult{Content: "claim_action: key is required", IsError: true}
	}
	if params.TTLSeconds <= 0 {
		return ToolResult{Content: "claim_action: ttl_seconds must be positive", IsError: true}
	}
	if sl == nil {
		return ToolResult{Content: "claim_action: NATS unavailable — cannot claim action", IsError: true}
	}

	result, err := sl.ClaimAction(ClaimActionParams{
		Key:        params.Key,
		TTLSeconds: params.TTLSeconds,
		JobID:      strings.TrimSpace(params.JobID),
		RunID:      strings.TrimSpace(params.RunID),
	})
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("claim_action: failed to claim action: %v", err), IsError: true}
	}

	status := strings.TrimSpace(result.Status)
	if status == "" {
		status = "claimed"
	}
	content := fmt.Sprintf("Action key %q claimed successfully.", params.Key)
	if !result.Claimed {
		content = fmt.Sprintf("Action key %q was already %s; skip the side effect.", params.Key, status)
	}

	return ToolResult{
		Content: content,
		Details: map[string]any{
			"claimed":      result.Claimed,
			"key":          result.Key,
			"status":       status,
			"agent_id":     result.AgentID,
			"job_id":       result.JobID,
			"run_id":       result.RunID,
			"claimed_at":   result.ClaimedAt,
			"completed_at": result.CompletedAt,
			"expires_at":   result.ExpiresAt,
		},
	}
}

func completeActionTool(sl *SessionLog) SkillTool {
	return SkillTool{
		Label:                   "Complete Action",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "complete_action",
			Description: anthropic.String(
				"Record the final outcome of a previously claimed side effect and optionally persist updated recurring job state. " +
					"Use this after a claimed action finishes. " +
					"Do NOT call this before the guarded work has been attempted.",
			),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"key": map[string]any{
						"type":        "string",
						"description": "The same idempotency key used with claim_action.",
					},
					"status": map[string]any{
						"type":        "string",
						"description": "Optional completion status such as completed or failed.",
					},
					"result": map[string]any{
						"type":        "object",
						"description": "Structured result details for auditing/debugging.",
					},
					"job_id": map[string]any{
						"type":        "string",
						"description": "Optional scheduled job ID to update job state for.",
					},
					"run_id": map[string]any{
						"type":        "string",
						"description": "Optional scheduled run ID to mark complete.",
					},
					"job_state": map[string]any{
						"type":        "object",
						"description": "Optional durable recurring job state to persist atomically with completion.",
					},
				},
				Required: []string{"key"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeCompleteAction(sl, raw)
		},
	}
}

func executeCompleteAction(sl *SessionLog, raw json.RawMessage) ToolResult {
	var params struct {
		Key      string         `json:"key"`
		Status   string         `json:"status"`
		Result   map[string]any `json:"result"`
		JobID    string         `json:"job_id"`
		RunID    string         `json:"run_id"`
		JobState map[string]any `json:"job_state"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("complete_action: invalid parameters: %v", err), IsError: true}
	}
	params.Key = strings.TrimSpace(params.Key)
	if params.Key == "" {
		return ToolResult{Content: "complete_action: key is required", IsError: true}
	}
	if sl == nil {
		return ToolResult{Content: "complete_action: NATS unavailable — cannot complete action", IsError: true}
	}
	if params.Result == nil {
		params.Result = map[string]any{}
	}

	if err := sl.CompleteAction(CompleteActionParams{
		Key:      params.Key,
		Status:   strings.TrimSpace(params.Status),
		Result:   params.Result,
		JobID:    strings.TrimSpace(params.JobID),
		RunID:    strings.TrimSpace(params.RunID),
		JobState: params.JobState,
	}); err != nil {
		return ToolResult{Content: fmt.Sprintf("complete_action: failed to record completion: %v", err), IsError: true}
	}

	status := strings.TrimSpace(params.Status)
	if status == "" {
		status = "completed"
	}
	return ToolResult{
		Content: fmt.Sprintf("Action key %q marked %s.", params.Key, status),
		Details: map[string]any{
			"key":       params.Key,
			"status":    status,
			"job_id":    strings.TrimSpace(params.JobID),
			"run_id":    strings.TrimSpace(params.RunID),
			"job_state": params.JobState,
			"result":    params.Result,
		},
	}
}
