package main

import "testing"

func TestSessionLog_NilReceiver_ClaimAction(t *testing.T) {
	var sl *SessionLog
	result, err := sl.ClaimAction(ClaimActionParams{
		Key:        "demo-action",
		TTLSeconds: 60,
	})
	if err != nil {
		t.Fatalf("nil receiver ClaimAction: want nil error, got %v", err)
	}
	if result.Claimed {
		t.Fatalf("nil receiver ClaimAction: claimed = true, want false")
	}
}

func TestSessionLog_NilReceiver_CompleteAction(t *testing.T) {
	var sl *SessionLog
	if err := sl.CompleteAction(CompleteActionParams{Key: "demo-action"}); err != nil {
		t.Fatalf("nil receiver CompleteAction: want nil error, got %v", err)
	}
}
