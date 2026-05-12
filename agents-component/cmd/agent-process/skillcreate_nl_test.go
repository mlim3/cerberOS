package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ─── sanitizeSkillName ────────────────────────────────────────────────────────

func TestSanitizeSkillName_Empty(t *testing.T) {
	if got := sanitizeSkillName(""); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestSanitizeSkillName_AlreadyValid(t *testing.T) {
	if got := sanitizeSkillName("fetch_page"); got != "fetch_page" {
		t.Errorf("want fetch_page, got %q", got)
	}
}

func TestSanitizeSkillName_UppercaseConverted(t *testing.T) {
	if got := sanitizeSkillName("FetchPage"); got != "fetchpage" {
		t.Errorf("want fetchpage, got %q", got)
	}
}

func TestSanitizeSkillName_HyphensToUnderscore(t *testing.T) {
	if got := sanitizeSkillName("fetch-page"); got != "fetch_page" {
		t.Errorf("want fetch_page, got %q", got)
	}
}

func TestSanitizeSkillName_LeadingDigitPrefixed(t *testing.T) {
	got := sanitizeSkillName("1_bad")
	if !strings.HasPrefix(got, "skill_") {
		t.Errorf("leading digit must be prefixed with skill_, got %q", got)
	}
}

func TestSanitizeSkillName_TruncatedAt64(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := sanitizeSkillName(long)
	if len(got) > 64 {
		t.Errorf("sanitized name must be ≤64 chars, got %d", len(got))
	}
}

// ─── ensureNegativeGuidance ───────────────────────────────────────────────────

func TestEnsureNegativeGuidance_AlreadyHas(t *testing.T) {
	in := "Does X. Do not use when Y."
	out := ensureNegativeGuidance(in)
	if out != in {
		t.Errorf("already-present guidance must not be duplicated; got %q", out)
	}
}

func TestEnsureNegativeGuidance_Missing(t *testing.T) {
	out := ensureNegativeGuidance("Does something useful")
	if !strings.Contains(strings.ToLower(out), "do not") {
		t.Errorf("missing guidance must be appended; got %q", out)
	}
}

// ─── validateGeneratedSkill ───────────────────────────────────────────────────

func goodNode() *types.SkillNode {
	now := time.Now().UTC()
	return &types.SkillNode{
		Name:          "fetch_summary",
		Level:         "command",
		Label:         "Fetch Summary",
		Description:   "Computes a rolling summary of available data. Do NOT use for real-time alerts.",
		Recipe:        "1. Gather data. 2. Compute summary. 3. Return result.",
		Spec:          &types.SkillSpec{Parameters: map[string]types.ParameterDef{}},
		Origin:        "synthesized",
		SynthesizedAt: &now,
	}
}

func TestValidateGeneratedSkill_Valid(t *testing.T) {
	if err := validateGeneratedSkill(goodNode()); err != nil {
		t.Errorf("valid node must pass: %v", err)
	}
}

func TestValidateGeneratedSkill_Nil(t *testing.T) {
	if err := validateGeneratedSkill(nil); err == nil {
		t.Error("nil node must return error")
	}
}

func TestValidateGeneratedSkill_EmptyRecipe(t *testing.T) {
	n := goodNode()
	n.Recipe = ""
	if err := validateGeneratedSkill(n); err == nil {
		t.Error("empty recipe must return error")
	}
}

func TestValidateGeneratedSkill_BadName(t *testing.T) {
	n := goodNode()
	// Only special chars → sanitizeSkillName returns "" → fails regex check.
	n.Name = "!!!"
	if err := validateGeneratedSkill(n); err == nil {
		t.Error("all-special-char name must return error after sanitization produces empty string")
	}
}

func TestValidateGeneratedSkill_CredentialInDescription(t *testing.T) {
	n := goodNode()
	n.Description = "Uses sk-abc1234567890 token. Do NOT use for other."
	if err := validateGeneratedSkill(n); err == nil {
		t.Error("credential-like description must return error")
	}
}

func TestValidateGeneratedSkill_PlaceholderMismatch(t *testing.T) {
	n := goodNode()
	n.Spec = &types.SkillSpec{Parameters: map[string]types.ParameterDef{
		"query": {Type: "string", Required: true, Description: "The search query"},
	}}
	n.Recipe = "1. Do something without the param."
	if err := validateGeneratedSkill(n); err == nil {
		t.Error("recipe missing parameter placeholder must return error")
	}
}

func TestValidateGeneratedSkill_PlaceholderPresent(t *testing.T) {
	n := goodNode()
	n.Spec = &types.SkillSpec{Parameters: map[string]types.ParameterDef{
		"query": {Type: "string", Required: true, Description: "The search query"},
	}}
	n.Recipe = "1. Search for {{query}} and return results."
	if err := validateGeneratedSkill(n); err != nil {
		t.Errorf("recipe with placeholder must pass: %v", err)
	}
}

// ─── fallbackSkillFromDescription ─────────────────────────────────────────────

func TestFallbackSkillFromDescription_ContractValid(t *testing.T) {
	node := fallbackSkillFromDescription("sends a weekly digest email to the team", "weekly_digest")
	if err := validateGeneratedSkill(node); err != nil {
		t.Errorf("fallback node must pass contract validation: %v", err)
	}
}

func TestFallbackSkillFromDescription_HasNegativeGuidance(t *testing.T) {
	node := fallbackSkillFromDescription("does something", "do_something")
	lower := strings.ToLower(node.Description)
	if !strings.Contains(lower, "do not") && !strings.Contains(lower, "not for") {
		t.Errorf("fallback must include negative guidance; description: %q", node.Description)
	}
}

func TestFallbackSkillFromDescription_OriginSynthesized(t *testing.T) {
	node := fallbackSkillFromDescription("does something", "do_something")
	if node.Origin != "synthesized" {
		t.Errorf("fallback origin must be synthesized, got %q", node.Origin)
	}
}

func TestFallbackSkillFromDescription_EmptyRequestedName_DeriveFromDesc(t *testing.T) {
	node := fallbackSkillFromDescription("fetch latest news articles", "")
	if node.Name == "" {
		t.Error("fallback must derive a non-empty name when requestedName is empty")
	}
}

// ─── classifySkillRisk ────────────────────────────────────────────────────────

func nodeFor(description, recipe string) *types.SkillNode {
	return &types.SkillNode{Name: "test_skill", Description: description, Recipe: recipe}
}

func TestClassifyRisk_LowRisk(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Fetches a URL and returns HTML", "1. Fetch URL. 2. Return."), "Fetches a URL and returns HTML", "user")
	if len(reasons) != 0 {
		t.Errorf("low-risk skill must have no risk reasons; got %v", reasons)
	}
}

func TestClassifyRisk_Email(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Sends an email to the team", "1. Send email."), "Sends an email to the team", "user")
	if len(reasons) == 0 {
		t.Error("email skill must be flagged as risky")
	}
}

func TestClassifyRisk_Gmail(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Uses Gmail to send report", "1. Gmail send."), "Gmail report", "user")
	if len(reasons) == 0 {
		t.Error("gmail skill must be flagged as risky")
	}
}

func TestClassifyRisk_Calendar(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Adds an event to calendar", "1. Create calendar event."), "Calendar invite", "user")
	if len(reasons) == 0 {
		t.Error("calendar skill must be flagged as risky")
	}
}

func TestClassifyRisk_Destructive_Delete(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Deletes old files", "1. Delete files."), "Delete old files", "user")
	if len(reasons) == 0 {
		t.Error("destructive (delete) skill must be flagged as risky")
	}
}

func TestClassifyRisk_Credentials(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Uses an API key", "1. Use api key."), "Needs api key", "user")
	if len(reasons) == 0 {
		t.Error("credential skill must be flagged as risky")
	}
}

func TestClassifyRisk_Recurring(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Runs every hour", "1. Run every hour."), "Runs every hour", "user")
	if len(reasons) == 0 {
		t.Error("recurring skill must be flagged as risky")
	}
}

func TestClassifyRisk_GlobalScope(t *testing.T) {
	reasons := classifySkillRisk(nodeFor("Does something basic", "1. Do it."), "Does something basic", "global")
	found := false
	for _, r := range reasons {
		if strings.Contains(r, "global") {
			found = true
		}
	}
	if !found {
		t.Errorf("global scope must be a risk reason; got %v", reasons)
	}
}

func TestClassifyRisk_RequiredCredentials(t *testing.T) {
	n := nodeFor("Read data", "1. Read.")
	n.RequiredCredentialTypes = []string{"oauth_token"}
	reasons := classifySkillRisk(n, "Read data", "user")
	if len(reasons) == 0 {
		t.Error("non-empty RequiredCredentialTypes must be flagged as risky")
	}
}

func TestClassifyRisk_NoDuplicateReasons(t *testing.T) {
	// "email" and "send" both appear — risk classifier must deduplicate.
	reasons := classifySkillRisk(nodeFor("Sends email using send tool", "1. Send email."), "Sends email", "user")
	seen := map[string]int{}
	for _, r := range reasons {
		seen[r]++
	}
	for r, n := range seen {
		if n > 1 {
			t.Errorf("risk reason %q appears %d times; must be deduplicated", r, n)
		}
	}
}

// ─── draftHash ────────────────────────────────────────────────────────────────

func TestDraftHash_Deterministic(t *testing.T) {
	n := goodNode()
	h1 := draftHash("web", n)
	h2 := draftHash("web", n)
	if h1 != h2 {
		t.Errorf("draft hash must be deterministic; got %q and %q", h1, h2)
	}
}

func TestDraftHash_DiffersOnDomain(t *testing.T) {
	n := goodNode()
	if draftHash("web", n) == draftHash("data", n) {
		t.Error("draft hash must differ when domain changes")
	}
}

func TestDraftHash_DiffersOnRecipe(t *testing.T) {
	n1 := goodNode()
	n2 := goodNode()
	n2.Recipe = "1. Different step."
	if draftHash("web", n1) == draftHash("web", n2) {
		t.Error("draft hash must differ when recipe changes")
	}
}

func TestDraftHash_DiffersOnOwner(t *testing.T) {
	n1 := goodNode()
	n2 := goodNode()
	n1.OwnerUserID = "alice"
	n2.OwnerUserID = "bob"
	if draftHash("web", n1) == draftHash("web", n2) {
		t.Error("draft hash must differ when owner changes")
	}
}

// ─── generateSkillFromNL — draft input ────────────────────────────────────────

func TestGenerateSkillFromNL_ValidDraft(t *testing.T) {
	n := goodNode()
	raw, _ := json.Marshal(n)
	gen, err := generateSkillFromNL(nil, nil, nil, "web", "irrelevant description", "", json.RawMessage(raw))
	if err != nil {
		t.Fatalf("valid draft must succeed: %v", err)
	}
	if gen.Mode != "draft" {
		t.Errorf("want mode=draft, got %q", gen.Mode)
	}
	if gen.Node.Name != n.Name {
		t.Errorf("draft name must be preserved; want %q, got %q", n.Name, gen.Node.Name)
	}
}

func TestGenerateSkillFromNL_InvalidDraft_ReturnsError(t *testing.T) {
	badDraft := json.RawMessage(`{"name":"!!!","recipe":"","level":"command"}`)
	_, err := generateSkillFromNL(nil, nil, nil, "web", "description", "", badDraft)
	if err == nil {
		t.Error("invalid draft must return error")
	}
}

func TestGenerateSkillFromNL_NilClientFallback(t *testing.T) {
	gen, err := generateSkillFromNL(nil, nil, nil, "web", "fetch a URL and return its HTML content", "fetch_url", nil)
	if err != nil {
		t.Fatalf("fallback must succeed with nil client: %v", err)
	}
	if gen.Mode != "fallback" {
		t.Errorf("nil client must produce fallback mode, got %q", gen.Mode)
	}
}

// ─── nlSkillCreateEnabled ─────────────────────────────────────────────────────

func TestNLSkillCreateEnabled_Default(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "")
	if nlSkillCreateEnabled() {
		t.Error("must be disabled when env var is empty")
	}
}

