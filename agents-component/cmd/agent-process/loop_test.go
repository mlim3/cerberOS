package main

import (
	"strings"
	"testing"
)

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

// ---------------------------------------------------------------------------
// buildSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_General_IgnoresManifest(t *testing.T) {
	// "general" domain returns a fixed prompt regardless of manifest content.
	got := buildSystemPrompt("general", "- some_tool: does something\n", "", "")
	if strings.Contains(got, "Available commands") {
		t.Error("general domain prompt must not include command manifest")
	}
	if !strings.Contains(got, "general-purpose") {
		t.Error("general domain prompt must mention general-purpose reasoning")
	}
}

func TestBuildSystemPrompt_Domain_NoManifest(t *testing.T) {
	// An empty manifest should produce the base prompt with no manifest section.
	got := buildSystemPrompt("web", "", "", "")
	if strings.Contains(got, "Available commands") {
		t.Errorf("empty manifest: prompt must not include 'Available commands' section; got %q", got)
	}
	if !strings.Contains(got, `"web"`) {
		t.Errorf("prompt must mention the domain name; got %q", got)
	}
}

func TestBuildSystemPrompt_Domain_WithManifest(t *testing.T) {
	manifest := "- web_fetch: Fetches a webpage by URL.\n- web_parse: Parses HTML into text.\n"
	got := buildSystemPrompt("web", manifest, "", "")
	if !strings.Contains(got, "Available commands:") {
		t.Errorf("prompt with manifest must include 'Available commands:' header; got %q", got)
	}
	if !strings.Contains(got, "web_fetch") {
		t.Errorf("prompt must include command name from manifest; got %q", got)
	}
	if !strings.Contains(got, "web_parse") {
		t.Errorf("prompt must include second command from manifest; got %q", got)
	}
}

func TestBuildSystemPrompt_ManifestAppendsAfterBase(t *testing.T) {
	manifest := "- web_fetch: Fetches a webpage.\n"
	got := buildSystemPrompt("web", manifest, "", "")
	// Manifest must come after the base instructional text, not before.
	baseIdx := strings.Index(got, "task_complete")
	manifestIdx := strings.Index(got, "Available commands:")
	if baseIdx < 0 || manifestIdx < 0 {
		t.Fatalf("expected both base prompt and manifest in output; got %q", got)
	}
	if manifestIdx < baseIdx {
		t.Errorf("manifest section must appear after base prompt text; baseIdx=%d manifestIdx=%d", baseIdx, manifestIdx)
	}
}

// ---------------------------------------------------------------------------
// buildSystemPrompt — agentMemory and userProfile injection (capability 2)
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_WithAgentMemory_InjectsSection(t *testing.T) {
	got := buildSystemPrompt("web", "", "The target API returns ISO8601 timestamps.", "")
	if !strings.Contains(got, "## Knowledge from past tasks") {
		t.Error("agentMemory: expected '## Knowledge from past tasks' section header")
	}
	if !strings.Contains(got, "ISO8601") {
		t.Error("agentMemory: expected fact text in prompt")
	}
}

func TestBuildSystemPrompt_WithUserProfile_InjectsSection(t *testing.T) {
	got := buildSystemPrompt("web", "", "", "User prefers concise bullet points.")
	if !strings.Contains(got, "## User context") {
		t.Error("userProfile: expected '## User context' section header")
	}
	if !strings.Contains(got, "bullet points") {
		t.Error("userProfile: expected profile text in prompt")
	}
}

func TestBuildSystemPrompt_WithBoth_BothSectionsPresent(t *testing.T) {
	got := buildSystemPrompt("web", "", "API fact.", "User pref.")
	if !strings.Contains(got, "## Knowledge from past tasks") {
		t.Error("both: expected knowledge section")
	}
	if !strings.Contains(got, "## User context") {
		t.Error("both: expected user context section")
	}
}

func TestBuildSystemPrompt_EmptyMemory_NoSectionsAdded(t *testing.T) {
	got := buildSystemPrompt("web", "- cmd: desc.", "", "")
	if strings.Contains(got, "## Knowledge") {
		t.Error("empty agentMemory must not produce knowledge section")
	}
	if strings.Contains(got, "## User context") {
		t.Error("empty userProfile must not produce user context section")
	}
}

func TestBuildSystemPrompt_MemorySectionsAfterManifest(t *testing.T) {
	manifest := "- web_fetch: Fetches.\n"
	got := buildSystemPrompt("web", manifest, "fact", "pref")
	manifestIdx := strings.Index(got, "Available commands:")
	memIdx := strings.Index(got, "## Knowledge from past tasks")
	if manifestIdx < 0 || memIdx < 0 {
		t.Fatalf("expected manifest and memory section in output; got %q", got)
	}
	if memIdx < manifestIdx {
		t.Error("memory section must appear after the command manifest")
	}
}

func TestBuildSystemPrompt_General_WithMemory_SectionsAppended(t *testing.T) {
	// General domain still gets memory sections even though it has no manifest.
	got := buildSystemPrompt("general", "", "stored fact", "")
	if !strings.Contains(got, "general-purpose") {
		t.Error("general domain base text must be present")
	}
	if !strings.Contains(got, "## Knowledge from past tasks") {
		t.Error("general domain with agentMemory must still include knowledge section")
	}
}
