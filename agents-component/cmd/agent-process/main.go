// Package main is the agent-process binary that runs inside each Firecracker microVM.
//
// The binary is invoked by the Lifecycle Manager at spawn. It receives its initial
// context either from stdin (process-manager mode) or from the Firecracker MMDS
// (Microvm Metadata Service) when AEGIS_MMDS_ENDPOINT is set in the environment.
//
// In stdin mode the binary loops: each iteration reads one JSON SpawnContext,
// runs the four-phase ReAct loop (EDD §13.1), writes a single JSON-encoded
// TaskOutput followed by a newline to stdout, and waits for the next context.
// On stdin EOF it exits cleanly with code 0. This lets the Lifecycle Manager
// reuse a warm process across tasks (zero Vault + fork tax on Priority-1 IDLE
// reuse) while remaining fully backward compatible with the old one-shot
// contract — a caller that sends exactly one SpawnContext and closes stdin
// gets exactly one TaskOutput and a clean exit.
//
// In MMDS mode the one-shot contract is preserved (the Firecracker host only
// publishes a single SpawnContext per VM); reuse is not wired up there.
//
// All diagnostic logs go to stderr so they do not contaminate the JSON output
// stream.
//
// Inputs (stdin or MMDS at AEGIS_MMDS_ENDPOINT):
//
//	{
//	  "task_id":         "uuid",
//	  "skill_domain":    "web",
//	  "permission_token": "opaque-ref",
//	  "instructions":    "Fetch the page at https://example.com",
//	  "trace_id":        "uuid"
//	}
//
// MMDS envelope (when AEGIS_MMDS_ENDPOINT is set):
//
//	{
//	  "spawn_context": { ...SpawnContext fields... },
//	  "env":           { "KEY": "VALUE", ... }
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
//	ANTHROPIC_API_KEY     — required; injected by the Lifecycle Manager at spawn.
//	AEGIS_MMDS_ENDPOINT   — when set, SpawnContext is read from this MMDS URL
//	                        instead of stdin (Firecracker microVM mode).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/logfields"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/internal/telemetry"
)

// mmdsPayload is the envelope written to the Firecracker MMDS by the Lifecycle
// Manager before InstanceStart. Agent-process reads this when AEGIS_MMDS_ENDPOINT
// is set; falls back to reading SpawnContext from stdin otherwise.
type mmdsPayload struct {
	SpawnContext SpawnContext      `json:"spawn_context"`
	Env          map[string]string `json:"env"`
}

