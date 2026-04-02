package security

import (
	"context"
	"os"
	"testing"
)

func TestCheckSubscribe_M3_AgentOnlySubjects(t *testing.T) {
	tests := []struct {
		component string
		wantErr   bool
	}{
		{"agent", false},
		{"aegis-agent", false},
		{"monitoring", true},
		{"orchestrator", true},
		{"aegis-demo", true},
	}
	for _, subj := range []string{subjectAgentsVaultExecuteResult, subjectAgentsCredentialResponse} {
		for _, tt := range tests {
			err := CheckSubscribe(tt.component, subj)
			gotErr := err != nil
			if gotErr != tt.wantErr {
				t.Errorf("CheckSubscribe(%q, %q) err=%v wantErr=%v", tt.component, subj, err, tt.wantErr)
			}
		}
	}
}

func TestCheckSubscribe_AgentsRecursiveWildcardDenied(t *testing.T) {
	for _, c := range []string{"monitoring", "agent", "aegis-demo", "orchestrator"} {
		if err := CheckSubscribe(c, subjectAgentsRecursiveWildcard); err == nil {
			t.Errorf("CheckSubscribe(%q, aegis.agents.>) want deny", c)
		}
	}
}

func TestCheckSubscribe_AgentsLeafWildcard(t *testing.T) {
	if err := CheckSubscribe("monitoring", subjectAgentsLeafWildcard); err != nil {
		t.Errorf("monitoring aegis.agents.*: %v", err)
	}
	if err := CheckSubscribe("orchestrator", subjectAgentsLeafWildcard); err == nil {
		t.Errorf("orchestrator must not subscribe to aegis.agents.*")
	}
}

func TestCheckQueueSubscribe_M3_QueueName(t *testing.T) {
	if err := CheckQueueSubscribe("agent", subjectAgentsVaultExecuteResult, QueueAgentsComponent); err != nil {
		t.Fatalf("valid queue: %v", err)
	}
	if err := CheckQueueSubscribe("agent", subjectAgentsVaultExecuteResult, "wrong"); err == nil {
		t.Fatal("wrong queue must fail")
	}
}

func TestCheckPublish_M3_SensitiveIngress(t *testing.T) {
	if err := CheckPublish("vault", subjectAgentsVaultExecuteResult); err != nil {
		t.Errorf("vault publish: %v", err)
	}
	if err := CheckPublish("agent", subjectAgentsVaultExecuteResult); err == nil {
		t.Error("agent must not publish sensitive ingress")
	}
}

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
