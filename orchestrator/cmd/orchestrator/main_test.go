package main

import (
	"encoding/json"
	"strconv"
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
	if rt.mockNATS == nil {
		t.Fatal("runtime mock NATS is nil in demo mode")
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

	if err := rt.mockNATS.Deliver(gateway.TopicTasksInbound, msg); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}

	published := rt.mockNATS.Published[gateway.TopicAgentTasksInbound]
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

func TestBuildRuntime_PlannerResultTransitionsTaskToPlanActive(t *testing.T) {
	rt, err := buildRuntime(demoConfig())
	if err != nil {
		t.Fatalf("buildRuntime() error = %v", err)
	}
	if err := rt.gateway.Start(); err != nil {
		t.Fatalf("gateway.Start() error = %v", err)
	}
	if rt.mockNATS == nil || rt.mockMemory == nil {
		t.Fatal("runtime mocks are nil in demo mode")
	}

	task := types.UserTask{
		TaskID:               "550e8400-e29b-41d4-a716-446655440001",
		UserID:               "user-2",
		RequiredSkillDomains: []string{"general"},
		Priority:             5,
		TimeoutSeconds:       60,
		Payload:              json.RawMessage(`{"raw_input":"make a sandwich"}`),
		CallbackTopic:        "aegis.user-io.results.task-2",
	}
	rawTask, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("json.Marshal(task) error = %v", err)
	}
	inbound, err := json.Marshal(types.MessageEnvelope{
		MessageID:       "msg-2",
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
	if err := rt.mockNATS.Deliver(gateway.TopicTasksInbound, inbound); err != nil {
		t.Fatalf("Deliver(task inbound) error = %v", err)
	}

	plannerMsg := rt.mockNATS.LastPublished(gateway.TopicAgentTasksInbound)
	if plannerMsg == nil {
		t.Fatal("no planner task published")
	}
	var plannerEnvelope types.MessageEnvelope
	if err := json.Unmarshal(plannerMsg, &plannerEnvelope); err != nil {
		t.Fatalf("json.Unmarshal(planner envelope) error = %v", err)
	}

	planJSON := `{
	  "plan_id":"plan-sandwich-1",
	  "parent_task_id":"550e8400-e29b-41d4-a716-446655440001",
	  "created_at":"2026-04-05T22:44:49Z",
	  "subtasks":[
	    {
	      "subtask_id":"subtask-1",
	      "required_skill_domains":["general"],
	      "action":"prepare",
	      "instructions":"Prepare ingredients",
	      "params":{},
	      "depends_on":[],
	      "timeout_seconds":30
	    }
	  ]
	}`
	resultPayload := `{"task_id":"` + plannerEnvelope.CorrelationID + `","agent_id":"agent-1","success":true,"output":` + strconv.Quote(planJSON) + `,"trace_id":"` + plannerEnvelope.CorrelationID + `"}`
	resultMsg, err := json.Marshal(types.MessageEnvelope{
		MessageID:       "msg-3",
		MessageType:     "task.result",
		SourceComponent: "aegis-agents",
		CorrelationID:   plannerEnvelope.CorrelationID,
		Timestamp:       time.Now().UTC(),
		SchemaVersion:   "1.0",
		Payload:         json.RawMessage(resultPayload),
	})
	if err != nil {
		t.Fatalf("json.Marshal(result envelope) error = %v", err)
	}
	if err := rt.mockNATS.Deliver(gateway.TopicTaskResult, resultMsg); err != nil {
		t.Fatalf("Deliver(task result) error = %v", err)
	}

	ts, err := rt.mockMemory.GetTaskState(task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskState() error = %v", err)
	}
	if ts.PlanID != "plan-sandwich-1" {
		t.Fatalf("plan_id = %q, want plan-sandwich-1", ts.PlanID)
	}
	foundPlanActive := false
	for _, event := range ts.StateHistory {
		if event.State == types.StatePlanActive {
			foundPlanActive = true
			break
		}
	}
	if !foundPlanActive {
		t.Fatalf("state history = %#v, want a %s transition", ts.StateHistory, types.StatePlanActive)
	}

	planRecords, err := rt.mockMemory.Read(types.MemoryQuery{
		TaskID:   task.TaskID,
		DataType: types.DataTypePlanState,
	})
	if err != nil {
		t.Fatalf("Read(plan_state) error = %v", err)
	}
	if len(planRecords) == 0 {
		t.Fatal("expected persisted plan_state record after planner result")
	}
}
