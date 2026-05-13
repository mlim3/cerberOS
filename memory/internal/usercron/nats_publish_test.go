package usercron

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/memory/internal/storage"
	"github.com/nats-io/nats.go"
)

type fakeJetStream struct {
	nats.JetStreamContext
	subject string
	data    []byte
	err     error
}

func (f *fakeJetStream) Publish(subj string, data []byte, _ ...nats.PubOpt) (*nats.PubAck, error) {
	f.subject = subj
	f.data = append([]byte(nil), data...)
	if f.err != nil {
		return nil, f.err
	}
	return &nats.PubAck{Stream: "test", Sequence: 1}, nil
}

func TestDispatchUserCron_PreservesSchedulingContext(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"userId":               "00000000-0000-0000-0000-000000000001",
		"rawInput":             "Check flight deals and notify me if they drop.",
		"conversationId":       "conv-scheduled-123",
		"requiredSkillDomains": []string{"google_workspace", "web"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	jobID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	runID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	job := storage.ScheduledJob{
		ID:      jobID,
		Name:    "flight_watcher",
		Payload: payload,
		State:   []byte(`{"last_checked_price":580,"notified":true}`),
	}

	js := &fakeJetStream{}
	dispatcher := &NatsDispatcher{JS: js}

	if err := dispatcher.DispatchUserCron(context.Background(), job, runID); err != nil {
		t.Fatalf("DispatchUserCron() error = %v", err)
	}

	if js.subject != SubjectTaskInbound {
		t.Fatalf("publish subject = %q, want %q", js.subject, SubjectTaskInbound)
	}

	var env struct {
		MessageType     string `json:"message_type"`
		SourceComponent string `json:"source_component"`
		Payload         struct {
			UserID               string         `json:"user_id"`
			RequiredSkillDomains []string       `json:"required_skill_domains"`
			ConversationID       string         `json:"conversation_id"`
			Payload              map[string]any `json:"payload"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(js.data, &env); err != nil {
		t.Fatalf("unmarshal published envelope: %v", err)
	}

	if env.MessageType != "user_task" {
		t.Fatalf("message_type = %q, want %q", env.MessageType, "user_task")
	}
	if env.SourceComponent != "io" {
		t.Fatalf("source_component = %q, want %q", env.SourceComponent, "io")
	}
	if env.Payload.UserID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("payload.user_id = %q", env.Payload.UserID)
	}
	if env.Payload.ConversationID != "conv-scheduled-123" {
		t.Fatalf("payload.conversation_id = %q, want %q", env.Payload.ConversationID, "conv-scheduled-123")
	}
	if len(env.Payload.RequiredSkillDomains) != 2 {
		t.Fatalf("required_skill_domains length = %d, want 2", len(env.Payload.RequiredSkillDomains))
	}
	if env.Payload.RequiredSkillDomains[0] != "google_workspace" || env.Payload.RequiredSkillDomains[1] != "web" {
		t.Fatalf("required_skill_domains = %#v", env.Payload.RequiredSkillDomains)
	}

	rawInput, _ := env.Payload.Payload["raw_input"].(string)
	if rawInput != "Check flight deals and notify me if they drop." {
		t.Fatalf("raw_input = %q", rawInput)
	}
	jobName, _ := env.Payload.Payload["job_name"].(string)
	if jobName != "flight_watcher" {
		t.Fatalf("job_name = %q, want %q", jobName, "flight_watcher")
	}
	jobIDOut, _ := env.Payload.Payload["job_id"].(string)
	if jobIDOut != jobID.String() {
		t.Fatalf("job_id = %q, want %q", jobIDOut, jobID.String())
	}
	runIDOut, _ := env.Payload.Payload["run_id"].(string)
	if runIDOut != runID.String() {
		t.Fatalf("run_id = %q, want %q", runIDOut, runID.String())
	}
	jobStateOut, ok := env.Payload.Payload["job_state"].(map[string]any)
	if !ok {
		t.Fatalf("job_state missing or wrong type: %#v", env.Payload.Payload["job_state"])
	}
	if jobStateOut["last_checked_price"] != float64(580) {
		t.Fatalf("job_state.last_checked_price = %#v", jobStateOut["last_checked_price"])
	}
	systemPrompt, _ := env.Payload.Payload["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "scheduled execution") {
		t.Fatalf("system_prompt missing scheduled execution guidance: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "claim_action") || !strings.Contains(systemPrompt, "complete_action") {
		t.Fatalf("system_prompt missing idempotency guidance: %q", systemPrompt)
	}
}

func TestDispatchUserCron_RejectsMissingUserOrInput(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "missing userId",
			payload: map[string]any{
				"rawInput": "do thing",
			},
		},
		{
			name: "missing rawInput",
			payload: map[string]any{
				"userId": "00000000-0000-0000-0000-000000000001",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			err = (&NatsDispatcher{JS: &fakeJetStream{}}).DispatchUserCron(context.Background(), storage.ScheduledJob{
				ID:      uuid.New(),
				Name:    "broken-job",
				Payload: payload,
			}, uuid.New())
			if err == nil {
				t.Fatal("DispatchUserCron() error = nil, want error")
			}
			if !strings.Contains(err.Error(), "requires userId and rawInput") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
