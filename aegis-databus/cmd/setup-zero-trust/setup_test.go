package main

import (
	"testing"

	"aegis-databus/pkg/security"
)

func TestPlaceholderConstant(t *testing.T) {
	if placeholder != "__AEGIS_NKEY_PUBLIC__" {
		t.Errorf("placeholder: want __AEGIS_NKEY_PUBLIC__, got %q", placeholder)
	}
}

func TestGenerateUserNKey(t *testing.T) {
	pub, seed, err := security.GenerateUserNKey()
	if err != nil {
		t.Fatalf("GenerateUserNKey: %v", err)
	}
	if pub == "" || len(seed) == 0 {
		t.Error("GenerateUserNKey: pub and seed must be non-empty")
	}
	if len(pub) < 32 {
		t.Errorf("pub too short: %d", len(pub))
	}
}
