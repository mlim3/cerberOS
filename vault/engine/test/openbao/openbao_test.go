//go:build openbao

package openbao_test

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

func TestMain(m *testing.M) {
	if os.Getenv("BAO_ADDR") == "" || os.Getenv("BAO_TOKEN") == "" {
		// Skip all tests if OpenBao is not configured.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func newManager(t *testing.T) secretmanager.SecretManager {
	t.Helper()
	auditor := audit.New(audit.NewJSONExporter(io.Discard))
	m := secretmanager.NewOpenBaoSecretManager(auditor)
	if m == nil {
		t.Fatal("NewOpenBaoSecretManager returned nil — check BAO_ADDR and BAO_TOKEN")
	}
	return m
}

func cleanup(t *testing.T, m secretmanager.SecretManager, keys ...string) {
	t.Helper()
	for _, k := range keys {
		m.DeleteSecret(context.Background(), k)
	}
}

func TestOpenBao_CRUDLifecycle(t *testing.T) {
	m := newManager(t)
	key := "test-crud-lifecycle"
	t.Cleanup(func() { cleanup(t, m, key) })

	// Put
	if err := m.PutSecret(context.Background(), key, "lifecycle-val"); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	// Get
	secrets, err := m.GetSecrets([]string{key})
	if err != nil {
		t.Fatalf("GetSecrets: %v", err)
	}
	if secrets[key] != "lifecycle-val" {
		t.Fatalf("got %q, want lifecycle-val", secrets[key])
	}

	// Delete
	if err := m.DeleteSecret(context.Background(), key); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}

	// Verify gone
	_, err = m.GetSecrets([]string{key})
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestOpenBao_GetMultipleKeys(t *testing.T) {
	m := newManager(t)
	keyA, keyB := "test-multi-a", "test-multi-b"
	t.Cleanup(func() { cleanup(t, m, keyA, keyB) })

	if err := m.PutSecret(context.Background(), keyA, "val-a"); err != nil {
		t.Fatal(err)
	}
	if err := m.PutSecret(context.Background(), keyB, "val-b"); err != nil {
		t.Fatal(err)
	}

	secrets, err := m.GetSecrets([]string{keyA, keyB})
	if err != nil {
		t.Fatalf("GetSecrets: %v", err)
	}
	if secrets[keyA] != "val-a" || secrets[keyB] != "val-b" {
		t.Fatalf("secrets = %v", secrets)
	}
}

func TestOpenBao_AtomicGetFailure(t *testing.T) {
	m := newManager(t)
	key := "test-atomic-exists"
	t.Cleanup(func() { cleanup(t, m, key) })

	if err := m.PutSecret(context.Background(), key, "val"); err != nil {
		t.Fatal(err)
	}

	_, err := m.GetSecrets([]string{key, "test-atomic-does-not-exist"})
	if err == nil {
		t.Fatal("expected error for mixed existing/nonexistent keys")
	}
}

func TestOpenBao_PutOverwrite(t *testing.T) {
	m := newManager(t)
	key := "test-overwrite"
	t.Cleanup(func() { cleanup(t, m, key) })

	if err := m.PutSecret(context.Background(), key, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := m.PutSecret(context.Background(), key, "v2"); err != nil {
		t.Fatal(err)
	}

	secrets, err := m.GetSecrets([]string{key})
	if err != nil {
		t.Fatal(err)
	}
	if secrets[key] != "v2" {
		t.Fatalf("got %q, want v2", secrets[key])
	}
}

func TestOpenBao_DeleteNonexistent(t *testing.T) {
	m := newManager(t)
	// Deleting a key that was never created — document whether this errors or is a no-op.
	err := m.DeleteSecret(context.Background(), "test-never-existed-key")
	// OpenBao KV v2 delete on nonexistent key may or may not error.
	// This test documents the actual behavior.
	t.Logf("DeleteSecret on nonexistent key: err=%v", err)
}

func TestOpenBao_SpecialCharValues(t *testing.T) {
	m := newManager(t)
	key := "test-special-chars"
	t.Cleanup(func() { cleanup(t, m, key) })

	value := `p@$$w0rd!#&"quotes"<xml>` + "\nnewline\ttab"
	if err := m.PutSecret(context.Background(), key, value); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	secrets, err := m.GetSecrets([]string{key})
	if err != nil {
		t.Fatalf("GetSecrets: %v", err)
	}
	if secrets[key] != value {
		t.Fatalf("got %q, want %q", secrets[key], value)
	}
}