func TestNLSkillCreateEnabled_True(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on", "TRUE", "YES"} {
		t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", v)
		if !nlSkillCreateEnabled() {
			t.Errorf("must be enabled for value %q", v)
		}
	}
}

func TestNLSkillCreateEnabled_False(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", "FALSE"} {
		t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", v)
		if nlSkillCreateEnabled() {
			t.Errorf("must be disabled for value %q", v)
		}
	}
}

// ─── executeCreateSkillFromNL — feature-flag gate ─────────────────────────────

func TestExecuteCreateSkillFromNL_Disabled(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "false")
	raw, _ := json.Marshal(nlSkillCreateInput{Description: "does something"})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	if !res.IsError {
		t.Error("disabled flag must return an error ToolResult")
	}
	if !strings.Contains(res.Content, "AEGIS_NL_SKILL_CREATE_ENABLED") {
		t.Errorf("error must mention the env flag; got %q", res.Content)
	}
}

func TestExecuteCreateSkillFromNL_NilSessionLog_PersistFails(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	// Provide a safe low-risk draft so we reach the persistence step.
	n := goodNode()
	raw, _ := json.Marshal(nlSkillCreateInput{
		Description: "fetch a URL and return body",
		Draft:       mustMarshal(n),
	})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	// Low-risk skill reaches persist path and must fail without session log.
	if !res.IsError {
		t.Error("nil session log must return an error when attempting to persist")
	}
}