// SpawnContext is the initial context injected by the Lifecycle Manager at spawn.
// It is delivered as JSON via stdin.
type SpawnContext struct {
	TaskID           string                   `json:"task_id"`
	SkillDomain      string                   `json:"skill_domain"`
	PermissionToken  string                   `json:"permission_token"` // opaque credential ref — never a raw credential value
	Instructions     string                   `json:"instructions"`
	CommandManifest  string                   `json:"command_manifest,omitempty"`  // "- name: description" list built by factory; injected into system prompt
	RecoveredContext string                   `json:"recovered_context,omitempty"` // non-empty on respawn
	AgentMemory      string                   `json:"agent_memory,omitempty"`      // distilled facts from past tasks in this domain; injected into system prompt
	UserProfile      string                   `json:"user_profile,omitempty"`      // user preference observations; injected into system prompt
	TraceID          string                   `json:"trace_id"`
	UserContextID    string                   `json:"user_context_id,omitempty"` // propagated from parent TaskSpec; echoed in all outbound events (issue #67)
	ConversationID   string                   `json:"conversation_id,omitempty"` // non-empty when this task continues a prior conversation
	PriorTurns       []anthropic.MessageParam `json:"prior_turns,omitempty"`     // reconstructed history from factory; nil for standalone tasks
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
	rootLog := slog.New(slog.NewJSONHandler(os.Stderr, nil)).
		With("component", "agents", "module", "agent-process")
	slog.SetDefault(rootLog)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OTLP tracing is process-scoped (one exporter + one TracerProvider across
	// every task we handle). Each iteration of the task loop opens its own
	// child span rooted at the incoming SpawnContext.TraceID so subsequent
	// reused-process tasks still show up in Tempo as independent traces.
	traceShutdown, tErr := telemetry.Init(rootCtx)
	if tErr != nil {
		rootLog.Warn("telemetry init failed — continuing without traces", "error", tErr)
	}
	defer func() {
		if traceShutdown != nil {
			_ = traceShutdown(context.Background())
		}
	}()

	// Process-scoped spec budget. Resetting per-task would defeat the point of
	// a budget (it bounds total skill-spec tokens loaded over the lifetime of
	// a single agent). Long-lived reused processes therefore share one budget.
	var specBudget *skills.SpecBudget
	if budgetStr := os.Getenv("AEGIS_SKILL_SPEC_BUDGET"); budgetStr != "" {
		if ceiling, err := strconv.Atoi(budgetStr); err == nil && ceiling > 0 {
			if b, err := skills.NewSpecBudget(ceiling); err == nil {
				specBudget = b
				rootLog.Info("spec budget enabled", "ceiling_tokens", ceiling)
			} else {
				rootLog.Warn("spec budget init failed, disabling budget enforcement", "error", err)
			}
		}
	}

	// Shared stdout writer: we must emit exactly one JSON object per task,
	// newline-delimited, so the host-side Lifecycle Manager can frame task
	// boundaries when the process is reused across tasks.
	encoder := json.NewEncoder(os.Stdout)
	stdinDecoder := json.NewDecoder(os.Stdin)

	// persistedHistory and persistedConversationID support the warm-path
	// optimisation: when a reused process handles consecutive tasks in the
	// same conversation the in-memory history slice is passed directly to
	// RunLoop, skipping the JSON deserialisation of PriorTurns entirely.
	var persistedHistory []anthropic.MessageParam
	var persistedConversationID string

	iteration := 0
	for {
		iteration++
		spawnCtx, err := readNextSpawnContext(rootLog, stdinDecoder)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Clean shutdown — host closed stdin.
				if iteration == 1 {
					rootLog.Warn("agent-process exiting: stdin closed before any SpawnContext was delivered")
				} else {
					rootLog.Info("agent-process exiting: stdin closed", "tasks_handled", iteration-1)
				}
				return
			}
			// Decode error on a non-first task: emit a best-effort failure
			// envelope and exit. We cannot recover from a corrupt framing.
			writeError(rootLog, "", "", fmt.Sprintf("decode spawn context: %v", err))
			os.Exit(1)
		}

		// Choose the history source. When this process already holds the prior
		// turns in memory (warm path) prefer those over the factory-injected
		// PriorTurns to avoid unnecessary deserialisation. Fall back to the
		// factory-injected turns for cold provisions and conversation switches.
		var injectedHistory []anthropic.MessageParam
		if spawnCtx.ConversationID != "" &&
			spawnCtx.ConversationID == persistedConversationID &&
			len(persistedHistory) > 0 {
			injectedHistory = persistedHistory
		} else {
			injectedHistory = spawnCtx.PriorTurns
		}

		finalHistory, err := runOneTask(rootCtx, rootLog, spawnCtx, specBudget, encoder, injectedHistory)

		// Thread the final history for the next iteration if this task was part
		// of a conversation. Reset on conversation change or standalone tasks.
		if spawnCtx.ConversationID != "" && finalHistory != nil {
			persistedHistory = finalHistory
			persistedConversationID = spawnCtx.ConversationID
		} else {
			persistedHistory = nil
			persistedConversationID = ""
		}

		if err != nil {
			// runOneTask already emitted a failure TaskOutput on stdout; continue
			// to the next iteration so a transient task failure does not kill the
			// reusable process. The factory observes the failed TaskOutput and
			// tears the process down itself if it considers the agent unhealthy.
			rootLog.Error("task failed", "error", err, "task_id", spawnCtx.TaskID)
		}
	}
}

// readNextSpawnContext reads one SpawnContext from the shared decoder in stdin
// mode, or fetches the MMDS envelope in Firecracker mode. Returns io.EOF when
// the stream is cleanly closed so the caller can exit with status 0.
func readNextSpawnContext(log *slog.Logger, dec *json.Decoder) (*SpawnContext, error) {
	if os.Getenv("AEGIS_MMDS_ENDPOINT") != "" {
		// MMDS mode: fetch once, then signal EOF on the next call so the loop
		// terminates (Firecracker VM reuse is not wired up yet).
		if mmdsDelivered {
			return nil, io.EOF
		}
		mmdsDelivered = true
		return readSpawnContextFromMMDS(log)
	}
	var ctx SpawnContext
	if err := dec.Decode(&ctx); err != nil {
		return nil, err
	}
	return &ctx, nil
}

// mmdsDelivered flips true after the first MMDS read so the loop exits on the
// next iteration (Firecracker mode remains one-shot).
var mmdsDelivered bool

