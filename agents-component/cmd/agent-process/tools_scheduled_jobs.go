// Package main — tools_scheduled_jobs.go implements the scheduling tools:
//
//   - create_scheduled_job: persists a recurring user_cron job in the Memory
//     Component and dispatches it via NATS on each firing.
//   - list_scheduled_jobs: returns all active scheduled jobs for the current user.
//   - cancel_scheduled_job: deletes a scheduled job by ID.
//
// All three tools route through the NATS state.write / state.read.request path
// so the agent never makes direct HTTP calls outside the Orchestrator boundary.
// The Orchestrator's gateway forwards these to the Memory API's
// /api/v1/scheduled_jobs and /api/v1/user_crons endpoints.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// ── create_scheduled_job ──────────────────────────────────────────────────────

// createScheduledJobTool returns a SkillTool that creates a recurring task.
func createScheduledJobTool(sl *SessionLog, userContextID string) SkillTool {
	return SkillTool{
		Label:                   "Create Scheduled Job",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "create_scheduled_job",
			Description: anthropic.String(
				"Schedule a recurring task that runs automatically on a fixed interval or cron schedule. " +
					"The task description (raw_input) is sent to a fresh agent on each firing, exactly as if " +
					"the user had typed it. " +
					"Use this when the user explicitly asks to automate a repeating task (e.g. daily reports, " +
					"weekly summaries, periodic checks). " +
					"Do NOT use for one-time tasks, tasks the user wants to run immediately, or tasks where " +
					"the schedule has not been explicitly requested. " +
					"Provide either interval_seconds (for fixed-interval repeats) or cron_expression " +
					"(for calendar-based schedules) — not both.",
			),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Short human-readable name for the job (e.g. \"daily_sales_report\"). Used in list and cancel operations.",
					},
					"raw_input": map[string]interface{}{
						"type":        "string",
						"description": "The task description dispatched to a new agent on each scheduled firing. Write it as if the user is speaking (e.g. \"Summarise yesterday's sales and send it to the team\").",
					},
					"interval_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "How often the job fires, in seconds (e.g. 3600 = every hour, 86400 = every day). Provide this OR cron_expression, not both.",
					},
					"cron_expression": map[string]interface{}{
						"type":        "string",
						"description": "Standard 5-field cron expression for calendar-based schedules (e.g. \"0 9 * * 1\" = every Monday at 09:00 UTC). Provide this OR interval_seconds, not both.",
					},
					"first_run_at": map[string]interface{}{
						"type":        "string",
						"description": "RFC3339 timestamp for the first run (e.g. \"2026-05-12T09:00:00Z\"). Defaults to now + interval_seconds when omitted.",
					},
					"required_skill_domains": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Skill domains this job needs to execute (e.g. [\"google_workspace\", \"web\"]). These are pre-authorized at every dispatch so the agent has credential access without user interaction. Omit only if the job uses no credentialed tools.",
					},
				},
				Required: []string{"name", "raw_input"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeCreateScheduledJob(sl, userContextID, raw)
		},
	}
}

