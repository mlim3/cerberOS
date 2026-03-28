// Package main — loop.go implements the four-phase ReAct execution loop.
//
// Per EDD §13.1:
//
//	Phase 1 — Reason:  LLM receives context window, produces text (done) or tool call (continue).
//	Phase 2 — Act:     Tool call dispatched to skill execute function.
//	Phase 3 — Observe: Result split — content enters LLM context; details go to monitoring only.
//	Phase 4 — Update:  Token count checked against compaction threshold.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	// modelContextWindow is the Claude Haiku 4.5 context window size in tokens.
	modelContextWindow = 200_000

	// compactThreshold is the token-usage fraction that triggers compaction (§13.3).
	compactThreshold = 0.80

	// hardAbortThreshold is the token-usage fraction that triggers CONTEXT_OVERFLOW (§13.3).
	hardAbortThreshold = 0.95

	// maxIterations is a safety cap that prevents infinite loops on misbehaving models.
	maxIterations = 50
)

// RunLoop executes the ReAct loop until the task is complete or a termination
// condition is met. It returns the final task result as a string.
//
// ve may be nil — credentialed vault tools are excluded from the tool registry
// when vault execution is unavailable.
func RunLoop(ctx context.Context, log *slog.Logger, spawnCtx *SpawnContext, ve *VaultExecutor) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}
	c := anthropic.NewClient(option.WithAPIKey(apiKey))
	client := &c

	tools := toolsForDomain(spawnCtx.SkillDomain, ve)
	toolDefs := toolDefinitions(tools)
	systemPrompt := buildSystemPrompt(spawnCtx.SkillDomain)

	history := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(spawnCtx.Instructions)),
	}

	// assistantTurn counts completed Reason phases — used to label compaction summaries.
	assistantTurn := 0
	compactedThrough := 0

	// compactionPending is set by Phase 4 when the token count reaches the 80%
	// threshold. It causes compaction to run before the next Reason phase.
	compactionPending := false

	// lastTotalTokens holds the token count from the most recent Reason API
	// response. Used in pre-Phase-1 log messages when compactionPending is set.
	var lastTotalTokens int64

	for iter := 0; iter < maxIterations; iter++ {
		// --------------------------------------------------------------------
		// Pre-Phase-1: compact if Phase 4 flagged it in the previous iteration.
		// --------------------------------------------------------------------
		if compactionPending {
			log.Info("compaction executing (pre-reason)",
				"last_total_tokens", lastTotalTokens,
				"threshold_pct", int(compactThreshold*100),
			)
			compacted, err := compact(ctx, client, log, history, compactedThrough+1, assistantTurn)
			if err != nil {
				log.Warn("compaction failed, continuing without compaction", "error", err)
			} else {
				history = compacted
				compactedThrough = assistantTurn
			}
			compactionPending = false
		}

		// --------------------------------------------------------------------
		// Phase 1: Reason — call the LLM with the current context window.
		// --------------------------------------------------------------------
		resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.ModelClaudeHaiku4_5,
			MaxTokens: int64(4096),
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Tools:     toolDefs,
			Messages:  history,
		})
		if err != nil {
			return "", fmt.Errorf("react iter %d: API call: %w", iter, err)
		}

		totalTokens := resp.Usage.InputTokens + resp.Usage.OutputTokens
		assistantTurn++

		log.Info("reason phase complete",
			"iter", iter,
			"stop_reason", resp.StopReason,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"total_tokens", totalTokens,
		)

		// end_turn with text content → task is complete.
		if resp.StopReason == anthropic.StopReasonEndTurn {
			for _, block := range resp.Content {
				if block.Type == "text" && block.Text != "" {
					return block.Text, nil
				}
			}
			return "", fmt.Errorf("end_turn received with no text content in response")
		}

		if resp.StopReason != anthropic.StopReasonToolUse {
			return "", fmt.Errorf("unexpected stop reason: %s", resp.StopReason)
		}

		// Append assistant message to history before processing its tool calls.
		history = append(history, resp.ToParam())

		// --------------------------------------------------------------------
		// Phase 2: Act + Phase 3: Observe — dispatch each tool call.
		// --------------------------------------------------------------------
		var toolResults []anthropic.ContentBlockParamUnion
		finalResult := ""
		taskDone := false

		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			toolUse := block.AsToolUse()

			log.Info("act phase: dispatching tool",
				"tool", toolUse.Name,
				"tool_use_id", toolUse.ID,
			)

			result := dispatchTool(ctx, tools, toolUse.Name, toolUse.Input)

			// Phase 3: Observe — details go to monitoring (stderr) only; never
			// enter LLM context. Content is what the LLM receives.
			log.Info("observe phase: tool result",
				"tool", toolUse.Name,
				"tool_use_id", toolUse.ID,
				"is_error", result.IsError,
				"details", result.Details,
			)

			// task_complete is the agent's explicit terminal signal.
			if toolUse.Name == toolNameTaskComplete && !result.IsError {
				finalResult = result.Content
				taskDone = true
			}

			toolResults = append(toolResults,
				anthropic.NewToolResultBlock(toolUse.ID, result.Content, result.IsError),
			)
		}

		if taskDone {
			log.Info("task_complete called", "result_len", len(finalResult))
			return finalResult, nil
		}

		// Add tool results as the next user turn in history.
		history = append(history, anthropic.NewUserMessage(toolResults...))

		// --------------------------------------------------------------------
		// Phase 4: Update Context — track total token count after Observe (§13.3).
		// totalTokens is from the Phase 1 API response; it is the best available
		// estimate until the next Reason phase receives updated usage data.
		// --------------------------------------------------------------------
		lastTotalTokens = totalTokens
		switch contextWindowAction(totalTokens) {
		case contextActionHardAbort:
			log.Error("hard abort: context overflow",
				"tokens", totalTokens,
				"threshold_pct", int(hardAbortThreshold*100),
			)
			if ve != nil {
				ve.PublishError("CONTEXT_OVERFLOW",
					fmt.Sprintf("token count %d exceeds %.0f%% of context window",
						totalTokens, hardAbortThreshold*100),
					spawnCtx.TraceID,
				)
			}
			return "", fmt.Errorf("CONTEXT_OVERFLOW: token count %d exceeds %.0f%% of context window",
				totalTokens, hardAbortThreshold*100)
		case contextActionCompactPending:
			log.Info("compaction pending: token threshold reached",
				"tokens", totalTokens,
				"threshold_pct", int(compactThreshold*100),
			)
			compactionPending = true
		}
	}

	return "", fmt.Errorf("max iterations (%d) reached without task completion", maxIterations)
}

