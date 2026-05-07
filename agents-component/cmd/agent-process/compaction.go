// Package main — compaction.go implements the context compaction algorithm from EDD §13.3.
//
// When the token count reaches 80% of the model context window, the runtime
// summarises turns older than the most recent 10 assistant turns and replaces
// them with a single structured summary message. This preserves 20% headroom
// for the current turn's output.
//
// Algorithm (§13.3):
//  1. Identify compaction window: all turns older than the most recent N (10).
//  2. Summarise: internal LLM call with structured prompt; preserve all tool call
//     outcomes, intermediate task state, commitments, and constraints; discard
//     conversational filler and raw content already processed.
//  3. Quality gate: summary must be < 25% of original window size.
//     If gate fails, fall back to extractive summary (tool names, status codes,
//     key values only).
//  4. Replace: compaction window removed; single summary message inserted
//     (role=user, prefixed [COMPACTED SUMMARY — turns N through M]).
//  5. Persist: compaction event written to episodic memory via state.write
//     (data_type: episode, entry_type: compaction).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

const (
	// compactionRetainTurns is the number of most-recent assistant turns
	// always kept verbatim during compaction (§13.3).
	compactionRetainTurns = 10

	// compactionMaxRatio is the quality gate: the summary must be smaller
	// than this fraction of the original window's JSON size (§13.3).
	compactionMaxRatio = 0.25

	// compactionSnippetBytes is the maximum number of bytes of a tool result's
	// content included in the extractive fallback summary.
	compactionSnippetBytes = 100
)

// compact reduces conversation history using the summarise-and-evict strategy
// from EDD §13.3. It retains the most recent compactionRetainTurns assistant
// turns verbatim and summarises all prior turns into a single summary message.
//
// sl is used to persist the compaction event to episodic memory (step 5).
// parentID is the entry_id of the preceding session entry; the compaction entry
// is chained to it. sl may be nil — persistence is skipped when unavailable.
//
// Returns the compacted history, the compaction entry_id (for parent chaining),
// and any error. On success, entry_id is always non-empty when sl is non-nil.
func compact(
	ctx context.Context,
	client *anthropic.Client,
	log *slog.Logger,
	history []anthropic.MessageParam,
	firstTurn, lastTurn int,
	sl *SessionLog,
	parentID string,
) ([]anthropic.MessageParam, string, error) {
	retainFrom := findRetentionBoundary(history)
	if retainFrom == 0 {
		// All turns fall within the retention window — nothing to compact.
		return history, parentID, nil
	}

	toCompact := history[:retainFrom]
	toRetain := history[retainFrom:]

	summary, err := summariseHistory(ctx, client, toCompact, firstTurn, lastTurn)
	if err != nil {
		log.Warn("LLM summarisation failed, using extractive fallback", "error", err)
		summary = extractiveSummary(toCompact, firstTurn, lastTurn)
	}

	// Step 5: Persist compaction event to episodic memory (EDD §13.3).
	compactionEntryID := sl.Write(turnTypeCompaction, summary, parentID, "")

	summaryMsg := anthropic.NewUserMessage(anthropic.NewTextBlock(summary))

	compacted := make([]anthropic.MessageParam, 0, 1+len(toRetain))
	compacted = append(compacted, summaryMsg)
	compacted = append(compacted, toRetain...)
	return compacted, compactionEntryID, nil
}

// findRetentionBoundary returns the history index at which the retention window
// begins. All turns before this index are subject to compaction; turns from this
// index onwards are kept verbatim (§13.3 step 1).
//
// Returns 0 when all turns fall within the retention window — i.e. there are
// fewer than compactionRetainTurns assistant turns in history and nothing should
// be compacted.
func findRetentionBoundary(history []anthropic.MessageParam) int {
	assistantCount := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == anthropic.MessageParamRoleAssistant {
			assistantCount++
			if assistantCount >= compactionRetainTurns {
				return i
			}
		}
	}
	return 0
}

// summariseHistory issues an internal LLM call to produce a structured summary
// of the turns in the compaction window. Returns an error if the quality gate
// fails (summary > 25% of original size), prompting extractive fallback.
func summariseHistory(
	ctx context.Context,
	client *anthropic.Client,
	history []anthropic.MessageParam,
	firstTurn, lastTurn int,
) (string, error) {
	historyJSON, err := json.Marshal(history)
	if err != nil {
		return "", fmt.Errorf("marshal history for summarisation: %w", err)
	}
	originalSize := len(historyJSON)

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 2048,
		System: []anthropic.TextBlockParam{
			{Text: "You are a precise summarisation assistant. " +
				"Preserve: all tool call outcomes, intermediate task state, commitments, and constraints. " +
				"Discard: conversational filler, redundant observations, and raw content already processed. " +
				"Be concise."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(fmt.Sprintf(
				"Summarise the following conversation history (turns %d through %d). "+
					"Output only the summary — no preamble.\n\n%s",
				firstTurn, lastTurn, string(historyJSON),
			))),
		},
	})
	if err != nil {
		return "", fmt.Errorf("summarisation API call: %w", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text == "" {
		return "", fmt.Errorf("summarisation returned empty response")
	}

	summary := resp.Content[0].Text

	// Quality gate: reject summaries that are too large.
	if float64(len(summary)) > float64(originalSize)*compactionMaxRatio {
		return "", fmt.Errorf("summary too large (%.0f%% of original; max %.0f%%)",
			float64(len(summary))/float64(originalSize)*100,
			compactionMaxRatio*100,
		)
	}

	return fmt.Sprintf("[COMPACTED SUMMARY — turns %d through %d]\n%s", firstTurn, lastTurn, summary), nil
}

// extractiveSummary is the fallback when LLM summarisation fails or exceeds
// the quality gate. It retains tool call names and result status codes with a
// brief content snippet — the minimum needed for the agent to understand what
// happened (§13.3 step 3 fallback).
func extractiveSummary(history []anthropic.MessageParam, firstTurn, lastTurn int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		"[COMPACTED SUMMARY — turns %d through %d]\nKey tool calls from prior turns:\n",
		firstTurn, lastTurn,
	))
	for _, msg := range history {
		switch msg.Role {
		case anthropic.MessageParamRoleAssistant:
			for _, block := range msg.Content {
				if tu := block.OfToolUse; tu != nil {
					sb.WriteString(fmt.Sprintf("- tool: %s\n", tu.Name))
				}
			}
		case anthropic.MessageParamRoleUser:
			for _, block := range msg.Content {
				tr := block.OfToolResult
				if tr == nil {
					continue
				}
				status := "ok"
				if tr.IsError.Valid() && tr.IsError.Value {
					status = "error"
				}
				snippet := ""
				for _, c := range tr.Content {
					if c.OfText != nil && c.OfText.Text != "" {
						snippet = c.OfText.Text
						if len(snippet) > compactionSnippetBytes {
							snippet = snippet[:compactionSnippetBytes] + "…"
						}
						break
					}
				}
				if snippet != "" {
					sb.WriteString(fmt.Sprintf("  result [%s]: %s\n", status, snippet))
				} else {
					sb.WriteString(fmt.Sprintf("  result [%s]\n", status))
				}
			}
		}
	}
	return sb.String()
}
