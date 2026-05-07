package validation

import (
	"testing"
)

func TestNoOpValidator_ValidatePayload(t *testing.T) {
	if err := NoOp.ValidatePayload([]byte("invalid")); err != nil {
		t.Errorf("NoOp.ValidatePayload: want nil, got %v", err)
	}
}

func TestNoOpValidator_ValidatePublish(t *testing.T) {
	if err := NoOp.ValidatePublish("any", "any.subject", []byte("x")); err != nil {
		t.Errorf("NoOp.ValidatePublish: want nil, got %v", err)
	}
}

func TestNoOpValidator_ValidateSubscribe(t *testing.T) {
	if err := NoOp.ValidateSubscribe("any", "any.subject"); err != nil {
		t.Errorf("NoOp.ValidateSubscribe: want nil, got %v", err)
	}
}

func TestStrictValidator_ValidatePayload(t *testing.T) {
	// Invalid payload
	if err := Strict.ValidatePayload([]byte("{}")); err == nil {
		t.Error("Strict.ValidatePayload invalid: want error, got nil")
	}
	// Valid CloudEvents
	valid := []byte(`{"specversion":"1.0","id":"1","source":"a","type":"t","data":{}}`)
	if err := Strict.ValidatePayload(valid); err != nil {
		t.Errorf("Strict.ValidatePayload: want nil, got %v", err)
	}
}