// runOneTask executes a single TaskSpec end-to-end. It emits exactly one
// newline-delimited TaskOutput to stdout (success or failure). It returns the
// final conversation history slice (for warm-path threading) and any error.
// framing and output are always written to stdout regardless of error.
func runOneTask(rootCtx context.Context, rootLog *slog.Logger, spawnCtx *SpawnContext, specBudget *skills.SpecBudget, encoder *json.Encoder, priorTurns []anthropic.MessageParam) ([]anthropic.MessageParam, error) {
	log := rootLog.With("task_id", spawnCtx.TaskID, "trace_id", spawnCtx.TraceID)
	if spawnCtx.ConversationID != "" {
		log = log.With("conversation_id", spawnCtx.ConversationID)
	}

	if err := validate(spawnCtx); err != nil {
		writeError(log, spawnCtx.TaskID, spawnCtx.TraceID, err.Error())
		return nil, err
	}

	log.Info("agent-process started for task; loading skills, vault, steerer, spawner",
		"skill_domain", spawnCtx.SkillDomain,
		"prior_turn_count", len(priorTurns),
		"is_followup", len(priorTurns) > 0,
		"recovered_from_crash", spawnCtx.RecoveredContext != "",
		"content_preview", logfields.PreviewHeadTail(spawnCtx.Instructions, 15, 10))

	taskCtx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	taskCtx = telemetry.ContextWithTraceID(taskCtx, spawnCtx.TraceID)
	taskCtx, span := telemetry.Tracer().Start(taskCtx, "agent-process.run")
	defer span.End()

	startHeartbeat(taskCtx, log, spawnCtx.TaskID, spawnCtx.TraceID)

	// VaultExecutor manages the async request/result flow for credentialed operations
	// (ADR-004). Returns nil if NATS env vars are absent — non-credentialed tools
	// continue to function normally. Task-scoped so each task gets its own
	// permission token binding.
	ve := NewVaultExecutor(log, spawnCtx.TaskID, spawnCtx.PermissionToken, spawnCtx.TraceID)
	if ve != nil {
		defer ve.Close()
	}

	// Steerer subscribes to the per-agent steering subject and buffers directives
	// for the ReAct loop Act phase (OQ-08). Returns nil when env vars are absent;
	// all loop behaviour is preserved with a nil steerer.
	steerer := NewSteerer(log, spawnCtx.TaskID, spawnCtx.TraceID)
	if steerer != nil {
		defer steerer.Close()
	}

	// AgentSpawner enables the agent-as-tool pattern (issue #67, EDD §13.6):
	// the agent can request a child agent from the Orchestrator by calling the
	// spawn_agent tool. Returns nil when NATS env vars are absent — spawn_agent
	// is excluded from the tool registry when as == nil.
	as := NewAgentSpawner(log, spawnCtx.TaskID, spawnCtx.TraceID, spawnCtx.UserContextID)
	if as != nil {
		defer as.Close()
	}

	result, finalHistory, err := RunLoop(taskCtx, log, spawnCtx, ve, steerer, as, specBudget, priorTurns)
	if err != nil {
		writeError(log, spawnCtx.TaskID, spawnCtx.TraceID, err.Error())
		return nil, err
	}

	out := TaskOutput{
		TaskID:  spawnCtx.TaskID,
		TraceID: spawnCtx.TraceID,
		Success: true,
		Result:  result,
	}
	if err := encoder.Encode(out); err != nil {
		log.Error("could not write task output to stdout protocol; aborting agent process so factory respawns",
			"error", err)
		// Encode failure means stdout is broken — can't meaningfully continue
		// the loop because the host can't observe our outputs.
		os.Exit(1)
	}

	log.Info("agent-process finished task (success); returning final result to factory",
		"result_preview", logfields.PreviewHeadTail(result, 15, 10),
		"final_turn_count", len(finalHistory))
	return finalHistory, nil
}

// readSpawnContextFromMMDS reads a single SpawnContext envelope from the
// Firecracker MMDS at AEGIS_MMDS_ENDPOINT and applies any forwarded env vars
// to the current process so downstream os.Getenv calls observe them.
// Firecracker mode remains one-shot; reuse is not wired up there.
func readSpawnContextFromMMDS(log *slog.Logger) (*SpawnContext, error) {
	mmdsEndpoint := os.Getenv("AEGIS_MMDS_ENDPOINT")
	log.Info("reading spawn context from MMDS", "endpoint", mmdsEndpoint)

	resp, err := http.Get(mmdsEndpoint) //nolint:noctx,gosec // endpoint is operator-controlled
	if err != nil {
		return nil, fmt.Errorf("fetch MMDS payload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch MMDS payload: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read MMDS response body: %w", err)
	}

	var payload mmdsPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode MMDS payload: %w", err)
	}

	for k, v := range payload.Env {
		if err := os.Setenv(k, v); err != nil {
			log.Warn("failed to set env var from MMDS", "key", k, "error", err)
		}
	}

	return &payload.SpawnContext, nil
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

// writeError writes a failed TaskOutput to stdout and logs the error to stderr.
func writeError(log *slog.Logger, taskID, traceID, errMsg string) {
	log.Error("agent-process failed", "error", errMsg)
	out := TaskOutput{
		TaskID:  taskID,
		TraceID: traceID,
		Success: false,
		Error:   errMsg,
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}
