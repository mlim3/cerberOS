package credentials_test

import (
	"testing"

	"github.com/aegis/aegis-agents/internal/credentials"
)

func TestPreAuthorizeAndGetCredential(t *testing.T) {
	b := credentials.New(map[string]string{
		"db.password": "s3cr3t",
	})

	token, err := b.PreAuthorize("agent-1", []string{"db.password"})
	if err != nil {
		t.Fatalf("PreAuthorize: %v", err)
	}
	if token == "" {
		t.Error("expected non-empty vault token")
	}

	val, err := b.GetCredential("agent-1", "db.password")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if val != "s3cr3t" {
		t.Errorf("got %q, want %q", val, "s3cr3t")
	}
}

func TestGetCredentialUnauthorizedKey(t *testing.T) {
	b := credentials.New(map[string]string{"db.password": "s3cr3t"})
	b.PreAuthorize("agent-1", []string{"db.password"})

	if _, err := b.GetCredential("agent-1", "api.key"); err == nil {
		t.Error("expected error for key not in permission set, got nil")
	}
}

func TestGetCredentialBeforePreAuthorize(t *testing.T) {
	b := credentials.New(nil)
	if _, err := b.GetCredential("ghost", "any.key"); err == nil {
		t.Error("expected error for unregistered agent, got nil")
	}
}

func TestRevoke(t *testing.T) {
	b := credentials.New(map[string]string{"k": "v"})
	b.PreAuthorize("agent-1", []string{"k"})

	if err := b.Revoke("agent-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := b.GetCredential("agent-1", "k"); err == nil {
		t.Error("expected error after revocation, got nil")
	}
}
