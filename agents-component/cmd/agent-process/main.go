// Package main is the agent-process binary that runs inside each Firecracker microVM.
//
// The binary is invoked by the Lifecycle Manager at spawn. It receives its initial
// context either from stdin (process-manager mode) or from the Firecracker MMDS
// (Microvm Metadata Service) when AEGIS_MMDS_ENDPOINT is set in the environment.
//
// The binary executes the task using the four-phase ReAct loop (EDD §13.1) and
// writes a JSON-encoded TaskOutput to stdout. All diagnostic logs go to stderr
// so they do not contaminate the JSON output stream.
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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/cerberOS/agents-component/internal/skills"
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
	TaskID           string `json:"task_id"`
	SkillDomain      string `json:"skill_domain"`
	PermissionToken  string `json:"permission_token"` // opaque credential ref — never a raw credential value
	Instructions     string `json:"instructions"`
	CommandManifest  string `json:"command_manifest,omitempty"`  // "- name: description" list built by factory; injected into system prompt
	RecoveredContext string `json:"recovered_context,omitempty"` // non-empty on respawn
	TraceID          string `json:"trace_id"`
	UserContextID    string `json:"user_context_id,omitempty"` // propagated from parent TaskSpec; echoed in all outbound events (issue #67)
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

	spawnCtx, err := readSpawnContext(log)
	if err != nil {
		writeError(log, "", "", err.Error())
		os.Exit(1)
	}

	log = log.With("task_id", spawnCtx.TaskID, "trace_id", spawnCtx.TraceID)

	if err := validate(spawnCtx); err != nil {
		writeError(log, spawnCtx.TaskID, spawnCtx.TraceID, err.Error())
		os.Exit(1)
	}

	log.Info("agent-process started", "skill_domain", spawnCtx.SkillDomain)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startHeartbeat(ctx, log, spawnCtx.TaskID, spawnCtx.TraceID)

	// VaultExecutor manages the async request/result flow for credentialed operations
	// (ADR-004). Returns nil if NATS env vars are absent — non-credentialed tools
	// continue to function normally.
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

	// Build the per-agent spec budget when AEGIS_SKILL_SPEC_BUDGET is set.
	// 0 or unset = no budget enforcement (nil disables all budget logic in RunLoop).
	var specBudget *skills.SpecBudget
	if budgetStr := os.Getenv("AEGIS_SKILL_SPEC_BUDGET"); budgetStr != "" {
		if ceiling, err := strconv.Atoi(budgetStr); err == nil && ceiling > 0 {
			if b, err := skills.NewSpecBudget(ceiling); err == nil {
				specBudget = b
				log.Info("spec budget enabled", "ceiling_tokens", ceiling)
			} else {
				log.Warn("spec budget init failed, disabling budget enforcement", "error", err)
			}
		}
	}

	result, err := RunLoop(ctx, log, spawnCtx, ve, steerer, as, specBudget)
	if err != nil {
		writeError(log, spawnCtx.TaskID, spawnCtx.TraceID, err.Error())
		os.Exit(1)
	}

	out := TaskOutput{
		TaskID:  spawnCtx.TaskID,
		TraceID: spawnCtx.TraceID,
		Success: true,
		Result:  result,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		log.Error("encode output failed", "error", err)
		os.Exit(1)
	}

	log.Info("agent-process completed")
}

// readSpawnContext reads the SpawnContext from MMDS when AEGIS_MMDS_ENDPOINT is
// set in the environment (Firecracker microVM mode), or from stdin otherwise
// (process-manager / local-dev mode).
//
// When reading from MMDS the payload envelope also carries an "env" map of
// key→value pairs forwarded from the host. These are applied to the process
// environment so all subsequent os.Getenv calls observe the injected values.
func readSpawnContext(log *slog.Logger) (*SpawnContext, error) {
	mmdsEndpoint := os.Getenv("AEGIS_MMDS_ENDPOINT")
	if mmdsEndpoint == "" {
		// Stdin path — process-manager / local-dev mode.
		var ctx SpawnContext
		if err := json.NewDecoder(os.Stdin).Decode(&ctx); err != nil {
			return nil, fmt.Errorf("decode spawn context from stdin: %w", err)
		}
		return &ctx, nil
	}

	// MMDS path — Firecracker microVM mode.
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

	// Apply forwarded env vars so downstream os.Getenv calls observe them.
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
