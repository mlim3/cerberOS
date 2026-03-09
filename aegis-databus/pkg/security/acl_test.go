package security

import (
	"context"
	"os"
	"testing"
)

func TestCheckSubscribe_DLQ_AdminOnly(t *testing.T) {
	// SR-DB-006: Only admin components may subscribe to aegis.dlq
	tests := []struct {
		component string
		subject   string
		wantErr   bool
	}{
		{"aegis-databus", "aegis.dlq", false},
		{"databus", "aegis.dlq.failed", false},
		{"admin", "aegis.dlq", false},
		{"dlq-admin", "aegis.dlq.anything", false},
		{"aegis-demo", "aegis.dlq", true},
		{"task-router", "aegis.dlq", true},
		{"agent", "aegis.dlq", true},
	}
	for _, tt := range tests {
		err := CheckSubscribe(tt.component, tt.subject)
		gotErr := err != nil
		if gotErr != tt.wantErr {
			t.Errorf("CheckSubscribe(%q, %q) err=%v wantErr=%v", tt.component, tt.subject, err, tt.wantErr)
		}
	}
}

func TestGetNKeyFromOpenBao(t *testing.T) {
	ctx := context.Background()
	// Without env or OpenBao: returns error
	os.Unsetenv("AEGIS_NKEY_SEED")
	os.Unsetenv("OPENBAO_ADDR")
	_, err := GetNKeyFromOpenBao(ctx, "databus")
	if err != ErrOpenBaoNotConfigured {
		t.Errorf("want ErrOpenBaoNotConfigured, got %v", err)
	}
	// With AEGIS_NKEY_SEED: returns seed (demo fallback)
	os.Setenv("AEGIS_NKEY_SEED", "SUtest123")
	defer os.Unsetenv("AEGIS_NKEY_SEED")
	seed, err := GetNKeyFromOpenBao(ctx, "databus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seed != "SUtest123" {
		t.Errorf("want SUtest123, got %q", seed)
	}
}
