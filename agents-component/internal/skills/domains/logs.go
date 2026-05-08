// Package domains contains the static skill domain definitions registered with
// the Skill Hierarchy Manager (M4) at component startup. Each exported function
// returns a fully-constructed SkillNode tree for one top-level domain.
//
// All commands must satisfy the Tool Contract (EDD §13.2): label required,
// description ≤300 chars with negative guidance, every parameter must have
// a description. The skills.ValidateCommandContract function enforces this
// at registration time.
package domains

import "github.com/cerberOS/agents-component/pkg/types"

// LogsDomain returns the "logs" skill domain, which provides read-only access
// to system event logs and agent execution history stored in the Memory Component.
//
// All four commands in this domain route as state.read.request messages to the
// Orchestrator — no vault-delegated execution or external credentials are needed.
// The Orchestrator fulfils reads via the Memory Component's log endpoints:
//
//	logs.query  → GET /api/v1/system/events
//	logs.search → GET /api/v1/system/events/search
//	logs.tail   → GET /api/v1/system/events?serviceName=X&limit=N
//	logs.agent  → GET /api/v1/agents/{agentId}/logs
func LogsDomain() *types.SkillNode {
	return &types.SkillNode{
		Name:  "logs",
		Level: "domain",
		Children: map[string]*types.SkillNode{
			"logs.query": {
				Name:                    "logs.query",
				Level:                   "command",
				Label:                   "Query System Event Logs",
				Description:             "Filter system event logs by severity, service name, or time range. Returns structured entries. Do not use for keyword search — use logs.search for that.",
				TimeoutSeconds:          30,
				RequiredCredentialTypes: []string{},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"severity": {
							Type:        "string",
							Required:    false,
							Description: "Severity level to filter on: 'info', 'warn', or 'error'. Omit to return all severities.",
						},
						"service_name": {
							Type:        "string",
							Required:    false,
							Description: "Name of the service that emitted the events (e.g. 'orchestrator', 'memory-api', 'vault'). Omit to query all services.",
						},
						"after": {
							Type:        "string",
							Required:    false,
							Description: "Return only events created after this ISO 8601 timestamp (e.g. '2026-04-22T10:00:00Z'). Omit for no lower bound.",
						},
						"before": {
							Type:        "string",
							Required:    false,
							Description: "Return only events created before this ISO 8601 timestamp. Omit for no upper bound.",
						},
						"limit": {
							Type:        "integer",
							Required:    false,
							Description: "Maximum number of events to return. Defaults to 50; maximum is 200.",
						},
					},
				},
			},
			"logs.search": {
				Name:                    "logs.search",
				Level:                   "command",
				Label:                   "Full-Text Search System Event Logs",
				Description:             "Search log messages by keyword or phrase using full-text search. Returns ranked results. Do not use for structured filtering by severity or time — use logs.query for that.",
				TimeoutSeconds:          30,
				RequiredCredentialTypes: []string{},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"query": {
							Type:        "string",
							Required:    true,
							Description: "Plain-text keyword or phrase to search for in log messages. Do not use SQL, regex, or tsquery syntax — plain English only.",
						},
						"service_name": {
							Type:        "string",
							Required:    false,
							Description: "Restrict the search to a specific service. Omit to search across all services.",
						},
						"limit": {
							Type:        "integer",
							Required:    false,
							Description: "Maximum number of matching events to return. Defaults to 20; maximum is 100.",
						},
					},
				},
			},
			"logs.tail": {
				Name:                    "logs.tail",
				Level:                   "command",
				Label:                   "Tail Recent Log Entries",
				Description:             "Retrieve the most recent N log entries from a specific service. Useful for checking current service activity. Do not use for time-range analysis — use logs.query with 'after'/'before' for that.",
				TimeoutSeconds:          15,
				RequiredCredentialTypes: []string{},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"service_name": {
							Type:        "string",
							Required:    true,
							Description: "The service to retrieve recent logs for (e.g. 'orchestrator', 'memory-api', 'vault', 'agents').",
						},
						"n": {
							Type:        "integer",
							Required:    false,
							Description: "Number of most-recent entries to return. Defaults to 20; maximum is 100.",
						},
					},
				},
			},
			"logs.agent": {
				Name:                    "logs.agent",
				Level:                   "command",
				Label:                   "Retrieve Agent Execution History",
				Description:             "Fetch the step-by-step execution log for an agent: tool calls, reasoning steps, and final answers in chronological order. Do not use for system-level events — use logs.query for those.",
				TimeoutSeconds:          30,
				RequiredCredentialTypes: []string{},
				Spec: &types.SkillSpec{
					Parameters: map[string]types.ParameterDef{
						"agent_id": {
							Type:        "string",
							Required:    true,
							Description: "UUID of the agent whose execution history to retrieve.",
						},
						"task_id": {
							Type:        "string",
							Required:    false,
							Description: "Restrict results to a specific task ID. Omit to return the agent's full history across all tasks.",
						},
						"limit": {
							Type:        "integer",
							Required:    false,
							Description: "Maximum number of execution log entries to return. Defaults to 50; maximum is 200.",
						},
					},
				},
			},
		},
	}
}
