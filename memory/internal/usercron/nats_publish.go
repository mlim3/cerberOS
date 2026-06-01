package usercron

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/nats-io/nats.go"
)

const (
	SubjectTaskInbound   = "aegis.orchestrator.tasks.inbound"
	IOResultsTopicPrefix = "aegis.io.results."
)

// NatsDispatcher publishes one user_task envelope (same shape as the IO service)
// to the orchestrator JetStream subject.
type NatsDispatcher struct {
	JS nats.JetStreamContext
}

type payloadBody struct {
	UserID               string   `json:"userId"`
	RawInput             string   `json:"rawInput"`
	ConversationID       string   `json:"conversationId,omitempty"`
	RequiredSkillDomains []string `json:"requiredSkillDomains,omitempty"` // nil → unrestricted scope at spawn
}

type envelope struct {
	MessageID       string `json:"message_id"`
	MessageType     string `json:"message_type"`
	SourceComponent string `json:"source_component"`
	CorrelationID   string `json:"correlation_id"`
	TraceID         string `json:"trace_id,omitempty"`
	Timestamp       string `json:"timestamp"`
	SchemaVersion   string `json:"schema_version"`
	Payload         any    `json:"payload"`
}

type innerUserTask struct {
	TaskID               string   `json:"task_id"`
	UserID               string   `json:"user_id"`
	RequiredSkillDomains []string `json:"required_skill_domains"`
	Priority             int      `json:"priority"`
	TimeoutSeconds       int      `json:"timeout_seconds"`
	Payload              any      `json:"payload"`
	CallbackTopic        string   `json:"callback_topic"`
	ConversationID       string   `json:"conversation_id,omitempty"`
}

// DispatchUserCron publishes the job to the orchestrator JetStream path.
func (d *NatsDispatcher) DispatchUserCron(ctx context.Context, job storage.ScheduledJob, runID uuid.UUID) error {
	if d == nil || d.JS == nil {
		return fmt.Errorf("nats dispatcher not configured")
	}
	_ = ctx
	var p payloadBody
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("parse user cron payload: %w", err)
	}
	if p.UserID == "" || p.RawInput == "" {
		return fmt.Errorf("user cron payload requires userId and rawInput")
	}
	taskID := uuid.NewString()
	inner := map[string]any{
		"raw_input": p.RawInput,
		// Tell the planner this is one scheduled execution — don't interpret
		// scheduling language ("every X min") as an infrastructure requirement.
		// The cron infrastructure already handles timing; the planner must execute
		// the described action ONCE using whatever skill domain it needs (e.g.
		// google_workspace for Gmail/Calendar).
		"system_prompt": "This is one scheduled execution of a recurring task. " +
			"The scheduling infrastructure already handles timing — your job is to execute the action described ONCE right now using available tools. " +
			"Route to the appropriate skill domain (e.g. google_workspace for Gmail/Calendar reads and sends, web for search). " +
			"Ignore any frequency language in the task (\"every X minutes\", \"daily\", etc.) — that is metadata for the scheduler, not an instruction to implement recurring loops or timers. " +
			"Before taking any side effect that must not repeat, use claim_action. When the run finishes, call complete_action with the same key and include updated job_state if you changed the durable state.",
		"user_cron":   true,
		"source":      "user_cron",
		"job_id":      job.ID.String(),
		"job_name":    job.Name,
		"run_id":      runID.String(),
		"maintenance": false,
	}
	var jobState any = map[string]any{}
	if len(job.State) > 0 {
		_ = json.Unmarshal(job.State, &jobState)
	}
	inner["job_state"] = jobState
	inner["system_prompt"] = inner["system_prompt"].(string) +
		fmt.Sprintf(" Scheduled job context: job_id=%s run_id=%s job_state_json=%s.",
			job.ID.String(),
			runID.String(),
			strings.TrimSpace(string(job.State)),
		)
	itu := innerUserTask{
		TaskID:               taskID,
		UserID:               p.UserID,
		RequiredSkillDomains: p.RequiredSkillDomains, // nil → vault returns unrestricted scope; explicit list → scoped pre-auth at spawn
		Priority:             5,
		TimeoutSeconds:       1800,
		Payload:              inner,
		CallbackTopic:        IOResultsTopicPrefix + taskID,
		ConversationID:       p.ConversationID,
	}
	tid := uuid.NewString()
	hex := ""
	for _, c := range tid {
		if c != '-' {
			hex += string(c)
		}
		if len(hex) >= 32 {
			break
		}
	}
	for len(hex) < 32 {
		hex += "0"
	}
	env := envelope{
		MessageID:       uuid.NewString(),
		MessageType:     "user_task",
		SourceComponent: "io",
		CorrelationID:   taskID,
		TraceID:         hex[:32],
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         itu,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = d.JS.Publish(SubjectTaskInbound, data)
	return err
}
