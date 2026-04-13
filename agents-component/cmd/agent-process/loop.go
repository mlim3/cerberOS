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
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
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

// pendingToolCall is one tool_use block extracted from an assistant response,
// ready for parallel dispatch in Phase 2.
type pendingToolCall struct {
	toolUseID string
	name      string
	input     json.RawMessage
}

// toolOutcome pairs a dispatched tool call with its completed ToolResult.
// Outcomes are stored in the same index order as the assistant response so the
// Anthropic API receives tool results in the required order.
type toolOutcome struct {
	call      pendingToolCall
	result    ToolResult
	elapsedMS int64 // wall-clock execution time in milliseconds; used for skill_invocation telemetry
}

// RunLoop executes the ReAct loop until the task is complete or a termination
// condition is met. It returns the final task result as a string.
//
// ve may be nil — credentialed vault tools are excluded from the tool registry
// when vault execution is unavailable.
//
// steerer may be nil — steering support is disabled when absent (e.g. env vars
// not set). All existing behaviour is preserved with a nil steerer.
//
// Phase 2 (Act) dispatches all tool calls in a response concurrently (OQ-09).
// Results are collected in the original index order for Anthropic API compliance.
//
// budget optionally enforces a per-agent token ceiling on loaded SkillSpecs.
// When non-nil, applySpecBudget filters tools to those that fit within the
// ceiling (LRU eviction). Pass nil to disable budget enforcement.
//
// opts are forwarded to the Anthropic client constructor after the API key
// option. Tests use this to inject option.WithBaseURL pointing at a mock server.
func RunLoop(ctx context.Context, log *slog.Logger, spawnCtx *SpawnContext, ve *VaultExecutor, steerer *Steerer, as *AgentSpawner, budget *skills.SpecBudget, opts ...option.RequestOption) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}
	clientOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	c := anthropic.NewClient(clientOpts...)
	client := &c

	// Session log persists each turn to episodic memory (EDD §13.4).
	// Created before tools so memory tools can capture sl in their closures.
	// nil-safe: all methods are no-ops when sl is nil.
	sl := NewSessionLog(ve, log)
	ctx = WithSessionLog(ctx, sl)

	tools := toolsForDomain(spawnCtx.SkillDomain, ve, as)
	// Memory tools (memory_update, profile_update, memory_search) are available
	// in every domain — they are not domain-specific skills.
	tools = append(tools, memoryTools(sl, spawnCtx.SkillDomain, spawnCtx.UserContextID)...)
	// Apply spec budget: register each tool's definition cost and evict LRU
	// entries if the ceiling is exceeded. Pinned tools (task_complete,
	// spawn_agent) are never evicted.
	tools = applySpecBudget(budget, spawnCtx.SkillDomain, tools, log)
	toolDefs := toolDefinitions(tools)
	systemPrompt := buildSystemPrompt(spawnCtx.SkillDomain, spawnCtx.CommandManifest, spawnCtx.AgentMemory, spawnCtx.UserProfile)

	history := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(spawnCtx.Instructions)),
	}

	// Write root user_message entry — parent_entry_id is "" for the tree root.
	currentParentID := sl.Write(turnTypeUserMessage, spawnCtx.Instructions, "", "")

	// assistantTurn counts completed Reason phases — used to label compaction summaries.
	assistantTurn := 0
	compactedThrough := 0

	// toolCallCount accumulates every tool call dispatched across all iterations.
	// When the task completes, shouldSynthesize uses this to decide whether the
	// task was complex enough to warrant extracting a reusable skill.
	toolCallCount := 0

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
			if ve != nil {
				ve.PublishMetricsEvent(types.MetricsEventCompactionTriggered, "", 0)
			}
			compacted, compactionEntryID, err := compact(ctx, client, log, history, compactedThrough+1, assistantTurn, sl, currentParentID)
			if err != nil {
				log.Warn("compaction failed, continuing without compaction", "error", err)
			} else {
				history = compacted
				compactedThrough = assistantTurn
				if compactionEntryID != "" {
					currentParentID = compactionEntryID
				}
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

		// Persist assistant_response entry — collect text content for the payload.
		assistantText := ""
		for _, block := range resp.Content {
			if block.Type == "text" {
				assistantText = block.Text
				break
			}
		}
		assistantEntryID := sl.Write(turnTypeAssistantResponse, assistantText, currentParentID, "")
		currentParentID = assistantEntryID

		// end_turn with text content → task is complete.
		if resp.StopReason == anthropic.StopReasonEndTurn {
			for _, block := range resp.Content {
				if block.Type == "text" && block.Text != "" {
					attemptSkillSynthesis(ctx, client, log, spawnCtx, sl, history, toolCallCount)
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
		// Phase 2: Act — dispatch all tool calls concurrently (OQ-09).
		//
		// actCtx is derived from ctx; cancelling it (OQ-08 steering interrupt)
		// propagates to all in-flight vault executes simultaneously.
		//
		// All tool calls from the same assistant response are dispatched in
		// parallel. Outcomes are stored in the original index order so Phase 3
		// can assemble the tool-result user message in the order the Anthropic
		// API requires.
		// --------------------------------------------------------------------
		actCtx, cancelAct := context.WithCancel(ctx)

		// capturedDirectiveCh receives the steering directive (if any) captured
		// during this Act phase. Buffered size 1 — goroutine never blocks.
		capturedDirectiveCh := make(chan *types.SteeringDirective, 1)

		// steeringDone is used to synchronise with the steering goroutine before
		// draining capturedDirectiveCh. Without it there is a race: cancelAct()
		// fires, the main goroutine reaches the drain, but the steering goroutine
		// has not yet written to capturedDirectiveCh — so the drain takes the
		// default branch and silently drops the directive.
		var steeringDone sync.WaitGroup
		if steerer != nil {
			steeringDone.Add(1)
			go func() {
				defer steeringDone.Done()
				select {
				case d := <-steerer.Chan():
					dp := d // capture by value before sending pointer
					capturedDirectiveCh <- &dp
					if d.InterruptTool {
						cancelAct() // cancel actCtx → all concurrent dispatches interrupted
					}
				case <-actCtx.Done():
					// actCtx was cancelled (tools finished or parent ctx done).
					// Race guard: if a directive arrived in steerer.Chan() at the
					// same moment actCtx fired, Go's select may have chosen Done
					// over Chan non-deterministically. Do a non-blocking drain so
					// the directive is never silently dropped.
					select {
					case d := <-steerer.Chan():
						dp := d
						capturedDirectiveCh <- &dp
					default:
						// No directive pending — normal exit.
					}
				}
			}()
		}

		// Collect all tool_use blocks in the order the assistant emitted them.
		// Index order is preserved in outcomes so Phase 3 assembles results correctly.
		var pending []pendingToolCall
		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			tu := block.AsToolUse()
			pending = append(pending, pendingToolCall{
				toolUseID: tu.ID,
				name:      tu.Name,
				input:     tu.Input,
			})
		}

		// actParentID is the session log parent shared by all concurrent tool_call
		// entries — they all branch from the same assistant_response entry.
		actParentID := currentParentID

		// Fan-out: dispatch all tool calls concurrently.
		// outcomes[i] receives the result for pending[i] — index order preserved.
		outcomes := make([]toolOutcome, len(pending))
		var wg sync.WaitGroup
		for i, call := range pending {
			wg.Add(1)
			go func(idx int, c pendingToolCall) {
				defer wg.Done()
				log.Info("act phase: dispatching tool",
					"tool", c.name,
					"tool_use_id", c.toolUseID,
					"parallel_index", idx,
					"parallel_total", len(pending),
				)
				// Thread actCtx so vault operations are cancelled on steering interrupt.
				// actParentID propagates to vault tools for session log linking (EDD §13.4).
				toolCtx := WithParentEntryID(actCtx, actParentID)
				start := time.Now()
				result := dispatchTool(toolCtx, tools, c.name, c.input)
				outcomes[idx] = toolOutcome{call: c, result: result, elapsedMS: time.Since(start).Milliseconds()}
			}(i, call)
		}
		wg.Wait() // all concurrent dispatches complete (or are interrupted) before Phase 3

		// Accumulate total tool calls for synthesis threshold check at task end.
		toolCallCount += len(outcomes)

		// Update LRU order for each successfully-dispatched tool so that
		// recently-used specs are retained longest when the budget must evict.
		if budget != nil {
			for _, o := range outcomes {
				if !o.result.IsError {
					budget.Touch(spawnCtx.SkillDomain, o.call.name)
				}
			}
		}

		// Determine whether a steering interrupt fired during dispatch.
		toolInterrupted := actCtx.Err() != nil
		// Stop the steering monitor goroutine; it will exit via actCtx.Done().
		cancelAct()
		// Wait for the steering goroutine to finish writing to capturedDirectiveCh
		// before the drain below. Without this, the drain races with the goroutine
		// and may take the default branch while the goroutine is mid-write.
		steeringDone.Wait()

		// --------------------------------------------------------------------
		// Phase 3: Observe — process outcomes in index order.
		// Content enters LLM context; Details go to monitoring only (§13.2).
		// Session entries are written serially here (single goroutine) so the
		// parent chain remains consistent even though dispatch was concurrent.
		// --------------------------------------------------------------------
		var toolResults []anthropic.ContentBlockParamUnion
		finalResult := ""
		taskDone := false

		for _, o := range outcomes {
			log.Info("observe phase: tool result",
				"tool", o.call.name,
				"tool_use_id", o.call.toolUseID,
				"is_error", o.result.IsError,
				"details", o.result.Details,
				"tool_interrupted", toolInterrupted,
			)

			// Persist tool_result entry. Parent is the tool_call session entry
			// written by vault tools (SessionEntryID); falls back to actParentID
			// for local tools (which don't write a tool_call entry themselves).
			// All concurrent tool results fan out from actParentID in the tree.
			toolResultParentID := o.result.SessionEntryID
			if toolResultParentID == "" {
				toolResultParentID = actParentID
			}
			toolResultEntryID := sl.Write(turnTypeToolResult, o.result.Content, toolResultParentID, "")
			currentParentID = toolResultEntryID // advance linearly through index order

			// task_complete is the agent's explicit terminal signal.
			if o.call.name == toolNameTaskComplete && !o.result.IsError {
				finalResult = o.result.Content
				taskDone = true
			}

			// Emit skill_invocation telemetry for every domain tool call.
			// task_complete is a control signal, not a skill — skip it.
			if o.call.name != toolNameTaskComplete {
				ve.EmitSkillInvocation(
					spawnCtx.SkillDomain,
					o.call.name,
					drillDownDepth(o.call.name),
					o.elapsedMS,
					outcomeFromResult(o.result),
				)
			}

			toolResults = append(toolResults,
				anthropic.NewToolResultBlock(o.call.toolUseID, o.result.Content, o.result.IsError),
			)
		}

		if taskDone {
			log.Info("task_complete called", "result_len", len(finalResult))
			attemptSkillSynthesis(ctx, client, log, spawnCtx, sl, history, toolCallCount)
			return finalResult, nil
		}

		// Add tool results as the next user turn in history.
		if len(toolResults) > 0 {
			history = append(history, anthropic.NewUserMessage(toolResults...))
		}

		// Drain captured directive (non-blocking — goroutine may not have fired).
		var capturedDirective *types.SteeringDirective
		select {
		case capturedDirective = <-capturedDirectiveCh:
		default:
		}

		if capturedDirective != nil {
			// Acknowledge receipt to the Orchestrator.
			ackStatus := "received"
			if toolInterrupted {
				ackStatus = "applied"
			}
			steerer.Ack(capturedDirective.DirectiveID, ackStatus, "")

			// Inject the directive as a structured user-turn message so the LLM
			// sees it cleanly before the next Reason phase (OQ-08).
			steeringMsg := formatSteeringMessage(capturedDirective)
			steeringEntryID := sl.Write(turnTypeSteeringDirective, steeringMsg, currentParentID, "")
			currentParentID = steeringEntryID
			history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(steeringMsg)))

			log.Info("steering directive applied",
				"directive_id", capturedDirective.DirectiveID,
				"type", capturedDirective.Type,
				"tool_interrupted", toolInterrupted,
			)

			// cancel directive: terminate the task (partial results are acceptable).
			if capturedDirective.Type == "cancel" {
				return "", fmt.Errorf("task cancelled by steering directive: %s", capturedDirective.Instructions)
			}
		}

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
				ve.PublishMetricsEvent(types.MetricsEventContextOverflow, "", 0)
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

// buildSystemPrompt constructs a domain-scoped system prompt that includes the
// command manifest built by the factory at spawn time (EDD §13.1).
//
// manifest is the pre-formatted "- name: description\n..." list passed via
// SpawnContext. When non-empty it is appended to the base prompt so the agent
// knows what commands are available from turn 1 without a discovery round-trip.
// For the "general" domain there is no skill manifest to inject.
//
// agentMemory and userProfile are injected as read-only context sections when
// non-empty. They are excluded from the spawn context token budget because they
// are system overhead, not task payload.
func buildSystemPrompt(skillDomain, manifest, agentMemory, userProfile string) string {
	var base string
	if skillDomain == "general" {
		base = `You are an Aegis OS general-purpose reasoning agent. ` +
			`Answer questions and complete tasks using your own knowledge and reasoning. ` +
			`When the task is complete, call task_complete with the final result. ` +
			`Be concise and factual.`
	} else {
		base = fmt.Sprintf(
			`You are an Aegis OS agent scoped to the "%s" skill domain. `+
				`Execute the assigned task using only the capabilities available within that domain. `+
				`When the task is complete, call task_complete with the final result. `+
				`Be concise and factual.`,
			skillDomain,
		)
		if manifest != "" {
			base += "\n\nAvailable commands:\n" + manifest
		}
	}
	if agentMemory != "" {
		base += "\n\n## Knowledge from past tasks\n" + agentMemory
	}
	if userProfile != "" {
		base += "\n\n## User context\n" + userProfile
	}
	return base
}
