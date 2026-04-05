package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/gateway"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

func TestBuildRuntime_WiresPlannerDispatchPath(t *testing.T) {
	rt, err := buildRuntime(demoConfig())
	if err != nil {
		t.Fatalf("buildRuntime() error = %v", err)
	}
	if rt.dispatcher == nil || rt.gateway == nil || rt.health == nil || rt.monitor == nil {
		t.Fatal("runtime has nil core components")
	}

	if err := rt.gateway.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}

	task := types.UserTask{
		TaskID:         "550e8400-e29b-41d4-a716-446655440000",
		UserID:         "user-1",
		Priority:       5,
		TimeoutSeconds: 60,
		Payload:        json.RawMessage(`{"raw_input":"book a flight from NYC to LA"}`),
		CallbackTopic:  "aegis.user-io.results.task-1",
	}
	rawTask, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("json.Marshal(task) error = %v", err)
	}
	msg, err := json.Marshal(types.MessageEnvelope{
		MessageID:       "msg-1",
		MessageType:     "user_task",
		SourceComponent: "user-io",
		CorrelationID:   task.TaskID,
		Timestamp:       time.Now().UTC(),
		SchemaVersion:   "1.0",
		Payload:         rawTask,
	})
	if err != nil {
		t.Fatalf("json.Marshal(envelope) error = %v", err)
	}

	if err := rt.nats.Deliver(gateway.TopicTasksInbound, msg); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	published := rt.nats.Published[gateway.TopicAgentTasksInbound]
	if len(published) != 1 {
		t.Fatalf("planner task publishes = %d, want 1", len(published))
	}

	var envelope types.MessageEnvelope
	if err := json.Unmarshal(published[0], &envelope); err != nil {
		t.Fatalf("json.Unmarshal(published envelope) error = %v", err)
	}
	if envelope.MessageType != "task.inbound" {
		t.Fatalf("envelope.MessageType = %q, want task.inbound", envelope.MessageType)
	}

	var plannerPayload struct {
		RequiredSkills []string `json:"required_skills"`
		Instructions   string   `json:"instructions"`
		TraceID        string   `json:"trace_id"`
	}
	if err := json.Unmarshal(envelope.Payload, &plannerPayload); err != nil {
		t.Fatalf("json.Unmarshal(planner payload) error = %v", err)
	}
	if len(plannerPayload.RequiredSkills) != 1 || plannerPayload.RequiredSkills[0] != "general" {
		t.Fatalf("required_skills = %v, want [general]", plannerPayload.RequiredSkills)
	}
	if plannerPayload.TraceID == "" {
		t.Fatal("planner trace_id is empty")
	}
	if plannerPayload.Instructions == "" {
		t.Fatal("planner instructions are empty")
	}
}
