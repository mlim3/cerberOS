package main

import (
	"encoding/json"
	"testing"
)

func TestExtractCorrelationID(t *testing.T) {
	empty := extractCorrelationID([]byte(`{}`))
	if empty != "" {
		t.Errorf("extractCorrelationID({}): want \"\", got %q", empty)
	}
	valid := extractCorrelationID([]byte(`{"correlationid":"x-y-z"}`))
	if valid != "x-y-z" {
		t.Errorf("extractCorrelationID: want \"x-y-z\", got %q", valid)
	}
	invalid := extractCorrelationID([]byte(`not json`))
	if invalid != "" {
		t.Errorf("extractCorrelationID invalid: want \"\", got %q", invalid)
	}
}

func TestNewUUID(t *testing.T) {
	id1 := newUUID()
	id2 := newUUID()
	if id1 == id2 {
		t.Error("newUUID: expected unique IDs")
	}
	if len(id1) != 36 {
		t.Errorf("newUUID: expected len 36, got %d", len(id1))
	}
	// Basic UUID format check
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(`{"id":"`+id1+`"}`), &m); err != nil {
		t.Errorf("newUUID produced invalid format: %v", err)
	}
}