// contextAction enumerates the token-budget decisions made by contextWindowAction.
type contextAction int

const (
	contextActionNone           contextAction = iota // < 80% of context window — no action needed
	contextActionCompactPending                      // ≥ 80% — set compactionPending; compact before next Reason phase
	contextActionHardAbort                           // ≥ 95% — abort current turn; emit CONTEXT_OVERFLOW
)

// contextWindowAction returns the action required based on the token count
// relative to the model context window thresholds (EDD §13.3). It is called
// in Phase 4 after every Observe phase so that compaction or hard abort can be
// applied before the next Reason (LLM) call.
func contextWindowAction(totalTokens int64) contextAction {
	tokens := float64(totalTokens)
	window := float64(modelContextWindow)
	switch {
	case tokens >= window*hardAbortThreshold:
		return contextActionHardAbort
	case tokens >= window*compactThreshold:
		return contextActionCompactPending
	default:
		return contextActionNone
	}
}

// buildSystemPrompt constructs a domain-scoped system prompt. In M3 this will
// include the full domain command manifest from the Skill Hierarchy Manager.
func buildSystemPrompt(skillDomain string) string {
	return fmt.Sprintf(
		`You are an Aegis OS agent scoped to the "%s" skill domain. `+
			`Execute the assigned task using only the capabilities available within that domain. `+
			`When the task is complete, call task_complete with the final result. `+
			`Be concise and factual.`,
		skillDomain,
	)
}
