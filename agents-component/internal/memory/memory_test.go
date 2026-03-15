package memory_test

import (
	"testing"

	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/pkg/types"
)

func TestWriteAndRead(t *testing.T) {
	c := memory.New()

	w := &types.MemoryWrite{
		AgentID:   "a1",
		SessionID: "s1",
		DataType:  "task_result",
		TTLHint:   3600,
		Payload:   "output data",
		Tags:      map[string]string{"context": "result"},
	}

	if err := c.Write(w); err != nil {
		t.Fatalf("Write: %v", err)
	}

	records, err := c.Read("a1", "result")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].DataType != "task_result" {
		t.Errorf("unexpected DataType: %s", records[0].DataType)
	}
}

func TestReadFiltersByContextTag(t *testing.T) {
	c := memory.New()

	c.Write(&types.MemoryWrite{
		AgentID: "a1", SessionID: "s1", DataType: "t",
		Tags: map[string]string{"context": "alpha"},
	})
	c.Write(&types.MemoryWrite{
		AgentID: "a1", SessionID: "s1", DataType: "t",
		Tags: map[string]string{"context": "beta"},
	})

	records, _ := c.Read("a1", "alpha")
	if len(records) != 1 {
		t.Errorf("expected 1 filtered record, got %d", len(records))
	}
}

func TestWriteMissingFields(t *testing.T) {
	c := memory.New()

	tests := []struct {
		name    string
		payload *types.MemoryWrite
	}{
		{"nil payload", nil},
		{"missing AgentID", &types.MemoryWrite{SessionID: "s", DataType: "t"}},
		{"missing SessionID", &types.MemoryWrite{AgentID: "a", DataType: "t"}},
		{"missing DataType", &types.MemoryWrite{AgentID: "a", SessionID: "s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := c.Write(tt.payload); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}
