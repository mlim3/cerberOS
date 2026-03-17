// Package main is the agent-process binary that runs inside each Firecracker microVM.
//
// The binary is invoked by the Lifecycle Manager at spawn. It receives its initial
// context from stdin as a JSON-encoded SpawnContext, executes the task using the
// Anthropic API, and writes a JSON-encoded TaskOutput to stdout. All diagnostic
// logs go to stderr so they do not contaminate the JSON output stream.
//
// M2 scope: single Anthropic API call (no ReAct loop). The full ReAct loop is
// implemented in the next milestone.
//
// Inputs (stdin):
//
//	{
//	  "task_id":         "uuid",
//	  "skill_domain":    "web",
//	  "permission_token": "opaque-ref",   // credential_ref registered at spawn
//	  "instructions":    "Fetch the page at https://example.com",
//	  "trace_id":        "uuid"
//	}
//
// Outputs (stdout):
//
//	{
//	  "task_id":  "uuid",
//	  "trace_id": "uuid",
//	  "success":  true,
//	  "result":   "...",
//	  "error":    ""
//	}
//
// Environment:
//
//	ANTHROPIC_API_KEY — required; injected by the Lifecycle Manager at spawn.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// SpawnContext is the initial context injected by the Lifecycle Manager at spawn.
// It is delivered as JSON via stdin.
type SpawnContext struct {
	TaskID          string `json:"task_id"`
	SkillDomain     string `json:"skill_domain"`
	PermissionToken string `json:"permission_token"` // opaque credential ref — never a raw credential value
	Instructions    string `json:"instructions"`
	TraceID         string `json:"trace_id"`
}

// TaskOutput is the result written to stdout when the task completes or fails.
type TaskOutput struct {
	TaskID  string `json:"task_id"`
	TraceID string `json:"trace_id"`
	Success bool   `json:"success"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	var ctx SpawnContext
	if err := json.NewDecoder(os.Stdin).Decode(&ctx); err != nil {
		writeError(log, "", "", fmt.Sprintf("decode spawn context: %v", err))
		os.Exit(1)
	}

	log = log.With("task_id", ctx.TaskID, "trace_id", ctx.TraceID)

	if err := validate(&ctx); err != nil {
		writeError(log, ctx.TaskID, ctx.TraceID, err.Error())
		os.Exit(1)
	}

	log.Info("agent-process started", "skill_domain", ctx.SkillDomain)

	result, err := executeTask(log, &ctx)
	if err != nil {
		writeError(log, ctx.TaskID, ctx.TraceID, err.Error())
		os.Exit(1)
	}

	out := TaskOutput{
		TaskID:  ctx.TaskID,
		TraceID: ctx.TraceID,
		Success: true,
		Result:  result,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		log.Error("encode output failed", "error", err)
		os.Exit(1)
	}

	log.Info("agent-process completed")
}

// validate checks that all required SpawnContext fields are present.
func validate(ctx *SpawnContext) error {
	if ctx.TaskID == "" {
		return fmt.Errorf("spawn context: task_id is required")
	}
	if ctx.SkillDomain == "" {
		return fmt.Errorf("spawn context: skill_domain is required")
	}
	if ctx.Instructions == "" {
		return fmt.Errorf("spawn context: instructions are required")
	}
	if ctx.TraceID == "" {
		return fmt.Errorf("spawn context: trace_id is required")
	}
	return nil
}

// executeTask makes a single Anthropic API call and returns the model response.
// M2 scope only — ReAct loop is a separate milestone.
func executeTask(log *slog.Logger, ctx *SpawnContext) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	systemPrompt := buildSystemPrompt(ctx.SkillDomain)

	log.Info("calling Anthropic API")

	resp, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewTextBlock(ctx.Instructions),
			),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic API call: %w", err)
	}

	if len(resp.Content) == 0 {
		return "", fmt.Errorf("anthropic API returned empty response")
	}

	// Extract text from the first content block.
	text := resp.Content[0].Text
	log.Info("anthropic API call completed",
		"stop_reason", resp.StopReason,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
	)

	return text, nil
}

// buildSystemPrompt constructs a system prompt that scopes the agent to its
// assigned skill domain. In M3 this will be driven by the Skill Hierarchy Manager
// and include the full domain command manifest at spawn time.
func buildSystemPrompt(skillDomain string) string {
	return fmt.Sprintf(
		`You are an Aegis OS agent scoped to the "%s" skill domain. `+
			`Execute the assigned task using only the capabilities available within that domain. `+
			`Be concise and factual. Return only the task result — no preamble, no commentary.`,
		skillDomain,
	)
}

// writeError writes a failed TaskOutput to stdout and logs the error to stderr.
func writeError(log *slog.Logger, taskID, traceID, errMsg string) {
	log.Error("agent-process failed", "error", errMsg)
	out := TaskOutput{
		TaskID:  taskID,
		TraceID: traceID,
		Success: false,
		Error:   errMsg,
	}
	// Best-effort — if stdout encode fails here there is nothing more we can do.
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
