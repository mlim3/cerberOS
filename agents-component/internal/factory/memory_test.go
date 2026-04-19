// Package factory — memory_test.go tests the private memory-fetch helpers and
// LoadSynthesizedSkills using the in-process stubs for Memory and Skills.
package factory

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/internal/credentials"
	"github.com/cerberOS/agents-component/internal/lifecycle"
	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTestFactory(t *testing.T, mem memory.Client, sm skills.Manager) *Factory {
	t.Helper()
	if sm == nil {
		sm = skills.New()
		_ = sm.RegisterDomain(&types.SkillNode{Name: "web", Level: "domain", Children: map[string]*types.SkillNode{}})
	}
	if mem == nil {
		mem = memory.New()
	}
	f, err := New(Config{
		Registry:    registry.New(),
		Skills:      sm,
		Credentials: credentials.New(map[string]string{"web.credential": "tok"}),
		Lifecycle:   lifecycle.New(),
		Memory:      mem,
		Comms:       comms.NewStubClient(),
		GenerateID:  func() string { return "test-agent" },
		Log:         slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	return f
}

func writeMemory(t *testing.T, mem memory.Client, agentID, dataType, contextTag string, payload interface{}) {
	t.Helper()
	if err := mem.Write(&types.MemoryWrite{
		AgentID:  agentID,
		DataType: dataType,
		Payload:  payload,
		Tags:     map[string]string{"context": contextTag},
	}); err != nil {
		t.Fatalf("memory.Write: %v", err)
	}
}

// validSynthesizedCommand returns a SkillNode that satisfies the Tool Contract.
func validSynthesizedCommand(name string) *types.SkillNode {
	return &types.SkillNode{
		Name:        name,
		Level:       "command",
		Label:       "Test Command",
		Description: "Does something useful. Do NOT use when X.",
		Origin:      "synthesized",
		Spec: &types.SkillSpec{
			Parameters: map[string]types.ParameterDef{
				"input": {Type: "string", Required: true, Description: "The input value."},
			},
		},
	}
}

// ─── joinMemoryPayloads ───────────────────────────────────────────────────────

func TestJoinMemoryPayloads_EmptyRecords_ReturnsEmpty(t *testing.T) {
	got := joinMemoryPayloads(nil, 4000)
	if got != "" {
		t.Errorf("empty records: want \"\", got %q", got)
	}
}

func TestJoinMemoryPayloads_SingleStringPayload(t *testing.T) {
	records := []types.MemoryWrite{{Payload: "fact one"}}
	got := joinMemoryPayloads(records, 4000)
	if got != "fact one" {
		t.Errorf("single payload: want %q, got %q", "fact one", got)
	}
}

func TestJoinMemoryPayloads_MultipleStringPayloads_NewlineJoined(t *testing.T) {
	records := []types.MemoryWrite{
		{Payload: "fact one"},
		{Payload: "fact two"},
		{Payload: "fact three"},
	}
	got := joinMemoryPayloads(records, 4000)
	if !strings.Contains(got, "fact one") || !strings.Contains(got, "fact two") || !strings.Contains(got, "fact three") {
		t.Errorf("all payloads must appear in output; got %q", got)
	}
	parts := strings.Split(got, "\n")
	if len(parts) != 3 {
		t.Errorf("want 3 newline-separated parts, got %d: %q", len(parts), got)
	}
}

func TestJoinMemoryPayloads_TruncatesAtMaxChars(t *testing.T) {
	// Each payload is 100 chars; maxChars=250 allows only 2 full entries (200 chars + 1 newline = 201 < 250).
	payload := strings.Repeat("a", 100)
	records := []types.MemoryWrite{
		{Payload: payload},
		{Payload: payload},
		{Payload: payload}, // third should be excluded
	}
	got := joinMemoryPayloads(records, 250)
	parts := strings.Split(got, "\n")
	if len(parts) != 2 {
		t.Errorf("at maxChars=250 with 100-char payloads, want 2 parts, got %d: %q", len(parts), got)
	}
}

func TestJoinMemoryPayloads_MapPayload_Marshaled(t *testing.T) {
	// After a JSON round-trip through the Memory Component, payloads become
	// map[string]interface{}. joinMemoryPayloads must marshal them to JSON.
	records := []types.MemoryWrite{
		{Payload: map[string]interface{}{"key": "value"}},
	}
	got := joinMemoryPayloads(records, 4000)
	if !strings.Contains(got, "key") || !strings.Contains(got, "value") {
		t.Errorf("map payload must be JSON-marshaled into output; got %q", got)
	}
}

// ─── fetchAgentMemory ─────────────────────────────────────────────────────────

func TestFetchAgentMemory_EmptyDomain_ReturnsEmpty(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	if got := f.fetchAgentMemory("", "trace-1"); got != "" {
		t.Errorf("empty domain: want \"\", got %q", got)
	}
}

func TestFetchAgentMemory_NoRecords_ReturnsEmpty(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	if got := f.fetchAgentMemory("web", "trace-1"); got != "" {
		t.Errorf("no records: want \"\", got %q", got)
	}
}

func TestFetchAgentMemory_SingleRecord_ReturnsFact(t *testing.T) {
	mem := memory.New()
	writeMemory(t, mem, "domain:web", "agent_memory", "agent_memory", "API returns paginated results.")
	f := newTestFactory(t, mem, nil)

	got := f.fetchAgentMemory("web", "trace-1")
	if !strings.Contains(got, "paginated results") {
		t.Errorf("want fact in output; got %q", got)
	}
}

func TestFetchAgentMemory_MultipleRecords_AllJoined(t *testing.T) {
	mem := memory.New()
	writeMemory(t, mem, "domain:web", "agent_memory", "agent_memory", "fact one")
	writeMemory(t, mem, "domain:web", "agent_memory", "agent_memory", "fact two")
	f := newTestFactory(t, mem, nil)

	got := f.fetchAgentMemory("web", "trace-1")
	if !strings.Contains(got, "fact one") || !strings.Contains(got, "fact two") {
		t.Errorf("want both facts; got %q", got)
	}
}

func TestFetchAgentMemory_UnknownDomain_ReturnsEmpty(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	if got := f.fetchAgentMemory("nonexistent", "trace-1"); got != "" {
		t.Errorf("unknown domain: want \"\", got %q", got)
	}
}

func TestFetchAgentMemory_DomainIsolation(t *testing.T) {
	mem := memory.New()
	writeMemory(t, mem, "domain:web", "agent_memory", "agent_memory", "web fact")
	writeMemory(t, mem, "domain:data", "agent_memory", "agent_memory", "data fact")
	f := newTestFactory(t, mem, nil)

	got := f.fetchAgentMemory("web", "trace-1")
	if strings.Contains(got, "data fact") {
		t.Errorf("web memory must not contain data-domain facts; got %q", got)
	}
	if !strings.Contains(got, "web fact") {
		t.Errorf("web memory must contain web-domain fact; got %q", got)
	}
}

// ─── fetchUserProfile ─────────────────────────────────────────────────────────

func TestFetchUserProfile_EmptyUserContextID_ReturnsEmpty(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	if got := f.fetchUserProfile("", "trace-1"); got != "" {
		t.Errorf("empty userContextID: want \"\", got %q", got)
	}
}

func TestFetchUserProfile_NoRecords_ReturnsEmpty(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	if got := f.fetchUserProfile("ctx-unknown", "trace-1"); got != "" {
		t.Errorf("no records: want \"\", got %q", got)
	}
}

func TestFetchUserProfile_SingleRecord_ReturnsObservation(t *testing.T) {
	mem := memory.New()
	writeMemory(t, mem, "user:ctx-1", "user_profile", "user_profile", "User prefers bullet points.")
	f := newTestFactory(t, mem, nil)

	got := f.fetchUserProfile("ctx-1", "trace-1")
	if !strings.Contains(got, "bullet points") {
		t.Errorf("want observation in output; got %q", got)
	}
}

func TestFetchUserProfile_MultipleRecords_AllJoined(t *testing.T) {
	mem := memory.New()
	writeMemory(t, mem, "user:ctx-2", "user_profile", "user_profile", "pref one")
	writeMemory(t, mem, "user:ctx-2", "user_profile", "user_profile", "pref two")
	f := newTestFactory(t, mem, nil)

	got := f.fetchUserProfile("ctx-2", "trace-1")
	if !strings.Contains(got, "pref one") || !strings.Contains(got, "pref two") {
		t.Errorf("want both prefs; got %q", got)
	}
}

func TestFetchUserProfile_UserIsolation(t *testing.T) {
	mem := memory.New()
	writeMemory(t, mem, "user:ctx-a", "user_profile", "user_profile", "pref for user A")
	writeMemory(t, mem, "user:ctx-b", "user_profile", "user_profile", "pref for user B")
	f := newTestFactory(t, mem, nil)

	got := f.fetchUserProfile("ctx-a", "trace-1")
	if strings.Contains(got, "user B") {
		t.Errorf("user A profile must not contain user B prefs; got %q", got)
	}
	if !strings.Contains(got, "user A") {
		t.Errorf("user A profile must contain user A pref; got %q", got)
	}
}

// ─── LoadSynthesizedSkills ────────────────────────────────────────────────────

func TestLoadSynthesizedSkills_NoRecords_ReturnsNilError(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	if err := f.LoadSynthesizedSkills(context.Background()); err != nil {
		t.Errorf("no records: want nil error, got %v", err)
	}
}

func TestLoadSynthesizedSkills_ValidRecord_RegisteredIntoSkills(t *testing.T) {
	mem := memory.New()
	node := validSynthesizedCommand("web_paginate")

	if err := mem.Write(&types.MemoryWrite{
		AgentID:  "domain:web",
		DataType: "skill_cache",
		Payload:  node,
		Tags:     map[string]string{"domain": "web", "origin": "synthesized", "skill_name": "web_paginate"},
	}); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}

	sm := skills.New()
	_ = sm.RegisterDomain(&types.SkillNode{Name: "web", Level: "domain", Children: map[string]*types.SkillNode{}})
	f := newTestFactory(t, mem, sm)

	if err := f.LoadSynthesizedSkills(context.Background()); err != nil {
		t.Fatalf("LoadSynthesizedSkills: %v", err)
	}

	cmds, err := f.skills.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	found := false
	for _, c := range cmds {
		if c.Name == "web_paginate" {
			found = true
		}
	}
	if !found {
		t.Errorf("synthesized skill 'web_paginate' not found in domain commands after load")
	}
}

func TestLoadSynthesizedSkills_MissingDomainTag_Skipped(t *testing.T) {
	mem := memory.New()
	// Record with no "domain" tag — must be silently skipped.
	if err := mem.Write(&types.MemoryWrite{
		AgentID:  "domain:web",
		DataType: "skill_cache",
		Payload:  validSynthesizedCommand("web_foo"),
		Tags:     map[string]string{"origin": "synthesized"}, // no "domain" key
	}); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}

	sm := skills.New()
	_ = sm.RegisterDomain(&types.SkillNode{Name: "web", Level: "domain", Children: map[string]*types.SkillNode{}})
	f := newTestFactory(t, mem, sm)

	// Must not error — skipped gracefully.
	if err := f.LoadSynthesizedSkills(context.Background()); err != nil {
		t.Errorf("missing domain tag must be skipped gracefully; got error %v", err)
	}

	cmds, _ := f.skills.GetCommands("web")
	for _, c := range cmds {
		if c.Name == "web_foo" {
			t.Error("skill with missing domain tag must not be registered")
		}
	}
}

