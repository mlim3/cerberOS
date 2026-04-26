// Package main — telemetry.go provides helper types and functions for skill
// usage telemetry.
//
// Every tool call dispatched by the ReAct loop emits a skill_invocation audit
// event to aegis.orchestrator.audit.event. These events feed downstream analysis
// of which skills to pre-warm, which to evict first, and which descriptions need
// improvement (high invocation rate + high error rate = bad description).
//
// The three telemetry fields covered here are:
//
//   - outcome:          "success" | "error" | "timeout"
//   - drill_down_depth: "domain" | "command" | "spec" — how far the agent
//     drilled into the skill hierarchy before invoking
//
// Publishing is handled by VaultExecutor.EmitSkillInvocation, which delegates
// to the existing emitAudit infrastructure. If NATS is unavailable (ve == nil),
// emission is a no-op — telemetry must never affect the ReAct loop.
package main

import (
	"strings"
)

// Drill-down depth values for skill_invocation events.
// They encode how far into the skill hierarchy the agent travelled before
// invoking a command, which signals whether progressive disclosure is working.
const (
	DepthDomain  = "domain"  // agent knew only the domain; invoked via domain-level search
	DepthCommand = "command" // agent knew the command name; invoked without spec detail
	DepthSpec    = "spec"    // agent had the full parameter spec available before invoking
)

// Outcome values for skill_invocation events.
const (
	OutcomeSuccess = "success"
	OutcomeError   = "error"
	OutcomeTimeout = "timeout"
)

// outcomeFromResult classifies a ToolResult into one of the three outcome
// values for skill_invocation telemetry.
//
//   - "timeout" — content contains TOOL_TIMEOUT or TOOL_INTERRUPTED
//     (local deadline, vault cancel, or steering interrupt).
//   - "error"   — IsError is true and the content is not a timeout message.
//   - "success" — IsError is false.
func outcomeFromResult(r ToolResult) string {
	if !r.IsError {
		return OutcomeSuccess
	}
	if strings.Contains(r.Content, "TOOL_TIMEOUT") || strings.Contains(r.Content, "TOOL_INTERRUPTED") {
		return OutcomeTimeout
	}
	return OutcomeError
}

// isVaultDelegated returns true when the named tool requires vault execution
// (non-empty RequiredCredentialTypes). Used to populate the vault_delegated
// field in skill_invocation audit events.
func isVaultDelegated(tools []SkillTool, name string) bool {
	for _, t := range tools {
		if t.Definition.Name == name {
			return len(t.RequiredCredentialTypes) > 0
		}
	}
	return false
}

// drillDownDepth returns the drill-down depth for a tool invocation.
//
// Pinned tools (task_complete, spawn_agent) are at "command" depth because
// they are always available without spec lookup. All other domain tools are
// at "spec" depth because their full InputSchema was loaded at startup.
func drillDownDepth(toolName string) string {
	if pinnedTools[toolName] {
		return DepthCommand
	}
	return DepthSpec
}
