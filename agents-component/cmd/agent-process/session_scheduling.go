// Package main — session_scheduling.go extends SessionLog with methods for
// creating, listing, and cancelling user-owned scheduled jobs.
//
// All writes are fire-and-forget state.write NATS messages (DataType
// "scheduled_job"); the Orchestrator's gateway routes them to the Memory
// Component's /api/v1/scheduled_jobs endpoint. Reads use the standard
// state.read.request / state.read.response round-trip.
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	nats "github.com/nats-io/nats.go"
)

// scheduledJobReadTimeout is the maximum time ListScheduledJobs waits for a
// state.read.response from the Orchestrator before giving up.
const scheduledJobReadTimeout = 10 * time.Second

// CreateScheduledJobParams holds every field needed to schedule a user_cron job.
type CreateScheduledJobParams struct {
	Name                 string    // human-readable job name
	RawInput             string    // the natural-language task payload dispatched at each firing
	ScheduleKind         string    // "interval" | "cron"
	CronExpression       string    // e.g. "0 9 * * 1"  (used when ScheduleKind == "cron")
	IntervalSeconds      int       // e.g. 3600  (used when ScheduleKind == "interval")
	NextRunAt            time.Time // first scheduled run; defaults to now+interval when zero
	UserContextID        string    // the user who owns the job
	RequiredSkillDomains []string  // skill domains to pre-authorize at dispatch (nil → unrestricted)
}

// CreateScheduledJob dispatches a new scheduled user_cron job via
// state.write → Orchestrator → Memory API.  The call returns after the NATS
// Publish succeeds; actual persistence is asynchronous.
//
// Returns nil when sl is nil (NATS unavailable).
func (sl *SessionLog) CreateScheduledJob(p CreateScheduledJobParams) error {
	if sl == nil {
		return nil
	}

	jobPayload := map[string]any{
		"jobType":        "user_cron",
		"targetKind":     "user",
		"targetService":  "orchestrator",
		"status":         "active",
		"scheduleKind":   p.ScheduleKind,
		"name":           p.Name,
		"userId":         p.UserContextID,
		"cronExpression": p.CronExpression,
		"payload": map[string]any{
			"userId":               p.UserContextID,
			"rawInput":             p.RawInput,
			"requiredSkillDomains": p.RequiredSkillDomains, // nil serialises as null; dispatcher treats null as unrestricted
		},
		"nextRunAt": p.NextRunAt.UTC().Format(time.RFC3339),
	}
	if p.ScheduleKind == "interval" && p.IntervalSeconds > 0 {
		jobPayload["intervalSeconds"] = float64(p.IntervalSeconds)
	}

	mw := types.MemoryWrite{
		AgentID:   sl.agentID,
		SessionID: sl.taskID,
		DataType:  "scheduled_job",
		Payload:   jobPayload,
		Tags: map[string]string{
			"operation": "create",
			"user_id":   p.UserContextID,
		},
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		TraceID:         sl.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("session log: create scheduled job: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		return fmt.Errorf("session log: create scheduled job: publish: %w", err)
	}
	sl.log.Info("session log: scheduled job creation dispatched",
		"name", p.Name, "schedule_kind", p.ScheduleKind,
		"user_id", p.UserContextID,
	)
	return nil
}

// ListScheduledJobs sends a state.read.request with DataType "scheduled_job"
// and waits for the Orchestrator to return the user's jobs from the Memory API.
// Each element of the returned slice is a raw JSON object with the fields
// returned by memory-api's scheduledJobToMap function (id, name, scheduleKind, …).
//
// Returns (nil, nil) when sl is nil.
func (sl *SessionLog) ListScheduledJobs(userContextID string) ([]json.RawMessage, error) {
	if sl == nil {
		return nil, nil
	}

	// Subscribe BEFORE publishing to avoid the race where the response
	// arrives before we start listening.
	sub, err := sl.js.SubscribeSync(
		comms.SubjectStateReadResponse,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		return nil, fmt.Errorf("session log: list scheduled jobs: subscribe: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	qp, _ := json.Marshal(map[string]string{"userId": userContextID})
	req := types.MemoryReadRequest{
		AgentID:     sl.agentID,
		DataType:    "scheduled_job",
		TraceID:     sl.traceID,
		QueryParams: qp,
	}
	reqEnv := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateReadRequest,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		TraceID:         sl.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         req,
	}
	data, err := json.Marshal(reqEnv)
	if err != nil {
		return nil, fmt.Errorf("session log: list scheduled jobs: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateReadRequest, data); err != nil {
		return nil, fmt.Errorf("session log: list scheduled jobs: publish: %w", err)
	}

	deadline := time.Now().Add(scheduledJobReadTimeout)
	for time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break // timeout or drain
		}
		_ = msg.Ack()

		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			sl.log.Warn("session log: list scheduled jobs: unmarshal envelope failed", "error", err)
			continue
		}

		// The orchestrator echoes AgentID in the response; filter to ours only.
		var resp struct {
			AgentID string            `json:"agent_id"`
			Records []json.RawMessage `json:"records"`
			TraceID string            `json:"trace_id"`
		}
		if err := json.Unmarshal(env.Payload, &resp); err != nil {
			sl.log.Warn("session log: list scheduled jobs: unmarshal payload failed", "error", err)
			continue
		}
		if resp.AgentID != sl.agentID {
			continue
		}
		if resp.Records == nil {
			resp.Records = []json.RawMessage{}
		}
		return resp.Records, nil
	}

	return nil, fmt.Errorf("session log: list scheduled jobs: timed out waiting for state.read.response")
}

// CancelScheduledJob dispatches a deletion request via state.write →
// Orchestrator → Memory API (DELETE /api/v1/scheduled_jobs/{jobId}).
// The call is fire-and-forget; actual deletion is asynchronous.
//
// Returns nil when sl is nil.
func (sl *SessionLog) CancelScheduledJob(jobID, userContextID string) error {
	if sl == nil {
		return nil
	}
	mw := types.MemoryWrite{
		AgentID:   sl.agentID,
		SessionID: sl.taskID,
		DataType:  "scheduled_job",
		Payload:   map[string]any{},
		Tags: map[string]string{
			"operation": "delete",
			"job_id":    jobID,
			"user_id":   userContextID,
		},
	}
	env := agentEnvelope{
		MessageID:       newUUID(),
		MessageType:     comms.MsgTypeStateWrite,
		SourceComponent: "agents",
		CorrelationID:   sl.taskID,
		TraceID:         sl.traceID,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		SchemaVersion:   "1.0",
		Payload:         mw,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("session log: cancel scheduled job: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		return fmt.Errorf("session log: cancel scheduled job: publish: %w", err)
	}
	sl.log.Info("session log: scheduled job cancellation dispatched",
		"job_id", jobID, "user_id", userContextID)
	return nil
}