func TestLoadSynthesizedSkills_UnknownDomain_Skipped(t *testing.T) {
	mem := memory.New()
	// Skill tagged for "unknown" domain which is not registered.
	if err := mem.Write(&types.MemoryWrite{
		AgentID:  "domain:unknown",
		DataType: "skill_cache",
		Payload:  validSynthesizedCommand("unknown_cmd"),
		Tags:     map[string]string{"domain": "unknown"},
	}); err != nil {
		t.Fatalf("mem.Write: %v", err)
	}

	// Only "web" is registered, not "unknown".
	f := newTestFactory(t, mem, nil)

	// Must not error — unknown domain is skipped gracefully.
	if err := f.LoadSynthesizedSkills(context.Background()); err != nil {
		t.Errorf("unknown domain must be skipped gracefully; got error %v", err)
	}
}

func TestLoadSynthesizedSkills_MultipleValidRecords_AllLoaded(t *testing.T) {
	mem := memory.New()
	for _, name := range []string{"web_paginate", "web_retry", "web_extract"} {
		if err := mem.Write(&types.MemoryWrite{
			AgentID:  "domain:web",
			DataType: "skill_cache",
			Payload:  validSynthesizedCommand(name),
			Tags:     map[string]string{"domain": "web", "skill_name": name},
		}); err != nil {
			t.Fatalf("mem.Write(%s): %v", name, err)
		}
	}

	sm := skills.New()
	_ = sm.RegisterDomain(&types.SkillNode{Name: "web", Level: "domain", Children: map[string]*types.SkillNode{}})
	f := newTestFactory(t, mem, sm)

	if err := f.LoadSynthesizedSkills(context.Background()); err != nil {
		t.Fatalf("LoadSynthesizedSkills: %v", err)
	}

	cmds, err := f.skills.GetCommands("web")
	if err != nil {
		t.Fatalf("GetCommands: %v", err)
	}
	cmdNames := make(map[string]bool)
	for _, c := range cmds {
		cmdNames[c.Name] = true
	}
	for _, want := range []string{"web_paginate", "web_retry", "web_extract"} {
		if !cmdNames[want] {
			t.Errorf("skill %q not loaded into domain", want)
		}
	}
}

