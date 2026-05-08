package usercron

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/mlim3/cerberOS/memory/internal/storage"
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
	UserID         string `json:"userId"`
	RawInput       string `json:"rawInput"`
	ConversationID string `json:"conversationId,omitempty"`
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
func (d *NatsDispatcher) DispatchUserCron(ctx context.Context, job storage.ScheduledJob) error {
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
		"raw_input":   p.RawInput,
		"user_cron":   true,
		"source":      "user_cron",
		"job_id":      job.ID.String(),
		"job_name":    job.Name,
		"maintenance": false,
	}
	itu := innerUserTask{
		TaskID:               taskID,
		UserID:               p.UserID,
		RequiredSkillDomains: []string{"general"},
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
