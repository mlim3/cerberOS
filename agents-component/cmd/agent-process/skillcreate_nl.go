package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	nlSkillCreateToolName = "create_skill_from_nl"
	nlSkillCreateMaxTokens = 768
)

var (
	skillNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)
	secretLikeRE = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{12,}|xox[baprs]-[a-z0-9-]{12,}|api[_ -]?key\s*[:=]\s*\S+|token\s*[:=]\s*\S+|password\s*[:=]\s*\S+)`)
)

type nlSkillCreateInput struct {
	Description string          `json:"description"`
	Domain      string          `json:"domain,omitempty"`
	Name        string          `json:"name,omitempty"`
	Scope       string          `json:"scope,omitempty"`
	Confirm     bool            `json:"confirm,omitempty"`
	DraftHash   string          `json:"draft_hash,omitempty"`
	Draft       json.RawMessage `json:"draft,omitempty"`
	Overwrite   bool            `json:"overwrite,omitempty"`
}

type generatedSkill struct {
	Node       *types.SkillNode
	Mode       string
	Warnings   []string
	RiskReasons []string
	DraftHash  string
}

func nlSkillCreateEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AEGIS_NL_SKILL_CREATE_ENABLED")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func createSkillFromNLTool(client *anthropic.Client, sl *SessionLog, ve *VaultExecutor, spawnCtx *SpawnContext, existingNames map[string]bool) SkillTool {
	return SkillTool{
		Label: "Create Skill From Natural Language",
		Definition: anthropic.ToolParam{
			Name: nlSkillCreateToolName,
			Description: anthropic.String("Create a reusable learned skill from the user's natural-language description. Use only when the user explicitly asks to create, save, define, or teach a skill. Risky skills return a draft that requires confirmation before persistence. Do NOT use to execute the described task."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"description": map[string]interface{}{"type": "string", "description": "The user's natural-language description of the skill to create."},
					"domain": map[string]interface{}{"type": "string", "description": "Target skill domain. Defaults to the current domain."},
					"name": map[string]interface{}{"type": "string", "description": "Optional snake_case skill name requested by the user."},
					"scope": map[string]interface{}{"type": "string", "enum": []string{"user", "global"}, "description": "Skill visibility scope. Defaults to user."},
					"confirm": map[string]interface{}{"type": "boolean", "description": "Set true only after the user explicitly confirms a risky or overwrite draft."},
					"draft_hash": map[string]interface{}{"type": "string", "description": "Hash from the draft preview being confirmed."},
					"draft": map[string]interface{}{"type": "object", "description": "Optional exact draft SkillNode returned by a previous unpersisted preview."},
					"overwrite": map[string]interface{}{"type": "boolean", "description": "Set true only after the user explicitly confirms replacing an existing synthesized skill with the same name."},
				},
				Required: []string{"description"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeCreateSkillFromNL(ctx, client, sl, ve, spawnCtx, existingNames, raw)
		},
	}
}

func executeCreateSkillFromNL(ctx context.Context, client *anthropic.Client, sl *SessionLog, ve *VaultExecutor, spawnCtx *SpawnContext, existingNames map[string]bool, raw json.RawMessage) ToolResult {
	if !nlSkillCreateEnabled() {
		return ToolResult{Content: "Natural-language skill creation is disabled by AEGIS_NL_SKILL_CREATE_ENABLED.", IsError: true}
	}
	var input nlSkillCreateInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid create_skill_from_nl input: %v", err), IsError: true}
	}
	input.Description = strings.TrimSpace(input.Description)
	if input.Description == "" {
		return ToolResult{Content: "description is required", IsError: true}
	}
	domain := strings.TrimSpace(input.Domain)
	if domain == "" && spawnCtx != nil {
		domain = spawnCtx.SkillDomain
	}
	if domain == "" {
		domain = "general"
	}
	scope := strings.ToLower(strings.TrimSpace(input.Scope))
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "global" {
		return ToolResult{Content: "scope must be either user or global", IsError: true}
	}
	if scope == "global" {
		return ToolResult{Content: "Global skill creation is not available from chat. Ask a manager to publish shared skills through the admin flow.", IsError: true}
	}
	if secretLikeRE.MatchString(input.Description) {
		return ToolResult{Content: "The skill description appears to contain a credential or token value. Remove secrets and describe the credential type only.", IsError: true}
	}
	requestedName := sanitizeSkillName(input.Name)
	if requestedName == "" {
		requestedName = sanitizeSkillName(extractRequestedSkillName(input.Description))
	}
	generated, err := generateSkillFromNL(ctx, client, slog.Default(), domain, input.Description, requestedName, input.Draft)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("skill draft generation failed: %v", err), IsError: true}
	}
	generated.Node.OwnerUserID = ""
	if spawnCtx != nil {
		generated.Node.OwnerUserID = spawnCtx.UserContextID
	}
	generated.Node.Scope = scope
	generated.RiskReasons = classifySkillRisk(generated.Node, input.Description, scope)
	generated.DraftHash = draftHash(domain, generated.Node)
	if existingNames[generated.Node.Name] && !input.Overwrite {
		generated.RiskReasons = append(generated.RiskReasons, "name collides with an existing tool or skill; explicit overwrite confirmation is required")
	}
	needsConfirm := len(generated.RiskReasons) > 0
	if needsConfirm && !input.Confirm {
		return ToolResult{Content: formatSkillDraft(domain, generated, true)}
	}
	if needsConfirm && input.DraftHash != "" && input.DraftHash != generated.DraftHash {
		return ToolResult{Content: "The confirmed draft hash does not match the current generated draft. Ask the user to review the latest draft before persisting.", IsError: true}
	}
	if needsConfirm && input.DraftHash == "" {
		return ToolResult{Content: "Confirmation requires the draft_hash from the reviewed draft.", IsError: true}
	}
	if sl == nil {
		return ToolResult{Content: "Cannot persist skill: session log/NATS connection is unavailable.", IsError: true}
	}
	if err := sl.PersistSkillWithScope(domain, generated.Node, generated.Node.OwnerUserID, scope); err != nil {
		return ToolResult{Content: fmt.Sprintf("skill persistence failed: %v", err), IsError: true}
	}
	if err := sl.PublishSkillReload(domain, generated.Node.Name, scope); err != nil {
		return ToolResult{Content: fmt.Sprintf("created skill %s in domain %s, but live reload signal failed: %v. It should be available after restart.", generated.Node.Name, domain, err)}
	}
	if ve != nil {
		ve.EmitSkillSynthesized(domain, generated.Node.Name)
	}
	return ToolResult{Content: fmt.Sprintf("Created skill %s in domain %s using %s generation. It was persisted, reload was signaled, and it is scoped to this user.", generated.Node.Name, domain, generated.Mode)}
}

func generateSkillFromNL(ctx context.Context, client *anthropic.Client, log *slog.Logger, domain, description, requestedName string, draft json.RawMessage) (generatedSkill, error) {
	if len(draft) > 0 && string(draft) != "null" {
		var node types.SkillNode
		if err := json.Unmarshal(draft, &node); err != nil {
			return generatedSkill{}, fmt.Errorf("parse draft: %w", err)
		}
		if err := validateGeneratedSkill(&node); err != nil {
			return generatedSkill{}, err
		}
		return generatedSkill{Node: &node, Mode: "draft"}, nil
	}
	if client != nil {
		node, err := synthesizeSkillFromDescription(ctx, client, log, domain, description, requestedName)
		if err == nil && node != nil {
			return generatedSkill{Node: node, Mode: "llm"}, nil
		}
	}
	node := fallbackSkillFromDescription(description, requestedName)
	if err := validateGeneratedSkill(node); err != nil {
		return generatedSkill{}, err
	}
	return generatedSkill{Node: node, Mode: "fallback", Warnings: []string{"LLM generation was unavailable or invalid; used deterministic fallback"}}, nil
}

func synthesizeSkillFromDescription(ctx context.Context, client *anthropic.Client, log *slog.Logger, domain, description, requestedName string) (*types.SkillNode, error) {
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model: anthropic.ModelClaudeHaiku4_5,
		MaxTokens: nlSkillCreateMaxTokens,
		System: []anthropic.TextBlockParam{{Text: skillSynthesisSystemPrompt(domain, requestedName)}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Create a reusable skill from this user request. Output ONLY valid JSON.\n\n" + description)),
		},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Content) == 0 || resp.Content[0].Text == "" {
		return nil, fmt.Errorf("empty LLM response")
	}
	raw := strings.TrimSpace(resp.Content[0].Text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var sj synthesizedSkillJSON
	if err := json.Unmarshal([]byte(raw), &sj); err != nil {
		return nil, err
	}
	if sj.Name == "" {
		return nil, fmt.Errorf("LLM returned empty skill name")
	}
	if len(sj.Name) > 64 {
		sj.Name = sj.Name[:64]
	}
	if len(sj.Description) > 300 {
		sj.Description = sj.Description[:300]
	}
	now := time.Now().UTC()
	node := &types.SkillNode{Name: sj.Name, Level: "command", Label: sj.Label, Description: sj.Description, Recipe: sj.Recipe, Spec: sj.Spec, Origin: "synthesized", SynthesizedAt: &now}
	if err := validateGeneratedSkill(node); err != nil {
		if log != nil {
			log.Warn("NL skill generation: LLM draft rejected", "error", err)
		}
		return nil, err
	}
	return node, nil
}

func fallbackSkillFromDescription(description, requestedName string) *types.SkillNode {
	name := sanitizeSkillName(requestedName)
	if name == "" {
		name = deriveSkillName(description)
	}
	label := strings.TrimSpace(description)
	if len(label) > 60 {
		label = label[:57] + "..."
	}
	if label == "" {
		label = strings.ReplaceAll(name, "_", " ")
	}
	desc := ensureNegativeGuidance(description)
	if len(desc) > 300 {
		desc = desc[:297] + "..."
	}
	now := time.Now().UTC()
	return &types.SkillNode{
		Name:        name,
		Level:       "command",
		Label:       label,
		Description: desc,
		Recipe:      "1. Interpret the user's request using this learned procedure: " + description + "\n2. Use only currently available tools and context.\n3. Return a concise factual result.",
		Spec:        &types.SkillSpec{Parameters: map[string]types.ParameterDef{}},
		Origin:      "synthesized",
		SynthesizedAt: &now,
	}
}

func validateGeneratedSkill(node *types.SkillNode) error {
	if node == nil {
		return fmt.Errorf("skill node is nil")
	}
	if node.Level == "" {
		node.Level = "command"
	}
	node.Name = sanitizeSkillName(node.Name)
	if !skillNameRE.MatchString(node.Name) {
		return fmt.Errorf("skill name must be snake_case and <=64 characters")
	}
	if strings.TrimSpace(node.Recipe) == "" {
		return fmt.Errorf("recipe is required")
	}
	if secretLikeRE.MatchString(node.Description) || secretLikeRE.MatchString(node.Recipe) {
		return fmt.Errorf("generated skill contains credential-like text")
	}
	if node.Spec != nil {
		for name := range node.Spec.Parameters {
			if !strings.Contains(node.Recipe, "{{"+name+"}}") {
				return fmt.Errorf("recipe does not reference parameter %q", name)
			}
		}
	}
	return skills.ValidateCommandContract(node)
}

func classifySkillRisk(node *types.SkillNode, description, scope string) []string {
	text := strings.ToLower(description + " " + node.Description + " " + node.Recipe)
	checks := []struct{ phrase, reason string }{
		{"email", "outbound communication requires confirmation"},
		{"gmail", "outbound communication requires confirmation"},
		{"calendar", "calendar side effects require confirmation"},
		{"invite", "outbound communication requires confirmation"},
		{"send", "side-effecting actions require confirmation"},
		{"delete", "destructive actions require confirmation"},
		{"remove", "destructive actions require confirmation"},
		{"revoke", "destructive actions require confirmation"},
		{"transfer", "financial or transfer actions require confirmation"},
		{"payment", "financial actions require confirmation"},
		{"api key", "credential-related skills require confirmation"},
		{"token", "credential-related skills require confirmation"},
		{"password", "credential-related skills require confirmation"},
		{"every ", "recurring automation requires confirmation"},
		{"schedule", "scheduled automation requires confirmation"},
	}
	seen := map[string]bool{}
	var reasons []string
	if scope == "global" {
		reasons = append(reasons, "global scope requires manager approval")
		seen[reasons[0]] = true
	}
	if len(node.RequiredCredentialTypes) > 0 {
		reasons = append(reasons, "credentialed skills require confirmation")
		seen[reasons[0]] = true
	}
	for _, c := range checks {
		if strings.Contains(text, c.phrase) && !seen[c.reason] {
			reasons = append(reasons, c.reason)
			seen[c.reason] = true
		}
	}
	return reasons
}

func draftHash(domain string, node *types.SkillNode) string {
	body := map[string]interface{}{"domain": domain, "name": node.Name, "description": node.Description, "recipe": node.Recipe, "spec": node.Spec, "owner_user_id": node.OwnerUserID, "scope": node.Scope}
	b, _ := json.Marshal(body)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func formatSkillDraft(domain string, generated generatedSkill, confirmationRequired bool) string {
	reasons := append([]string(nil), generated.RiskReasons...)
	sort.Strings(reasons)
	payload := map[string]interface{}{
		"status": "confirmation_required",
		"domain": domain,
		"skill": generated.Node,
		"generation_mode": generated.Mode,
		"draft_hash": generated.DraftHash,
		"risk_reasons": reasons,
		"confirmation_required": confirmationRequired,
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return "Review this skill draft. If you approve, confirm with the exact draft_hash shown.\n" + string(b)
}

func deriveSkillName(text string) string {
	cleaned := strings.ToLower(text)
	cleaned = regexp.MustCompile(`[^a-z0-9\s_-]`).ReplaceAllString(cleaned, " ")
	parts := strings.Fields(strings.ReplaceAll(cleaned, "-", " "))
	if len(parts) > 5 {
		parts = parts[:5]
	}
	name := strings.Join(parts, "_")
	if sanitized := sanitizeSkillName(name); sanitized != "" {
		return sanitized
	}
	return "created_skill"
}

func sanitizeSkillName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	name = regexp.MustCompile(`[^a-z0-9_]`).ReplaceAllString(name, "_")
	name = regexp.MustCompile(`_+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return ""
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "skill_" + name
	}
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "_")
	}
	if name == "" {
		return ""
	}
	return name
}

func ensureNegativeGuidance(desc string) string {
	trimmed := strings.TrimSpace(desc)
	if strings.Contains(strings.ToLower(trimmed), "do not use") || strings.Contains(strings.ToLower(trimmed), "not for") {
		return trimmed
	}
	return trimmed + ". Do NOT use when the user has not explicitly requested this learned skill."
}