func TestLoadSynthesizedSkills_UpsertReplacesExisting(t *testing.T) {
	// Load the same command name twice — second should replace first.
	mem := memory.New()
	for i := 0; i < 2; i++ {
		node := validSynthesizedCommand("web_paginate")
		node.Label = "Version " + string(rune('A'+i))
		if err := mem.Write(&types.MemoryWrite{
			AgentID:  "domain:web",
			DataType: "skill_cache",
			Payload:  node,
			Tags:     map[string]string{"domain": "web", "skill_name": "web_paginate"},
		}); err != nil {
			t.Fatalf("mem.Write: %v", err)
		}
	}

	sm := skills.New()
	_ = sm.RegisterDomain(&types.SkillNode{Name: "web", Level: "domain", Children: map[string]*types.SkillNode{}})
	f := newTestFactory(t, mem, sm)

	// Must not error — upsert is valid.
	if err := f.LoadSynthesizedSkills(context.Background()); err != nil {
		t.Fatalf("LoadSynthesizedSkills with duplicate: %v", err)
	}

	cmds, _ := f.skills.GetCommands("web")
	count := 0
	for _, c := range cmds {
		if c.Name == "web_paginate" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("upsert: want exactly 1 'web_paginate' command, got %d", count)
	}
}

func TestLoadSynthesizedSkills_MemoryReadError_ReturnedAsError(t *testing.T) {
	// errMemory is a memory.Client that always errors on ReadAllByType.
	f := newTestFactory(t, &errMemoryClient{}, nil)
	err := f.LoadSynthesizedSkills(context.Background())
	if err == nil {
		t.Error("read error must be propagated as an error")
	}
}

