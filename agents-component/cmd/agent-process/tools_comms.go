// Package main — tools_comms.go implements the "comms" skill domain tools.
//
// The comms domain provides:
//   - comms_format: local message template rendering (no credentials).
//   - vault_comms_send: authenticated message delivery via Vault.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/pkg/types"
)

// commsFormatTool renders a message template by substituting {{variable}} placeholders.
// No credentials are required — all data is passed as parameters.
func commsFormatTool() SkillTool {
	return SkillTool{
		Label:                   "Comms Format",
		RequiredCredentialTypes: nil,
		TimeoutSeconds:          5,
		Definition: anthropic.ToolParam{
			Name: "comms_format",
			Description: anthropic.String(
				"Render a message template by replacing {{variable}} placeholders with supplied values. " +
					"Use to compose notification or report bodies before sending. " +
					"Do NOT use this tool to deliver the message — call vault_comms_send for sending."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"template": map[string]interface{}{
						"type":        "string",
						"description": "Message template string. Use {{variable_name}} syntax for placeholders (e.g. \"Hello, {{name}}!\").",
					},
					"variables": map[string]interface{}{
						"type":        "object",
						"description": "Map of placeholder names to string substitution values. Each key corresponds to a {{key}} in the template.",
						"additionalProperties": map[string]interface{}{
							"type": "string",
						},
					},
				},
				Required: []string{"template"},
			},
		},
		Execute: executeCommsFormat,
	}
}

func executeCommsFormat(_ context.Context, raw json.RawMessage) ToolResult {
	var params struct {
		Template  string            `json:"template"`
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	result := params.Template
	for k, v := range params.Variables {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}

	// Warn (but do not error) when unresolved placeholders remain.
	details := map[string]interface{}{"substitutions": len(params.Variables)}
	if strings.Contains(result, "{{") {
		details["warning"] = "rendered message may contain unresolved {{placeholders}} — verify variables map"
	}

	return ToolResult{
		Content: result,
		Details: details,
	}
}

// vaultCommsSendTool delivers a formatted message through an authenticated channel via the Vault.
// TimeoutSeconds = 35: vault deadline = 30s + 5s buffer.
func vaultCommsSendTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Vault Comms Send",
		RequiredCredentialTypes: []string{"comms_api_key"},
		TimeoutSeconds:          35,
		Definition: anthropic.ToolParam{
			Name: "vault_comms_send",
			Description: anthropic.String(
				"Send a formatted message to an authenticated channel (email, Slack, webhook) via the Vault. " +
					"Compose the message first with comms_format. " +
					"Do NOT use for channels that require no credentials. " +
					"Do NOT include credential values in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"channel": map[string]interface{}{
						"type":        "string",
						"description": "Logical channel identifier configured in the Vault (e.g. \"ops-alerts\", \"customer-support\", \"finance-reports\"). Do NOT pass raw URLs, API keys, or webhook URLs.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "The fully-rendered message body to send. Use comms_format to render templates before calling this tool.",
					},
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Subject line for channels that support it (e.g. email). Omit for channels that do not use subjects (e.g. Slack).",
					},
				},
				Required: []string{"channel", "message"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultCommsSend(ctx, ve, raw)
		},
	}
}

func executeVaultCommsSend(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Channel string `json:"channel"`
		Message string `json:"message"`
		Subject string `json:"subject,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{
			Content: fmt.Sprintf("invalid parameters: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{
			Content: fmt.Sprintf("failed to encode operation params: %v", err),
			IsError: true,
			Details: map[string]interface{}{"error": err.Error()},
		}
	}

	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault comms_send: progress",
			"request_id", p.RequestID,
			"progress_type", p.ProgressType,
			"message", p.Message,
			"elapsed_ms", p.ElapsedMS,
		)
	}

	return ve.Execute(ctx, "comms_send", "comms_api_key", opParams, 30, onUpdate)
}
