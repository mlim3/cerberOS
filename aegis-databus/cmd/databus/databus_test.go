package main

import (
	"context"
	"testing"

	"aegis-databus/pkg/memory"
)

func TestSeedAuditDemo(t *testing.T) {
	ctx := context.Background()
	m := memory.NewMockMemoryClient()
	seedAuditDemo(ctx, m)
	entries, err := m.FetchPendingOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("FetchPendingOutbox: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 audit demo entries, got %d", len(entries))
	}
	subjects := make(map[string]bool)
	for _, e := range entries {
		subjects[e.Subject] = true
	}
	if !subjects["aegis.tasks.audit_seed"] || !subjects["aegis.memory.audit_seed"] {
		t.Errorf("expected both audit_seed subjects, got %v", subjects)
	}
}
