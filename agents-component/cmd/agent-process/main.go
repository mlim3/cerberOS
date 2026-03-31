// Package main is the agent-process binary that runs inside each Firecracker microVM.
//
// The binary is invoked by the Lifecycle Manager at spawn. It receives its initial
// context from stdin as a JSON-encoded SpawnContext, executes the task using the
// four-phase ReAct loop (EDD §13.1), and writes a JSON-encoded TaskOutput to
// stdout. All diagnostic logs go to stderr so they do not contaminate the JSON
// output stream.
//
// Inputs (stdin):
//
//	{
//	  "task_id":         "uuid",
//	  "skill_domain":    "web",
//	  "permission_token": "opaque-ref",
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

	var spawnCtx SpawnContext
	if err := json.NewDecoder(os.Stdin).Decode(&spawnCtx); err != nil {
		writeError(log, "", "", fmt.Sprintf("decode spawn context: %v", err))
		os.Exit(1)
	}

	log = log.With("task_id", spawnCtx.TaskID, "trace_id", spawnCtx.TraceID)

	if err := validate(&spawnCtx); err != nil {
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

	result, err := RunLoop(ctx, log, &spawnCtx, ve, steerer)
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
