package main

import "testing"

// TestContextWindowAction_BelowCompactThreshold covers the normal operating
// range — no token budget action needed.
func TestContextWindowAction_BelowCompactThreshold(t *testing.T) {
	// 79% of 200,000 = 158,000 tokens — below both thresholds.
	if got := contextWindowAction(158_000); got != contextActionNone {
		t.Errorf("158,000 tokens (79%%): want contextActionNone, got %v", got)
	}
}

// TestContextWindowAction_AtCompactThreshold verifies the 80% boundary is
// inclusive: exactly 160,000 tokens must trigger compaction pending.
func TestContextWindowAction_AtCompactThreshold(t *testing.T) {
	// 80% of 200,000 = 160,000 tokens exactly.
	if got := contextWindowAction(160_000); got != contextActionCompactPending {
		t.Errorf("160,000 tokens (80%%): want contextActionCompactPending, got %v", got)
	}
}

// TestContextWindowAction_AboveCompactBelowHardAbort covers the range between
// the two thresholds where compaction should be pending but no hard abort.
func TestContextWindowAction_AboveCompactBelowHardAbort(t *testing.T) {
	// 87% of 200,000 = 174,000 tokens — above compact threshold, below hard abort.
	if got := contextWindowAction(174_000); got != contextActionCompactPending {
		t.Errorf("174,000 tokens (87%%): want contextActionCompactPending, got %v", got)
	}
}

// TestContextWindowAction_AtHardAbortThreshold verifies the 95% boundary is
// inclusive: exactly 190,000 tokens must trigger a hard abort.
func TestContextWindowAction_AtHardAbortThreshold(t *testing.T) {
	// 95% of 200,000 = 190,000 tokens exactly.
	if got := contextWindowAction(190_000); got != contextActionHardAbort {
		t.Errorf("190,000 tokens (95%%): want contextActionHardAbort, got %v", got)
	}
}

// TestContextWindowAction_AboveHardAbortThreshold covers usage above 95%
// (e.g. a very large turn that consumed almost all available context).
func TestContextWindowAction_AboveHardAbortThreshold(t *testing.T) {
	// 200,000 tokens = 100% of context window.
	if got := contextWindowAction(200_000); got != contextActionHardAbort {
		t.Errorf("200,000 tokens (100%%): want contextActionHardAbort, got %v", got)
	}
}

// TestContextWindowAction_ZeroTokens verifies that 0 tokens (e.g. a
// just-started agent) results in no action.
func TestContextWindowAction_ZeroTokens(t *testing.T) {
	if got := contextWindowAction(0); got != contextActionNone {
		t.Errorf("0 tokens: want contextActionNone, got %v", got)
	}
}

// TestContextWindowAction_JustBelowHardAbort verifies a token count just
// below 95% still falls in the compact-pending band, not hard abort.
func TestContextWindowAction_JustBelowHardAbort(t *testing.T) {
	// 189,999 tokens < 190,000 (95%) → compact pending, not abort.
	if got := contextWindowAction(189_999); got != contextActionCompactPending {
		t.Errorf("189,999 tokens (<95%%): want contextActionCompactPending, got %v", got)
	}
}

// TestContextWindowAction_JustBelowCompact verifies a token count just below
// 80% produces no action.
func TestContextWindowAction_JustBelowCompact(t *testing.T) {
	// 159,999 tokens < 160,000 (80%) → no action.
	if got := contextWindowAction(159_999); got != contextActionNone {
		t.Errorf("159,999 tokens (<80%%): want contextActionNone, got %v", got)
	}
}
