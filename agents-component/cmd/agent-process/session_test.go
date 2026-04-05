package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ---- nil-safe SessionLog tests ----

func TestSessionLog_NilReceiver_WriteReturnsEmpty(t *testing.T) {
	var sl *SessionLog
	id := sl.Write(turnTypeUserMessage, "content", "", "")
	if id != "" {
		t.Errorf("nil SessionLog.Write: want \"\", got %q", id)
	}
}

func TestSessionLog_NilReceiver_ReadSessionReturnsNil(t *testing.T) {
	var sl *SessionLog
	entries := sl.ReadSession("trace-1")
	if entries != nil {
		t.Errorf("nil SessionLog.ReadSession: want nil, got %v", entries)
	}
}

// ---- context helper tests ----

func TestWithSessionLog_RoundTrip(t *testing.T) {
	sl := &SessionLog{log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	ctx := WithSessionLog(context.Background(), sl)
	got := SessionLogFromCtx(ctx)
	if got != sl {
		t.Error("SessionLogFromCtx: did not return the stored SessionLog")
	}
}

func TestSessionLogFromCtx_Missing(t *testing.T) {
	got := SessionLogFromCtx(context.Background())
	if got != nil {
		t.Errorf("SessionLogFromCtx on empty context: want nil, got %v", got)
	}
}

func TestWithParentEntryID_RoundTrip(t *testing.T) {
	const want = "parent-uuid-123"
	ctx := WithParentEntryID(context.Background(), want)
	got := ParentEntryIDFromCtx(ctx)
	if got != want {
		t.Errorf("ParentEntryIDFromCtx: want %q, got %q", want, got)
	}
}

func TestParentEntryIDFromCtx_Missing(t *testing.T) {
	got := ParentEntryIDFromCtx(context.Background())
	if got != "" {
		t.Errorf("ParentEntryIDFromCtx on empty context: want \"\", got %q", got)
	}
}

// ---- extractSessionEntries tests ----

func makeMemoryWrite(entry types.SessionEntry) types.MemoryWrite {
	return types.MemoryWrite{
		AgentID:   "agent-1",
		SessionID: "task-1",
		DataType:  "episode",
		Payload:   entry,
	}
}

func TestExtractSessionEntries_AllValid(t *testing.T) {
	entries := []types.SessionEntry{
		{EntryID: "e1", TurnType: turnTypeUserMessage, Content: "hello"},
		{EntryID: "e2", TurnType: turnTypeAssistantResponse, Content: "world", ParentEntryID: "e1"},
	}
	records := []types.MemoryWrite{makeMemoryWrite(entries[0]), makeMemoryWrite(entries[1])}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	got := extractSessionEntries(records, log)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	if got[0].EntryID != "e1" {
		t.Errorf("entry[0].EntryID: want %q, got %q", "e1", got[0].EntryID)
	}
	if got[1].ParentEntryID != "e1" {
		t.Errorf("entry[1].ParentEntryID: want %q, got %q", "e1", got[1].ParentEntryID)
	}
}

func TestExtractSessionEntries_EmptyRecords(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	got := extractSessionEntries(nil, log)
	if len(got) != 0 {
		t.Errorf("empty records: want 0 entries, got %d", len(got))
	}
}

func TestExtractSessionEntries_InvalidPayloadSkipped(t *testing.T) {
	// One record with an invalid payload (not a SessionEntry), one valid.
	bad := types.MemoryWrite{
		AgentID:   "agent-1",
		SessionID: "task-1",
		DataType:  "episode",
		Payload:   json.RawMessage(`{invalid-json`), // cannot be re-marshaled as SessionEntry
	}
	good := makeMemoryWrite(types.SessionEntry{EntryID: "e1", TurnType: turnTypeToolCall})
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	got := extractSessionEntries([]types.MemoryWrite{bad, good}, log)
	// bad payload: marshal succeeds but unmarshal as SessionEntry may produce empty struct
	// the valid record must appear
	found := false
	for _, e := range got {
		if e.EntryID == "e1" {
			found = true
		}
	}
	if !found {
		t.Error("extractSessionEntries: valid entry missing after skipping bad record")
	}
}

func TestExtractSessionEntries_VaultRequestIDPreserved(t *testing.T) {
	entry := types.SessionEntry{
		EntryID:        "e1",
		TurnType:       turnTypeToolCall,
		Content:        "vault dispatch",
		VaultRequestID: "req-abc-123",
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	got := extractSessionEntries([]types.MemoryWrite{makeMemoryWrite(entry)}, log)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if got[0].VaultRequestID != "req-abc-123" {
		t.Errorf("VaultRequestID: want %q, got %q", "req-abc-123", got[0].VaultRequestID)
	}
}

// ---- turn type constant tests ----

func TestTurnTypeConstants_Distinct(t *testing.T) {
	types := []string{
		turnTypeUserMessage,
		turnTypeAssistantResponse,
		turnTypeToolCall,
		turnTypeToolResult,
		turnTypeCompaction,
	}
	seen := make(map[string]bool, len(types))
	for _, tt := range types {
		if seen[tt] {
			t.Errorf("duplicate turn type constant: %q", tt)
		}
		seen[tt] = true
		if tt == "" {
			t.Error("empty turn type constant")
		}
	}
}
