package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	nats "github.com/nats-io/nats.go"
)

const idempotencyReadTimeout = 10 * time.Second

type ClaimActionParams struct {
	Key        string
	TTLSeconds int
	JobID      string
	RunID      string
}

type ClaimActionResult struct {
	Claimed     bool            `json:"claimed"`
	Key         string          `json:"key"`
	Status      string          `json:"status"`
	AgentID     string          `json:"agentId"`
	JobID       string          `json:"jobId,omitempty"`
	RunID       string          `json:"runId,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	ClaimedAt   string          `json:"claimedAt,omitempty"`
	CompletedAt string          `json:"completedAt,omitempty"`
	ExpiresAt   string          `json:"expiresAt,omitempty"`
}

type CompleteActionParams struct {
	Key      string
	Status   string
	Result   any
	JobID    string
	RunID    string
	JobState map[string]any
}

func (sl *SessionLog) ClaimAction(p ClaimActionParams) (ClaimActionResult, error) {
	if sl == nil {
		return ClaimActionResult{}, nil
	}

	sub, err := sl.js.SubscribeSync(
		comms.SubjectStateReadResponse,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		return ClaimActionResult{}, fmt.Errorf("session log: claim action: subscribe: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	qp, err := json.Marshal(map[string]any{
		"key":        p.Key,
		"agentId":    sl.agentID,
		"jobId":      p.JobID,
		"runId":      p.RunID,
		"ttlSeconds": p.TTLSeconds,
	})
	if err != nil {
		return ClaimActionResult{}, fmt.Errorf("session log: claim action: marshal params: %w", err)
	}

	req := types.MemoryReadRequest{
		AgentID:     sl.agentID,
		DataType:    "idempotency_claim",
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
		return ClaimActionResult{}, fmt.Errorf("session log: claim action: marshal request: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateReadRequest, data); err != nil {
		return ClaimActionResult{}, fmt.Errorf("session log: claim action: publish request: %w", err)
	}

	deadline := time.Now().Add(idempotencyReadTimeout)
	for time.Now().Before(deadline) {
		msg, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break
		}
		_ = msg.Ack()

		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			continue
		}
		var resp struct {
			AgentID string            `json:"agent_id"`
			Records []json.RawMessage `json:"records"`
		}
		if err := json.Unmarshal(env.Payload, &resp); err != nil {
			continue
		}
		if resp.AgentID != sl.agentID || len(resp.Records) == 0 {
			continue
		}

		var wrapped struct {
			DataType string          `json:"data_type"`
			Payload  json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(resp.Records[0], &wrapped); err != nil {
			return ClaimActionResult{}, fmt.Errorf("session log: claim action: decode wrapped record: %w", err)
		}
		var result ClaimActionResult
		if err := json.Unmarshal(wrapped.Payload, &result); err != nil {
			return ClaimActionResult{}, fmt.Errorf("session log: claim action: decode payload: %w", err)
		}
		return result, nil
	}

	return ClaimActionResult{}, fmt.Errorf("session log: claim action: timed out waiting for state.read.response")
}

func (sl *SessionLog) CompleteAction(p CompleteActionParams) error {
	if sl == nil {
		return nil
	}
	mw := types.MemoryWrite{
		AgentID:   sl.agentID,
		SessionID: sl.taskID,
		DataType:  "idempotency_complete",
		Payload: map[string]any{
			"key":      p.Key,
			"status":   p.Status,
			"result":   p.Result,
			"jobId":    p.JobID,
			"runId":    p.RunID,
			"jobState": p.JobState,
		},
		Tags: map[string]string{
			"operation": "complete",
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
		return fmt.Errorf("session log: complete action: marshal: %w", err)
	}
	if _, err := sl.js.Publish(comms.SubjectStateWrite, data); err != nil {
		return fmt.Errorf("session log: complete action: publish: %w", err)
	}
	return nil
}
