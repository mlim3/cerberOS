// Package main — tools.go implements the skill tool registry and tool contract.
//
// Every skill satisfies the tool contract from EDD §13.2:
//   - Definition: the Anthropic ToolParam passed to the API.
//   - Execute:    runs the skill and returns ToolResult.
//
// ToolResult enforces the content/details split:
//   - Content (≤16KB) enters the LLM context.
//   - Details go to monitoring (stderr) only and never enter agent context.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

const (
	// maxContentBytes is the maximum content size returned to the LLM (§13.2).
	maxContentBytes = 16 * 1024

	// toolNameTaskComplete is the reserved name for the task completion signal.
	toolNameTaskComplete = "task_complete"
)

// ToolResult is the output of a skill execution. It implements the tool contract
// from EDD §13.2: Content enters the LLM context; Details are for monitoring only
// and are never injected into agent context.
type ToolResult struct {
	Content string                 // what the LLM sees; max 16KB
	IsError bool                   // signals the LLM that execution failed
	Details map[string]interface{} // monitoring only — logged to stderr, never to LLM
}

// SkillTool pairs an Anthropic tool definition with its execute function.
type SkillTool struct {
	Definition anthropic.ToolParam
	Execute    func(input json.RawMessage) ToolResult
}

// toolsForDomain returns the skill tools available for the given domain.
// In M3 this will query the Skill Hierarchy Manager via the Orchestrator.
func toolsForDomain(domain string) []SkillTool {
	base := []SkillTool{taskCompleteTool()}
	switch domain {
	case "web":
		return append([]SkillTool{webFetchTool()}, base...)
	default:
		return base
	}
}

// toolDefinitions wraps each SkillTool definition into the ToolUnionParam
// expected by MessageNewParams.Tools.
func toolDefinitions(tools []SkillTool) []anthropic.ToolUnionParam {
	params := make([]anthropic.ToolUnionParam, len(tools))
	for i := range tools {
		def := tools[i].Definition // copy per iteration — each &def is distinct
		params[i] = anthropic.ToolUnionParam{OfTool: &def}
	}
	return params
}

// dispatchTool finds and executes the named tool.
// Unknown tool names return an error result so the LLM can correct itself.
func dispatchTool(tools []SkillTool, name string, input json.RawMessage) ToolResult {
	for _, t := range tools {
		if t.Definition.Name == name {
			return t.Execute(input)
		}
	}
	return ToolResult{
		Content: fmt.Sprintf("unknown tool %q — this tool is not available in the current skill domain", name),
		IsError: true,
		Details: map[string]interface{}{"error": "tool not registered", "tool": name},
	}
}

// webFetchTool fetches a URL via HTTP. No credentials required — for
// authenticated operations the agent must use vault execution (M3).
func webFetchTool() SkillTool {
	return SkillTool{
		Definition: anthropic.ToolParam{
			Name: "web_fetch",
			Description: anthropic.String(
				"Fetch the content of a URL via HTTP GET or POST. " +
					"Use for web pages, REST APIs, or any HTTP resource that does not require authentication. " +
					"Do NOT use for operations requiring authentication — those require vault execution."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "The fully-qualified URL to fetch (must include scheme: https:// or http://).",
					},
					"method": map[string]interface{}{
						"type":        "string",
						"description": "HTTP method. Allowed: GET, POST. Default: GET.",
						"enum":        []string{"GET", "POST"},
					},
				},
				Required: []string{"url"},
			},
		},
		Execute: executeWebFetch,
	}
}

func executeWebFetch(raw json.RawMessage) ToolResult {
	var params struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}
	if params.Method == "" {
		params.Method = "GET"
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(params.Method, params.URL, nil)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to build request: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}
	req.Header.Set("User-Agent", "aegis-agent/1.0")

	start := time.Now()
	resp, err := httpClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("HTTP request failed: %v", err),
			IsError: true,
			Details: map[string]interface{}{
				"error":      err.Error(),
				"url":        params.URL,
				"elapsed_ms": elapsed.Milliseconds(),
			},
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxContentBytes))
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to read response body: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error(), "url": params.URL},
		}
	}

	truncated := len(body) == maxContentBytes
	content := string(body)
	if truncated {
		content += "\n[CONTENT TRUNCATED — response exceeded 16KB limit]"
	}

	return ToolResult{
		Content: fmt.Sprintf("HTTP %d\n\n%s", resp.StatusCode, content),
		IsError: resp.StatusCode >= 400,
		Details: map[string]interface{}{
			"url":         params.URL,
			"status_code": resp.StatusCode,
			"elapsed_ms":  elapsed.Milliseconds(),
			"bytes_read":  len(body),
			"truncated":   truncated,
		},
	}
}

// taskCompleteTool is the agent's explicit signal that it has finished the task.
// When the agent calls this, RunLoop captures the result and exits the loop.
func taskCompleteTool() SkillTool {
	return SkillTool{
		Definition: anthropic.ToolParam{
			Name: toolNameTaskComplete,
			Description: anthropic.String(
				"Signal that the task is complete and return the final result. " +
					"Call this when you have all the information needed to answer the task. " +
					"Do NOT call this if you still have actions to take."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"result": map[string]interface{}{
						"type":        "string",
						"description": "The final task result. Be concise and factual.",
					},
				},
				Required: []string{"result"},
			},
		},
		Execute: func(raw json.RawMessage) ToolResult {
			var params struct {
				Result string `json:"result"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{
					Content: fmt.Sprintf("invalid parameters: %v", err),
					IsError: true,
					Details: map[string]interface{}{"error": err.Error()},
				}
			}
			return ToolResult{
				Content: params.Result,
				IsError: false,
				Details: map[string]interface{}{"final_result": params.Result},
			}
		},
	}
}