func TestExecuteCreateSkillFromNL_CredentialInDescription(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	raw, _ := json.Marshal(nlSkillCreateInput{Description: "Use sk-abc1234567890xyz token to call API"})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	if !res.IsError {
		t.Error("credential-like description must be rejected before generation")
	}
}

func TestExecuteCreateSkillFromNL_GlobalScopeRejected(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	raw, _ := json.Marshal(nlSkillCreateInput{Description: "does something safe", Scope: "global"})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	if !res.IsError {
		t.Error("global scope from chat must be rejected")
	}
}

func TestExecuteCreateSkillFromNL_InvalidScope(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	raw, _ := json.Marshal(nlSkillCreateInput{Description: "does something", Scope: "team"})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	if !res.IsError {
		t.Error("unknown scope must be rejected")
	}
}

func TestExecuteCreateSkillFromNL_RiskySkillReturnsPreview(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	// Use a draft so we bypass LLM but have a valid node with risky content.
	n := goodNode()
	n.Description = "Sends an email every Monday. Do NOT use for other comms."
	n.Recipe = "1. Send email to team. 2. Done."
	raw, _ := json.Marshal(nlSkillCreateInput{
		Description: "send weekly email every Monday",
		Draft:       mustMarshal(n),
	})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	// Should NOT be an error — it returns a draft preview.
	if res.IsError {
		t.Errorf("risky skill without confirm must return preview, not error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "draft_hash") {
		t.Errorf("preview must contain draft_hash; got %q", res.Content)
	}
	if !strings.Contains(res.Content, "confirmation_required") {
		t.Errorf("preview must contain confirmation_required; got %q", res.Content)
	}
}

func TestExecuteCreateSkillFromNL_ConfirmWithWrongHash(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	n := goodNode()
	n.Description = "Sends an email. Do NOT use for other comms."
	n.Recipe = "1. Send email. 2. Done."
	raw, _ := json.Marshal(nlSkillCreateInput{
		Description: "send email to team",
		Draft:       mustMarshal(n),
		Confirm:     true,
		DraftHash:   "wrong-hash-value",
	})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	if !res.IsError {
		t.Error("mismatched draft hash must return error")
	}
}

func TestExecuteCreateSkillFromNL_ConfirmWithoutHash(t *testing.T) {
	t.Setenv("AEGIS_NL_SKILL_CREATE_ENABLED", "true")
	n := goodNode()
	n.Description = "Sends an email. Do NOT use for other comms."
	n.Recipe = "1. Send email. 2. Done."
	raw, _ := json.Marshal(nlSkillCreateInput{
		Description: "send email to team",
		Draft:       mustMarshal(n),
		Confirm:     true,
		// DraftHash intentionally omitted
	})
	res := executeCreateSkillFromNL(nil, nil, nil, nil, nil, nil, raw)
	if !res.IsError {
		t.Error("confirm without draft_hash must return error")
	}
}

// mustMarshal is a test helper that marshals v or panics.
func mustMarshal(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