func executeCreateScheduledJob(sl *SessionLog, userContextID string, raw json.RawMessage) ToolResult {
	var params struct {
		Name                 string   `json:"name"`
		RawInput             string   `json:"raw_input"`
		IntervalSeconds      int      `json:"interval_seconds"`
		CronExpression       string   `json:"cron_expression"`
		FirstRunAt           string   `json:"first_run_at"`
		RequiredSkillDomains []string `json:"required_skill_domains"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("create_scheduled_job: invalid parameters: %v", err),
			IsError: true,
		}
	}

	params.Name = strings.TrimSpace(params.Name)
	params.RawInput = strings.TrimSpace(params.RawInput)
	params.CronExpression = strings.TrimSpace(params.CronExpression)

	if params.Name == "" {
		return ToolResult{Content: "create_scheduled_job: name is required", IsError: true}
	}
	if params.RawInput == "" {
		return ToolResult{Content: "create_scheduled_job: raw_input is required", IsError: true}
	}
	if params.IntervalSeconds > 0 && params.CronExpression != "" {
		return ToolResult{
			Content: "create_scheduled_job: provide interval_seconds OR cron_expression, not both",
			IsError: true,
		}
	}
	if params.IntervalSeconds <= 0 && params.CronExpression == "" {
		return ToolResult{
			Content: "create_scheduled_job: provide either interval_seconds or cron_expression",
			IsError: true,
		}
	}

	scheduleKind := "cron"
	if params.IntervalSeconds > 0 {
		scheduleKind = "interval"
	}

	// Determine first run time.
	var nextRunAt time.Time
	if params.FirstRunAt != "" {
		var parseErr error
		nextRunAt, parseErr = time.Parse(time.RFC3339, params.FirstRunAt)
		if parseErr != nil {
			return ToolResult{
				Content: fmt.Sprintf("create_scheduled_job: invalid first_run_at (must be RFC3339): %v", parseErr),
				IsError: true,
			}
		}
	}
	if nextRunAt.IsZero() {
		if scheduleKind == "interval" && params.IntervalSeconds > 0 {
			nextRunAt = time.Now().UTC().Add(time.Duration(params.IntervalSeconds) * time.Second)
		} else {
			// For cron schedules without explicit first_run_at, default to 1 minute from now
			// as a safe placeholder; the memory-api scheduleutil computes the real next
			// occurrence when the job first runs.
			nextRunAt = time.Now().UTC().Add(time.Minute)
		}
	}

	p := CreateScheduledJobParams{
		Name:                 params.Name,
		RawInput:             params.RawInput,
		ScheduleKind:         scheduleKind,
		CronExpression:       params.CronExpression,
		IntervalSeconds:      params.IntervalSeconds,
		NextRunAt:            nextRunAt,
		UserContextID:        userContextID,
		RequiredSkillDomains: params.RequiredSkillDomains,
	}
	if sl != nil {
		p.ConversationID = sl.conversationID
	}

	if err := sl.CreateScheduledJob(p); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("create_scheduled_job: failed to create job: %v", err),
			IsError: true,
		}
	}

	scheduleDesc := fmt.Sprintf("cron %q", params.CronExpression)
	if scheduleKind == "interval" {
		scheduleDesc = fmt.Sprintf("every %d seconds", params.IntervalSeconds)
	}

	return ToolResult{
		Content: fmt.Sprintf(
			"Scheduled job %q created successfully (%s, first run ~%s). "+
				"Use list_scheduled_jobs to see the job ID.",
			params.Name,
			scheduleDesc,
			nextRunAt.UTC().Format(time.RFC3339),
		),
		Details: map[string]interface{}{
			"name":          params.Name,
			"schedule_kind": scheduleKind,
			"next_run_at":   nextRunAt.UTC().Format(time.RFC3339),
		},
	}
}

// ── list_scheduled_jobs ───────────────────────────────────────────────────────

// listScheduledJobsTool returns a SkillTool that lists the user's scheduled jobs.
func listScheduledJobsTool(sl *SessionLog, userContextID string) SkillTool {
	return SkillTool{
		Label:                   "List Scheduled Jobs",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "list_scheduled_jobs",
			Description: anthropic.String(
				"List all scheduled jobs for the current user. " +
					"Returns each job's ID, name, schedule (interval or cron), next run time, and status. " +
					"Use this to discover job IDs needed for cancel_scheduled_job, or to confirm a job " +
					"was created successfully. " +
					"Do NOT call this speculatively — only when the user asks to see their scheduled tasks " +
					"or when you need the job ID for a follow-up operation.",
			),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeListScheduledJobs(sl, userContextID, raw)
		},
	}
}

func executeListScheduledJobs(sl *SessionLog, userContextID string, _ json.RawMessage) ToolResult {
	if sl == nil {
		return ToolResult{
			Content: "list_scheduled_jobs: NATS unavailable — cannot retrieve scheduled jobs",
			IsError: true,
		}
	}

	records, err := sl.ListScheduledJobs(userContextID)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("list_scheduled_jobs: failed to retrieve jobs: %v", err),
			IsError: true,
		}
	}

	if len(records) == 0 {
		return ToolResult{
			Content: "No scheduled jobs found for your account.",
			Details: map[string]interface{}{"count": 0},
		}
	}

	// Format each job for the LLM.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d scheduled job(s):\n\n", len(records)))
	for i, rawRec := range records {
		// Each record is a MemoryWrite envelope; extract Payload.
		var envelope struct {
			DataType string          `json:"data_type"`
			Payload  json.RawMessage `json:"payload"`
		}
		rawPayload := rawRec
		if err := json.Unmarshal(rawRec, &envelope); err == nil && envelope.Payload != nil {
			rawPayload = envelope.Payload
		}

		var job map[string]interface{}
		if err := json.Unmarshal(rawPayload, &job); err != nil {
			sb.WriteString(fmt.Sprintf("%d. (unparseable record)\n", i+1))
			continue
		}

		id, _ := job["id"].(string)
		name, _ := job["name"].(string)
		scheduleKind, _ := job["scheduleKind"].(string)
		status, _ := job["status"].(string)
		nextRunAt, _ := job["nextRunAt"].(string)
		cronExpr, _ := job["cronExpression"].(string)

		sb.WriteString(fmt.Sprintf("%d. %s (id: %s)\n", i+1, name, id))
		sb.WriteString(fmt.Sprintf("   Status: %s\n", status))
		if scheduleKind == "interval" {
			if iv, ok := job["intervalSeconds"].(float64); ok {
				sb.WriteString(fmt.Sprintf("   Schedule: every %.0f seconds\n", iv))
			}
		} else if scheduleKind == "cron" {
			sb.WriteString(fmt.Sprintf("   Schedule: cron %q\n", cronExpr))
		}
		sb.WriteString(fmt.Sprintf("   Next run: %s\n\n", nextRunAt))
	}

	return ToolResult{
		Content: sb.String(),
		Details: map[string]interface{}{"count": len(records)},
	}
}

// ── cancel_scheduled_job ──────────────────────────────────────────────────────

// cancelScheduledJobTool returns a SkillTool that deletes a scheduled job.
func cancelScheduledJobTool(sl *SessionLog, userContextID string) SkillTool {
	return SkillTool{
		Label:                   "Cancel Scheduled Job",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          15,
		Definition: anthropic.ToolParam{
			Name: "cancel_scheduled_job",
			Description: anthropic.String(
				"Cancel (delete) a scheduled job so it no longer fires. " +
					"Requires the job ID, which you can obtain from list_scheduled_jobs. " +
					"Use this when the user explicitly asks to stop, delete, or cancel a recurring task. " +
					"Do NOT guess the job ID — always call list_scheduled_jobs first if you do not have it. " +
					"Cancellation is permanent and cannot be undone.",
			),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"job_id": map[string]interface{}{
						"type":        "string",
						"description": "UUID of the scheduled job to cancel. Obtain via list_scheduled_jobs.",
					},
				},
				Required: []string{"job_id"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			return executeCancelScheduledJob(sl, userContextID, raw)
		},
	}
}

func executeCancelScheduledJob(sl *SessionLog, userContextID string, raw json.RawMessage) ToolResult {
	var params struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("cancel_scheduled_job: invalid parameters: %v", err),
			IsError: true,
		}
	}
	params.JobID = strings.TrimSpace(params.JobID)
	if params.JobID == "" {
		return ToolResult{Content: "cancel_scheduled_job: job_id is required", IsError: true}
	}

	if err := sl.CancelScheduledJob(params.JobID, userContextID); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("cancel_scheduled_job: failed to cancel job: %v", err),
			IsError: true,
		}
	}

	return ToolResult{
		Content: fmt.Sprintf(
			"Scheduled job %q has been cancelled. It will no longer fire.",
			params.JobID,
		),
		Details: map[string]interface{}{
			"job_id":  params.JobID,
			"deleted": true,
		},
	}
}