// ─── fetchPriorTurns ──────────────────────────────────────────────────────────

// writeConversationSnapshot seeds the in-process memory stub with a
// ConversationSnapshot record, mirroring what WriteConversationSnapshot writes.
func writeConversationSnapshot(t *testing.T, mem memory.Client, snap types.ConversationSnapshot) {
	t.Helper()
	if err := mem.Write(&types.MemoryWrite{
		AgentID:  "conversation:" + snap.ConversationID,
		DataType: "conversation_snapshot",
		Payload:  snap,
		Tags: map[string]string{
			"context":         "conversation_snapshot",
			"conversation_id": snap.ConversationID,
		},
	}); err != nil {
		t.Fatalf("writeConversationSnapshot: %v", err)
	}
}

// marshalTurns builds a []json.RawMessage from a slice of user messages.
func marshalTurns(t *testing.T, contents []string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, 0, len(contents))
	for _, c := range contents {
		msg := anthropic.NewUserMessage(anthropic.NewTextBlock(c))
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshalTurns: %v", err)
		}
		out = append(out, raw)
	}
	return out
}

func TestFetchPriorTurns_EmptyConversationID_ReturnsNil(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	turns, tokens := f.fetchPriorTurns("", "trace-1")
	if turns != nil || tokens != 0 {
		t.Errorf("empty conversationID: want nil, 0; got %v, %d", turns, tokens)
	}
}

func TestFetchPriorTurns_NoRecords_ReturnsNil(t *testing.T) {
	f := newTestFactory(t, nil, nil)
	turns, tokens := f.fetchPriorTurns("conv-unknown", "trace-1")
	if turns != nil || tokens != 0 {
		t.Errorf("no records: want nil, 0; got %v, %d", turns, tokens)
	}
}

