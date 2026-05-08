// Package main — tools_google.go implements the google_workspace skill domain
// using Gmail SMTP via App Password (no OAuth). Both tools route through the
// vault execute pipeline so the credential value is resolved server-side and
// never enters the agent context.
//
// The credential at users/<id>/credentials/gmail_app_password (or the global
// fallback "gmail_app_password" key) must be a JSON object:
//
//	{"email": "demo@gmail.com", "app_password": "abcd1234efgh5678"}
//
// Manager configures it via POST /api/admin/gmail-credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/logfields"
	"github.com/cerberOS/agents-component/pkg/types"
)

// vaultGmailSendTool sends a plain-text email via the manager-configured Gmail
// demo account (SMTP + App Password — no OAuth, no Cloud Console).
func vaultGmailSendTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Gmail Send",
		RequiredCredentialTypes: []string{"gmail_app_password"},
		TimeoutSeconds:          40,
		Definition: anthropic.ToolParam{
			Name: "gmail_send",
			Description: anthropic.String(
				"Send a plain-text email through the manager-configured Gmail account via SMTP. " +
					"Use for delivering summaries, reports, or notifications by email. " +
					"Do NOT use for unauthenticated webhooks — use vault_comms_send. " +
					"Do NOT include the app password in any parameter; it lives in the Vault."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"to": map[string]interface{}{
						"type":        "string",
						"description": "Recipient email address (one address only).",
					},
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Subject line. Will be sanitized of newline characters.",
					},
					"body": map[string]interface{}{
						"type":        "string",
						"description": "Plain-text body content (UTF-8). HTML will not be rendered as HTML.",
					},
				},
				Required: []string{"to", "subject", "body"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultGmailSend(ctx, ve, raw)
		},
	}
}

func executeVaultGmailSend(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}
	if params.To == "" || params.Subject == "" || params.Body == "" {
		return ToolResult{Content: "to, subject, and body are required", IsError: true}
	}
	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
	}
	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault gmail_send progress update from vault engine",
			"request_id", p.RequestID,
			"message_preview", logfields.PreviewWords(p.Message, 20, 140))
	}
	return ve.Execute(ctx, "vault_gmail_send", "gmail_app_password", opParams, 35, onUpdate)
}

// vaultGmailCalendarInviteTool creates a calendar event by sending a Gmail
// message with an iCalendar (.ics) REQUEST attachment. The recipient sees a
// one-click "Add to Calendar" button in Gmail — no Google Calendar API needed.
//
// Default recipient is the demo account itself, so the demo flow is:
//  1. Agent calls calendar_create_event
//  2. Demo Gmail receives an invite
//  3. Click "Yes" → event lands on Google Calendar
func vaultGmailCalendarInviteTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Calendar Invite (Gmail)",
		RequiredCredentialTypes: []string{"gmail_app_password"},
		TimeoutSeconds:          40,
		Definition: anthropic.ToolParam{
			Name: "calendar_create_event",
			Description: anthropic.String(
				"Create a calendar event by sending a Gmail invite (iCalendar attachment). " +
					"Recipient gets a one-click 'Add to Calendar' button in Gmail. " +
					"If 'to' is omitted, the invite goes to the configured demo account. " +
					"Do NOT use for non-event emails — use gmail_send."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "Event title / summary line.",
					},
					"start": map[string]interface{}{
						"type":        "string",
						"description": "ISO 8601 start datetime (e.g. '2026-06-01T10:00:00-07:00' or '2026-06-01T17:00:00Z').",
					},
					"end": map[string]interface{}{
						"type":        "string",
						"description": "ISO 8601 end datetime, in the same format as 'start'.",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Optional event body / agenda text.",
					},
					"location": map[string]interface{}{
						"type":        "string",
						"description": "Optional location string (e.g. address or 'Zoom').",
					},
					"to": map[string]interface{}{
						"type":        "string",
						"description": "Optional recipient. Omit to invite the demo account itself.",
					},
				},
				Required: []string{"title", "start", "end"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			return executeVaultGmailCalendarInvite(ctx, ve, raw)
		},
	}
}

func executeVaultGmailCalendarInvite(ctx context.Context, ve *VaultExecutor, raw json.RawMessage) ToolResult {
	var params struct {
		Title       string `json:"title"`
		Start       string `json:"start"`
		End         string `json:"end"`
		Description string `json:"description,omitempty"`
		Location    string `json:"location,omitempty"`
		To          string `json:"to,omitempty"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
	}
	if params.Title == "" || params.Start == "" || params.End == "" {
		return ToolResult{Content: "title, start, and end are required (ISO 8601 datetimes)", IsError: true}
	}
	opParams, err := json.Marshal(params)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
	}
	onUpdate := func(p types.VaultOperationProgress) {
		ve.log.Info("vault gmail_calendar_invite progress update from vault engine",
			"request_id", p.RequestID,
			"message_preview", logfields.PreviewWords(p.Message, 20, 140))
	}
	return ve.Execute(ctx, "vault_gmail_calendar_invite", "gmail_app_password", opParams, 35, onUpdate)
}
