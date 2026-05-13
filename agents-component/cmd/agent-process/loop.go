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
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/cerberOS/agents-component/internal/logfields"
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

	// maxOutputTokens caps the per-turn Anthropic output.
	//
	// Trade-offs considered:
	//   * Latency: non-streaming responses block the caller for the full
	//     generation. Haiku 4.5 tops out around ~120 tok/s server-side, so
	//     16K tokens is ≤ ~2 minutes worst case — within our task deadlines.
	//   * Cost: output tokens are ~5× input; bounding per-turn output keeps
	//     a runaway prompt from minting an unreasonable bill.
	//   * Repetition-loop containment: a hard cap is our last line of
	//     defence if the model gets stuck.
	//   * Long-form writing tasks ("expand this business plan", "write a
	//     detailed itinerary") legitimately need several thousand tokens.
	//     4K was far too tight for that class of prompt — it truncated
	//     mid-response and the error surfaced as the confusing
	//     "Maximum recovery attempts exceeded" message.
	//
	// 16K is a deliberate compromise: enough headroom for long-form content
	// while keeping latency and cost bounded. Going higher (32K/64K) would
	// need streaming + incremental UI rendering to stay tolerable.
	maxOutputTokens = 16_384
)

// persistConversationSnapshot is a thin wrapper over SessionLog.WriteConversationSnapshot
// that enforces the user-facing snapshot contract: only the agent flagged as
// user-facing by the orchestrator persists a conversation_snapshot, and the
// snapshot is a CLEAN [user: original_text, assistant: final_text] pair —
// never the orchestrator's "Execute this subtask…" scaffolding nor any
// tool_use turns. This prevents the multi-agent fan-out from polluting the
// user's conversation history with internal orchestrator instructions.
//
// finalAssistantText is the final visible reply text returned to the user.
// The function is a no-op when sl is nil, the spawn is not user-facing, the
// task is not part of a conversation, or the original user message is empty.
func persistConversationSnapshot(sl *SessionLog, log *slog.Logger, spawnCtx *SpawnContext, finalAssistantText string, totalTokens int64) {
	if sl == nil || spawnCtx == nil {
		return
	}
	if !spawnCtx.UserFacing {
		log.Debug("skipping conversation snapshot — agent is not user-facing",
			"task_id", spawnCtx.TaskID,
			"conversation_id", spawnCtx.ConversationID,
		)
		return
	}
	if spawnCtx.ConversationID == "" || spawnCtx.OriginalUserMessage == "" {
		return
	}
	// Build the snapshot from PriorTurns + a clean [user, assistant] pair.
	// We deliberately drop the in-flight `history` slice — it contains the
	// orchestrator-generated subtask instructions ("Execute this subtask…")
	// and any tool_use/tool_result turns. The factory only needs prior
	// user-visible exchanges to reconstruct context for the next turn.
	clean := make([]anthropic.MessageParam, 0, len(spawnCtx.PriorTurns)+2)
	clean = append(clean, spawnCtx.PriorTurns...)
	clean = append(clean, anthropic.NewUserMessage(anthropic.NewTextBlock(spawnCtx.OriginalUserMessage)))
	clean = append(clean, anthropic.NewAssistantMessage(anthropic.NewTextBlock(finalAssistantText)))
	if err := sl.WriteConversationSnapshot(spawnCtx.ConversationID, spawnCtx.TaskID, clean, totalTokens); err != nil {
		log.Warn("could not persist conversation snapshot; next follow-up turn will lack this exchange",
			"error", err)
	}
}

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
// priorTurns is the reconstructed conversation history from a previous task in
// the same conversation. When non-nil the turns are prepended to history before
// the new Instructions user message, enabling multi-turn conversation continuity.
// Pass nil for standalone (single-task) usage.
//
// opts are forwarded to the Anthropic client constructor after the API key
// option. Tests use this to inject option.WithBaseURL pointing at a mock server.
//
// RunLoop returns the final task result, the final history slice (for the caller
// to thread through the warm process-reuse loop), and any error.
func RunLoop(ctx context.Context, log *slog.Logger, spawnCtx *SpawnContext, ve *VaultExecutor, steerer *Steerer, as *AgentSpawner, budget *skills.SpecBudget, priorTurns []anthropic.MessageParam, opts ...option.RequestOption) (string, []anthropic.MessageParam, error) {
	// Fail fast when the task was pre-authorized with credentials but vault is
	// unreachable. A non-empty PermissionToken means the Orchestrator issued a
	// scoped token for this task — continuing without vault would silently drop
	// all credentialed tools, leaving the agent unable to complete the task with
	// no user-visible explanation.
	if ve == nil && spawnCtx.PermissionToken != "" {
		return "", nil, fmt.Errorf("vault unavailable: task was pre-authorized with credentials but the vault executor could not connect (NATS unreachable or env vars missing) — credentialed tools cannot be executed")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is not set")
	}
	clientOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	c := anthropic.NewClient(clientOpts...)
	client := &c

	// LLM response cache (Assignment #9 — LLM caching: Personalization).
	// Process-lifetime, TTL-bounded, user-scoped. Only end_turn responses are
	// cached; tool-use responses are never cached because they depend on
	// tool outputs that are not part of the cache key.
	llmCache := NewLLMCache()

	// Session log persists each turn to episodic memory (EDD §13.4).
	// Created before tools so memory tools can capture sl in their closures.
	// nil-safe: all methods are no-ops when sl is nil.
	sl := NewSessionLog(ve, log)
	ctx = WithSessionLog(ctx, sl)

	tools := toolsForDomain(spawnCtx.SkillDomain, ve, as, sl)
	// Build dynamic SkillTools for skills synthesized in prior sessions.
	// Each tool's Execute makes an inline LLM call using the stored recipe with
	// caller parameters substituted in. Existing builtins take precedence: a
	// synthesized name that matches a builtin is skipped (the builtin is more
	// reliable than the LLM-executed recipe for that operation).
	if len(spawnCtx.SynthesizedSkills) > 0 {
		existingNames := make(map[string]bool, len(tools))
		for _, t := range tools {
			existingNames[t.Definition.Name] = true
		}
		synthTools := buildSynthesizedTools(client, spawnCtx.SynthesizedSkills, existingNames)
		tools = append(tools, synthTools...)
	}
	// Memory tools (memory_update, profile_update, memory_search) are available
	// in every domain — they are not domain-specific skills.
	tools = append(tools, memoryTools(sl, spawnCtx.SkillDomain, spawnCtx.UserContextID)...)
	// Apply spec budget: register each tool's definition cost and evict LRU
	// entries if the ceiling is exceeded. Pinned tools (task_complete,
	// spawn_agent) are never evicted.
	tools = applySpecBudget(budget, spawnCtx.SkillDomain, tools, log)

	// DynamicRegistry extends the static tool list at runtime. skill_load holds
	// a reference to it so newly loaded skills become available on the next
	// Reason phase without restarting the agent.
	registry := newDynamicRegistry(tools)

	// Upgrade skills_search with a registry-aware version that can:
	//   1. Auto-register credential-free static tools discovered cross-domain.
	//   2. Send a user clarification request for credentialed cross-domain tools.
	// The plain version built by toolsForDomain had no registry reference because
	// the registry did not exist yet (chicken-and-egg). Replace it now.
	upgradedSearch := skillsSearchTool(sl, spawnCtx.SkillDomain, as != nil, registry, sl)
	if err := registry.Replace("skills_search", upgradedSearch); err != nil {
		log.Warn("skills_search upgrade failed; falling back to non-registry-aware version", "error", err)
	}

	if err := registry.Register(skillLoadTool(registry, sl, spawnCtx.SkillLoadAllowed)); err != nil {
		log.Warn("skill_load tool registration failed", "error", err)
	}
	if err := registry.Register(createSkillFromNLTool(client, sl, ve, spawnCtx, registry)); err != nil {
		log.Warn("create_skill_from_nl tool registration failed", "error", err)
	}

	// Pre-populate the registry with external skills persisted by skill_load in
	// prior sessions. Each record carries the serialised externalSkillManifest in
	// its Recipe field; buildHTTPSkillTool reconstructs the full SkillTool from it.
	for _, ext := range spawnCtx.ExternalSkills {
		var m externalSkillManifest
		if err := json.Unmarshal([]byte(ext.Recipe), &m); err != nil {
			log.Warn("external skill hydration: unmarshal manifest failed; skipping",
				"skill_name", ext.Name, "error", err)
			continue
		}
		if err := registry.Register(buildHTTPSkillTool(&m)); err != nil {
			log.Warn("external skill hydration: registration failed; skipping",
				"skill_name", ext.Name, "error", err)
		}
	}

	registeredTools := registry.Tools()
	finalToolNames := make([]string, len(registeredTools))
	for i, t := range registeredTools {
		finalToolNames[i] = t.Definition.Name
	}
	log.Info("agent tools registered for task",
		"skill_domain", spawnCtx.SkillDomain,
		"tool_count", len(registeredTools),
		"tool_names", finalToolNames,
		"command_manifest_chars", len(spawnCtx.CommandManifest),
	)

	systemPrompt := buildSystemPrompt(spawnCtx.SkillDomain, spawnCtx.CommandManifest, spawnCtx.AgentMemory, spawnCtx.UserProfile)

	// Build conversation history. When priorTurns is non-nil the turns from the
	// previous task in this conversation are prepended so the LLM sees the full
	// multi-turn context. The new Instructions user message is always appended last.
	history := make([]anthropic.MessageParam, 0, len(priorTurns)+1)
	history = append(history, priorTurns...)
	history = append(history, anthropic.NewUserMessage(anthropic.NewTextBlock(spawnCtx.Instructions)))

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
		// LLM cache: lookup first, fall back to Anthropic on miss. Only
		// end_turn responses are cached on write (see llmcache.go).
		// --------------------------------------------------------------------
		// Rebuild toolDefs each iteration so skills registered by skill_load
		// during a previous Act phase are visible to the next Reason call.
		toolDefs := toolDefinitions(registry.Tools())
		reqParams := anthropic.MessageNewParams{
			Model:     anthropic.ModelClaudeHaiku4_5,
			MaxTokens: int64(maxOutputTokens),
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Tools:     toolDefs,
			Messages:  history,
		}
		var resp *anthropic.Message
		cacheKey := ""
		if llmCache.Enabled() {
			cacheKey = llmCache.Key(spawnCtx.UserContextID, reqParams)
			if cached := llmCache.Lookup(cacheKey); cached != nil {
				log.Info("llm cache hit", "iter", iter, "key", cacheKey[:12])
				if ve != nil {
					ve.PublishMetricsEvent(types.MetricsEventLLMCacheHit, "", 0)
				}
				resp = cached
			}
		}
		if resp == nil {
			if llmCache.Enabled() && ve != nil {
				ve.PublishMetricsEvent(types.MetricsEventLLMCacheMiss, "", 0)
			}
			apiResp, err := client.Messages.New(ctx, reqParams)
			if err != nil {
				return "", nil, fmt.Errorf("react iter %d: API call: %w", iter, err)
			}
			resp = apiResp
			if llmCache.Enabled() && cacheKey != "" && resp.StopReason == anthropic.StopReasonEndTurn {
				llmCache.Store(cacheKey, resp)
			}
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
					attemptSkillSynthesis(ctx, client, log, spawnCtx, sl, ve, history, toolCallCount)
					history = append(history, resp.ToParam())
					persistConversationSnapshot(sl, log, spawnCtx, block.Text, totalTokens)
					log.Info("model returned end_turn with text reply; finishing task",
						"result_preview", logfields.PreviewHeadTail(block.Text, 15, 10),
						"tool_call_count", toolCallCount)
					return block.Text, history, nil
				}
			}
			return "", nil, fmt.Errorf("end_turn received with no text content in response")
		}

		// max_tokens: the model hit maxOutputTokens mid-generation. Rather
		// than discarding the partial text and failing the task, return
		// whatever content was produced with a truncation notice. The next
		// user turn (a follow-up) can ask the model to continue from here.
		//
		// Persist the snapshot the same way as end_turn so the conversation
		// state stays well-formed for the next task in this conversation.
		if resp.StopReason == anthropic.StopReasonMaxTokens {
			for _, block := range resp.Content {
				if block.Type == "text" && block.Text != "" {
					log.Warn("model hit max_tokens before finishing reply; returning partial text with truncation notice",
						"max_tokens", maxOutputTokens,
						"output_tokens", resp.Usage.OutputTokens,
						"result_preview", logfields.PreviewHeadTail(block.Text, 15, 10),
					)
					attemptSkillSynthesis(ctx, client, log, spawnCtx, sl, ve, history, toolCallCount)
					history = append(history, resp.ToParam())
					const truncationNotice = "\n\n_[Response truncated — output token limit reached. Send a follow-up message to continue.]_"
					persistConversationSnapshot(sl, log, spawnCtx, block.Text+truncationNotice, totalTokens)
					return block.Text + truncationNotice, history, nil
				}
			}
			return "", nil, fmt.Errorf("max_tokens stop with no text content in response")
		}

		if resp.StopReason != anthropic.StopReasonToolUse {
			return "", nil, fmt.Errorf("unexpected stop reason: %s", resp.StopReason)
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
				// WithCredentialCancelCtx embeds actCtx as a value so requestCredentialAndRetry
				// can retrieve it after dispatchTool wraps toolCtx with a per-tool deadline;
				// the credential poll loop monitors actCtx.Done() for steering interrupts
				// without being affected by the per-tool timeout.
				toolCtx := WithParentEntryID(actCtx, actParentID)
				toolCtx = WithCredentialCancelCtx(toolCtx, actCtx)
				start := time.Now()
				result := dispatchTool(toolCtx, registry.Tools(), c.name, c.input)
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
				"details", logfields.BoundedDetails(o.result.Details),
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
					isVaultDelegated(tools, o.call.name),
					isSynthesized(tools, o.call.name),
				)
			}

			toolResults = append(toolResults,
				anthropic.NewToolResultBlock(o.call.toolUseID, o.result.Content, o.result.IsError),
			)
		}

		if taskDone {
			log.Info("agent invoked task_complete tool with final result; closing react loop and writing conversation snapshot",
				"result_preview", logfields.PreviewHeadTail(finalResult, 15, 10),
				"result_len", len(finalResult),
				"tool_call_count", toolCallCount)
			attemptSkillSynthesis(ctx, client, log, spawnCtx, sl, ve, history, toolCallCount)

			// Close the tool_use → tool_result turn pair BEFORE snapshotting so
			// the persisted history always ends on a well-formed boundary.
			// Without this the final assistant turn contains tool_use blocks
			// (task_complete and any sibling parallel tool calls) with no
			// matching tool_result user turn after them; replaying that
			// snapshot as prior_turns on the next task in this conversation
			// is rejected by Anthropic with:
			//   "tool_use ids were found without tool_result blocks
			//    immediately after: toolu_…".
			if len(toolResults) > 0 {
				history = append(history, anthropic.NewUserMessage(toolResults...))
			}

			persistConversationSnapshot(sl, log, spawnCtx, finalResult, totalTokens)
			return finalResult, history, nil
		}

		// Not done yet — add tool results as the next user turn in history
		// so the next Reason iteration sees them.
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
				return "", nil, fmt.Errorf("task cancelled by steering directive: %s", capturedDirective.Instructions)
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
			return "", nil, fmt.Errorf("CONTEXT_OVERFLOW: token count %d exceeds %.0f%% of context window",
				totalTokens, hardAbortThreshold*100)
		case contextActionCompactPending:
			log.Info("compaction pending: token threshold reached",
				"tokens", totalTokens,
				"threshold_pct", int(compactThreshold*100),
			)
			compactionPending = true
		}
	}

	return "", nil, fmt.Errorf("max iterations (%d) reached without task completion", maxIterations)
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
	// Prepend an explicit 7-day weekday→date lookup table so the LLM doesn't
	// have to do its own date arithmetic. Models frequently mis-map weekdays
	// to dates near their training cutoff (e.g. labelling May 13 2026 as
	// "Tuesday" when it's Wednesday); a literal table eliminates that step.
	now := time.Now()
	var hb strings.Builder
	hb.WriteString(fmt.Sprintf(
		"Today is %s. Current local time is %s. When the user names a weekday (\"Monday\", \"Tuesday\", …) without a date, use the lookup table below — do NOT compute the date yourself. Times like \"7:30 AM\" with no timezone mean the user's local timezone (%s); include the offset in any ISO 8601 datetime you emit (e.g. 2026-05-12T07:30:00-07:00).\n\nDate lookup (next 7 days):\n",
		now.Format("Monday, January 2, 2006"),
		now.Format("15:04 MST"),
		now.Format("MST"),
	))
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, i)
		label := ""
		if i == 0 {
			label = " (today)"
		} else if i == 1 {
			label = " (tomorrow)"
		}
		hb.WriteString(fmt.Sprintf("  - %s = %s%s\n",
			d.Format("Monday"), d.Format("2006-01-02"), label))
	}
	hb.WriteString("\n")
	header := hb.String()

	var base string
	if skillDomain == "general" {
		base = `You are an Aegis OS general-purpose reasoning agent. ` +
			`Answer questions and complete tasks. ` +
			`When you need a specialized capability that is not in your current tool set, ` +
			`use skills_search to discover available domain-specific tools, then use spawn_agent to delegate to the appropriate domain. ` +
			`Never call create_skill_from_nl to execute a task — only call it when the user explicitly asks you to create, save, define, or teach a new reusable skill. ` +
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
	return header + base
}