func TestFetchPriorTurns_ValidSnapshot_ReturnsTurns(t *testing.T) {
	mem := memory.New()
	snap := types.ConversationSnapshot{
		ConversationID: "conv-1",
		Turns:          marshalTurns(t, []string{"hello", "world"}),
		TotalTokens:    400,
		TaskID:         "task-1",
		WrittenAt:      time.Now(),
	}
	writeConversationSnapshot(t, mem, snap)
	f := newTestFactory(t, mem, nil)

	turns, tokens := f.fetchPriorTurns("conv-1", "trace-1")
	if len(turns) != 2 {
		t.Errorf("want 2 turns, got %d", len(turns))
	}
	if tokens != 400 {
		t.Errorf("want 400 tokens, got %d", tokens)
	}
}

func TestFetchPriorTurns_MultipleSnapshots_ReturnsMostRecent(t *testing.T) {
	mem := memory.New()

	older := types.ConversationSnapshot{
		ConversationID: "conv-2",
		Turns:          marshalTurns(t, []string{"old-turn"}),
		TotalTokens:    100,
		TaskID:         "task-old",
		WrittenAt:      time.Now().Add(-10 * time.Minute),
	}
	newer := types.ConversationSnapshot{
		ConversationID: "conv-2",
		Turns:          marshalTurns(t, []string{"new-turn-a", "new-turn-b"}),
		TotalTokens:    200,
		TaskID:         "task-new",
		WrittenAt:      time.Now(),
	}
	// Seed in reverse order to confirm sorting by WrittenAt, not insertion order.
	writeConversationSnapshot(t, mem, newer)
	writeConversationSnapshot(t, mem, older)
	f := newTestFactory(t, mem, nil)

	turns, tokens := f.fetchPriorTurns("conv-2", "trace-1")
	if len(turns) != 2 {
		t.Errorf("want 2 turns from newer snapshot, got %d", len(turns))
	}
	if tokens != 200 {
		t.Errorf("want 200 tokens from newer snapshot, got %d", tokens)
	}
}

func TestFetchPriorTurns_TokenGuard_DropsOldestTurns(t *testing.T) {
	mem := memory.New()
	// 6 turns, token count set above the 80% budget so the guard must drop some.
	budget := int64(float64(types.ModelContextWindow) * types.CompactThreshold)
	snap := types.ConversationSnapshot{
		ConversationID: "conv-3",
		Turns:          marshalTurns(t, []string{"t1", "t2", "t3", "t4", "t5", "t6"}),
		TotalTokens:    budget + 10_000, // deliberately over-budget
		TaskID:         "task-1",
		WrittenAt:      time.Now(),
	}
	writeConversationSnapshot(t, mem, snap)
	f := newTestFactory(t, mem, nil)

	turns, _ := f.fetchPriorTurns("conv-3", "trace-1")
	if len(turns) >= 6 {
		t.Errorf("token guard must drop oldest turns; want < 6, got %d", len(turns))
	}
	if len(turns) == 0 {
		t.Error("token guard must keep at least one turn")
	}
}

func TestFetchPriorTurns_ConversationIsolation(t *testing.T) {
	mem := memory.New()
	writeConversationSnapshot(t, mem, types.ConversationSnapshot{
		ConversationID: "conv-a",
		Turns:          marshalTurns(t, []string{"conv-a-turn"}),
		TotalTokens:    100,
		WrittenAt:      time.Now(),
	})
	writeConversationSnapshot(t, mem, types.ConversationSnapshot{
		ConversationID: "conv-b",
		Turns:          marshalTurns(t, []string{"conv-b-turn-1", "conv-b-turn-2"}),
		TotalTokens:    200,
		WrittenAt:      time.Now(),
	})
	f := newTestFactory(t, mem, nil)

	turnsA, _ := f.fetchPriorTurns("conv-a", "trace-1")
	if len(turnsA) != 1 {
		t.Errorf("conv-a: want 1 turn, got %d", len(turnsA))
	}
	turnsB, _ := f.fetchPriorTurns("conv-b", "trace-1")
	if len(turnsB) != 2 {
		t.Errorf("conv-b: want 2 turns, got %d", len(turnsB))
	}
}

// errMemoryClient is a stub that returns an error from ReadAllByType.
type errMemoryClient struct{}

func (e *errMemoryClient) Write(_ *types.MemoryWrite) error              { return nil }
func (e *errMemoryClient) Read(_, _ string) ([]types.MemoryWrite, error) { return nil, nil }
func (e *errMemoryClient) ReadAllByType(_ string) ([]types.MemoryWrite, error) {
	return nil, errReadFailed
}

var errReadFailed = &readError{}

type readError struct{}

func (r *readError) Error() string { return "simulated memory read error" }
