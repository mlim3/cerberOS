// Package main — tools_e2e_test.go implements the e2e_ping built-in tool.
//
// e2e_ping is a no-op fixture used exclusively by the agent skill search
// delegation e2e test. It proves the skills_search → spawn_agent → tool
// execution path works end-to-end without making any external calls.
//
// The e2e_test domain is intentionally absent from the orchestrator planner's
// domain guide, so the planner always assigns tasks to "general". The general
// agent must discover e2e_ping via skills_search and then call spawn_agent to
// run it — exercising the full delegation path.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

func e2ePingTool() SkillTool {
	return SkillTool{
		Label:                   "E2E Test Ping",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          5,
		Definition: anthropic.ToolParam{
			Name: "e2e_ping",
			Description: anthropic.String(
				"Automated e2e connectivity probe for cross-domain skill discovery and delegation validation. " +
					"Echoes a probe string to confirm the e2e_test domain is reachable via skills_search and spawn_agent. " +
					"Do NOT use outside automated e2e testing."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"probe": map[string]interface{}{
						"type":        "string",
						"description": `Probe identifier echoed back in the response (e.g. "skill-search-delegation"). Used to correlate test assertions.`,
					},
				},
				Required: []string{"probe"},
			},
		},
		Execute: func(_ context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				Probe string `json:"probe"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("e2e_ping: invalid parameters: %v", err), IsError: true}
			}
			if params.Probe == "" {
				return ToolResult{Content: "e2e_ping: probe parameter is required", IsError: true}
			}
			ts := time.Now().UnixMilli()
			slog.Default().Info("e2e_ping: executed", "probe", params.Probe, "ts", ts)
			return ToolResult{
				Content: fmt.Sprintf("e2e_ping: OK probe=%s ts=%d", params.Probe, ts),
				Details: map[string]interface{}{
					"probe":  params.Probe,
					"status": "ok",
				},
			}
		},
	}
}
