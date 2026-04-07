package main

import (
	"testing"
)

func TestExtractCorr(t *testing.T) {
	empty := extractCorr([]byte(`{}`))
	if empty != "" {
		t.Errorf("extractCorr({}): want \"\", got %q", empty)
	}
	valid := extractCorr([]byte(`{"correlationid":"abc-123"}`))
	if valid != "abc-123" {
		t.Errorf("extractCorr: want \"abc-123\", got %q", valid)
	}
	invalid := extractCorr([]byte(`{invalid`))
	if invalid != "" {
		t.Errorf("extractCorr invalid JSON: want \"\", got %q", invalid)
	}
}
