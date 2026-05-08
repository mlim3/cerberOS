// Package main — tools_logs.go implements the "logs" skill domain tools.
//
// All four commands route as state.read.request messages (DataType "system_log")
// to the Orchestrator, which forwards them to the Memory Component's log
// endpoints. No vault-delegated execution or external credentials are required.
//
// Routing per command:
//
//	logs_query  → ContextTag "logs.query"  → GET /api/v1/system/events
//	logs_search → ContextTag "logs.search" → GET /api/v1/system/events/search
//	logs_tail   → ContextTag "logs.tail"   → GET /api/v1/system/events?serviceName=X&limit=N
//	logs_agent  → ContextTag "logs.agent"  → GET /api/v1/agents/{agentId}/logs
//
// Execute functions retrieve the SessionLog from context (nil-safe) and call
// ReadSystemLogs, which handles subscribe/publish/wait internally.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// ── logs_query ────────────────────────────────────────────────────────────────

// logsQueryTool filters system event logs by severity, service name, or time range.
func logsQueryTool(_ *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Query System Event Logs",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "logs_query",
			Description: anthropic.String(
				"Filter system event logs by severity, service name, or time range. Returns structured log entries. " +
					"Do NOT use for keyword search — use logs_search for that. " +
					"Do NOT use to retrieve a specific agent's execution history — use logs_agent for that."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"severity": map[string]interface{}{
						"type":        "string",
						"description": "Severity level to filter on: 'info', 'warn', or 'error'. Omit to return all severities.",
						"enum":        []string{"info", "warn", "error"},
					},
					"service_name": map[string]interface{}{
						"type":        "string",
						"description": "Name of the service that emitted the events (e.g. 'orchestrator', 'memory-api', 'vault'). Omit to query all services.",
					},
					"after": map[string]interface{}{
						"type":        "string",
						"description": "Return only events created after this ISO 8601 timestamp (e.g. '2026-04-22T10:00:00Z'). Omit for no lower bound.",
					},
					"before": map[string]interface{}{
						"type":        "string",
						"description": "Return only events created before this ISO 8601 timestamp. Omit for no upper bound.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of events to return. Defaults to 50; maximum is 200.",
					},
				},
				Required: []string{},
			},
		},
		Execute: executeLogsQuery,
	}
}

func executeLogsQuery(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		Severity    string `json:"severity,omitempty"`
		ServiceName string `json:"service_name,omitempty"`
		After       string `json:"after,omitempty"`
		Before      string `json:"before,omitempty"`
		Limit       int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	queryParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode query params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	// SessionLog is threaded via context; nil-safe when NATS is absent.
	sl := SessionLogFromCtx(ctx)
	result := sl.ReadSystemLogs("logs.query", queryParams)
	return ToolResult{
		Content: result,
		Details: map[string]interface{}{
			"context_tag": "logs.query",
			"severity":    params.Severity,
			"service":     params.ServiceName,
		},
	}
}

// ── logs_search ───────────────────────────────────────────────────────────────

// logsSearchTool performs full-text search across system event log messages.
func logsSearchTool(_ *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Full-Text Search System Event Logs",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "logs_search",
			Description: anthropic.String(
				"Search log messages by keyword or phrase using full-text search. Returns ranked matching entries. " +
					"Do NOT use for structured filtering by severity or time range — use logs_query for that. " +
					"Do NOT use for agent execution history — use logs_agent for that."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Plain-text keyword or phrase to search for in log messages. Do not use SQL, regex, or tsquery syntax — plain English only.",
					},
					"service_name": map[string]interface{}{
						"type":        "string",
						"description": "Restrict the search to a specific service. Omit to search across all services.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of matching events to return. Defaults to 20; maximum is 100.",
					},
				},
				Required: []string{"query"},
			},
		},
		Execute: executeLogsSearch,
	}
}

func executeLogsSearch(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		Query       string `json:"query"`
		ServiceName string `json:"service_name,omitempty"`
		Limit       int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Query == "" {
		return ToolResult{
			Content: "query parameter is required",
			IsError: true,
		}
	}

	queryParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode query params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	sl := SessionLogFromCtx(ctx)
	result := sl.ReadSystemLogs("logs.search", queryParams)
	return ToolResult{
		Content: result,
		Details: map[string]interface{}{
			"context_tag": "logs.search",
			"query":       params.Query,
			"service":     params.ServiceName,
		},
	}
}

// ── logs_tail ─────────────────────────────────────────────────────────────────

// logsTailTool returns the most recent N log entries from a specific service.
func logsTailTool(_ *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Tail Recent Log Entries",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "logs_tail",
			Description: anthropic.String(
				"Retrieve the most recent N log entries from a specific service. " +
					"Useful for checking current service activity or health. " +
					"Do NOT use for time-range analysis — use logs_query with 'after'/'before' for that."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"service_name": map[string]interface{}{
						"type":        "string",
						"description": "The service to retrieve recent logs for (e.g. 'orchestrator', 'memory-api', 'vault', 'agents').",
					},
					"n": map[string]interface{}{
						"type":        "integer",
						"description": "Number of most-recent entries to return. Defaults to 20; maximum is 100.",
					},
				},
				Required: []string{"service_name"},
			},
		},
		Execute: executeLogsTail,
	}
}

func executeLogsTail(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		ServiceName string `json:"service_name"`
		N           int    `json:"n,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.ServiceName == "" {
		return ToolResult{
			Content: "service_name parameter is required",
			IsError: true,
		}
	}

	queryParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode query params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	sl := SessionLogFromCtx(ctx)
	result := sl.ReadSystemLogs("logs.tail", queryParams)
	return ToolResult{
		Content: result,
		Details: map[string]interface{}{
			"context_tag": "logs.tail",
			"service":     params.ServiceName,
			"n":           params.N,
		},
	}
}

// ── logs_agent ────────────────────────────────────────────────────────────────

// logsAgentTool retrieves the step-by-step execution log for a specific agent.
func logsAgentTool(_ *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Retrieve Agent Execution History",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "logs_agent",
			Description: anthropic.String(
				"Fetch the step-by-step execution log for an agent: tool calls, reasoning steps, and results in chronological order. " +
					"Do NOT use for system-level service events — use logs_query or logs_search for those."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "UUID of the agent whose execution history to retrieve.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Restrict results to a specific task ID. Omit to return the agent's full history across all tasks.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of execution log entries to return. Defaults to 50; maximum is 200.",
					},
				},
				Required: []string{"agent_id"},
			},
		},
		Execute: executeLogsAgent,
	}
}

func executeLogsAgent(ctx context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		AgentID string `json:"agent_id"`
		TaskID  string `json:"task_id,omitempty"`
		Limit   int    `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.AgentID == "" {
		return ToolResult{
			Content: "agent_id parameter is required",
			IsError: true,
		}
	}

	queryParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode query params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	sl := SessionLogFromCtx(ctx)
	result := sl.ReadSystemLogs("logs.agent", queryParams)
	return ToolResult{
		Content: result,
		Details: map[string]interface{}{
			"context_tag": "logs.agent",
			"agent_id":    params.AgentID,
			"task_id":     params.TaskID,
		},
	}
}
