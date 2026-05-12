// Package main — tools_google.go implements the google_workspace skill domain.
//
// # App-Password tools (no OAuth, no Cloud Console)
//
// gmail_send and calendar_create_event use Gmail SMTP + App Password. The
// credential at users/<id>/credentials/gmail_app_password must be:
//
//	{"email": "demo@gmail.com", "app_password": "abcd1234efgh5678"}
//
// # OAuth tools (read access via Google API)
//
// gmail_search, gmail_get_message, and calendar_list_events use the Google
// API with per-user OAuth tokens. The credential at
// users/<id>/credentials/google_oauth must be:
//
//	{"email": "user@gmail.com", "access_token": "ya29.xxx",
//	 "refresh_token": "1//xxx", "expires_at": "2026-05-09T15:00:00Z"}
//
// Manager configures it via the Admin UI → "Google Workspace (OAuth)" section.
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

// ── OAuth-based read tools ────────────────────────────────────────────────────

// vaultGmailSearchTool searches Gmail messages using the Gmail API with OAuth.
func vaultGmailSearchTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Gmail Search",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "gmail_search",
			Description: anthropic.String(
				"Search Gmail messages using the Gmail API with OAuth. Returns message IDs, senders, subjects, and snippets. " +
					"Use for finding emails by sender, subject, or keyword. " +
					"Do NOT use for sending email — use gmail_send. " +
					"Do NOT include credentials in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Gmail search query. Supports full Gmail syntax (e.g. \"from:boss budget\", \"subject:invoice is:unread\").",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of messages to return (default 10, max 50).",
					},
				},
				Required: []string{"query"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				Query      string `json:"query"`
				MaxResults *int   `json:"max_results,omitempty"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			if params.Query == "" {
				return ToolResult{Content: "query is required", IsError: true}
			}
			opParams, err := json.Marshal(params)
			if err != nil {
				return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
			}
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault gmail_search progress",
					"request_id", p.RequestID,
					"message_preview", logfields.PreviewWords(p.Message, 20, 140))
			}
			return ve.Execute(ctx, "vault_gmail_search", "google_oauth", opParams, 28, onUpdate)
		},
	}
}

// vaultGmailGetMessageTool fetches the full body of a Gmail message by ID.
func vaultGmailGetMessageTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Gmail Get Message",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "gmail_get_message",
			Description: anthropic.String(
				"Fetch the full body, headers, and attachment names of a Gmail message by its ID. " +
					"Use after gmail_search to read message content. " +
					"Do NOT use for searching — use gmail_search first. " +
					"Do NOT include credentials in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"message_id": map[string]interface{}{
						"type":        "string",
						"description": "Gmail message ID as returned by gmail_search (the \"id\" field in each result).",
					},
				},
				Required: []string{"message_id"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			if params.MessageID == "" {
				return ToolResult{Content: "message_id is required", IsError: true}
			}
			opParams, err := json.Marshal(params)
			if err != nil {
				return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
			}
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault gmail_get_message progress",
					"request_id", p.RequestID,
					"message_preview", logfields.PreviewWords(p.Message, 20, 140))
			}
			return ve.Execute(ctx, "vault_gmail_get_message", "google_oauth", opParams, 28, onUpdate)
		},
	}
}

// vaultCalendarListEventsTool lists upcoming Google Calendar events via OAuth.
func vaultCalendarListEventsTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Calendar List Events",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "calendar_list_events",
			Description: anthropic.String(
				"List upcoming Google Calendar events via the Calendar API with OAuth. " +
					"Returns event titles, start/end times, descriptions, and attendees. " +
					"Use for reading the user's calendar schedule. " +
					"Do NOT use for creating events — use calendar_create_event. " +
					"Do NOT include credentials in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of events to return (default 10, max 50).",
					},
					"time_min": map[string]interface{}{
						"type":        "string",
						"description": "Start of time range as ISO 8601 datetime (e.g. \"2026-05-09T00:00:00Z\"). Defaults to now.",
					},
					"time_max": map[string]interface{}{
						"type":        "string",
						"description": "End of time range as ISO 8601 datetime. Defaults to 7 days from now.",
					},
					"calendar_id": map[string]interface{}{
						"type":        "string",
						"description": "Calendar ID to query. Omit or pass \"primary\" for the user's primary calendar.",
					},
				},
				Required: []string{},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				MaxResults *int   `json:"max_results,omitempty"`
				TimeMin    string `json:"time_min,omitempty"`
				TimeMax    string `json:"time_max,omitempty"`
				CalendarID string `json:"calendar_id,omitempty"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, err := json.Marshal(params)
			if err != nil {
				return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
			}
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault calendar_list_events progress",
					"request_id", p.RequestID,
					"message_preview", logfields.PreviewWords(p.Message, 20, 140))
			}
			return ve.Execute(ctx, "vault_calendar_list_events", "google_oauth", opParams, 28, onUpdate)
		},
	}
}

func vaultGmailSendOAuthTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Gmail Send (OAuth)",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          40,
		Definition: anthropic.ToolParam{
			Name: "gmail_send",
			Description: anthropic.String(
				"Send a plain-text email via the Gmail API using the user's connected Google account. " +
					"Use whenever the user asks to send an email to a specific address. " +
					"Do NOT use for calendar invites — use calendar_create_event for that. " +
					"Do NOT include credentials in any parameter."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"to": map[string]interface{}{
						"type":        "string",
						"description": "Recipient email address.",
					},
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Email subject line.",
					},
					"body": map[string]interface{}{
						"type":        "string",
						"description": "Plain-text email body.",
					},
				},
				Required: []string{"to", "subject", "body"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				To      string `json:"to"`
				Subject string `json:"subject"`
				Body    string `json:"body"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, err := json.Marshal(params)
			if err != nil {
				return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
			}
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault gmail_send_oauth progress", "request_id", p.RequestID)
			}
			return ve.Execute(ctx, "vault_gmail_send_oauth", "google_oauth", opParams, 38, onUpdate)
		},
	}
}

func vaultCalendarCreateEventTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Calendar Create Event (OAuth)",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          40,
		Definition: anthropic.ToolParam{
			Name: "calendar_create_event",
			Description: anthropic.String(
				"Create a Google Calendar event (OAuth). Before booking a specific time, " +
					"call calendar_freebusy; if conflicts, ask (a) another time or (b) " +
					"double-book. If finding a free slot, call calendar_find_free_slot " +
					"then create without asking. Emails use gmail_send, not this."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"title": map[string]interface{}{
						"type":        "string",
						"description": "Event title / summary.",
					},
					"start": map[string]interface{}{
						"type":        "string",
						"description": "ISO 8601 start datetime (e.g. '2026-05-12T17:00:00-07:00').",
					},
					"end": map[string]interface{}{
						"type":        "string",
						"description": "ISO 8601 end datetime, same format as start.",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Optional event description.",
					},
					"location": map[string]interface{}{
						"type":        "string",
						"description": "Optional location (address or video link).",
					},
					"calendar_id": map[string]interface{}{
						"type":        "string",
						"description": "Calendar ID. Omit or use 'primary' for the user's main calendar.",
					},
				},
				Required: []string{"title", "start", "end"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				Title       string `json:"title"`
				Start       string `json:"start"`
				End         string `json:"end"`
				Description string `json:"description,omitempty"`
				Location    string `json:"location,omitempty"`
				CalendarID  string `json:"calendar_id,omitempty"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, err := json.Marshal(params)
			if err != nil {
				return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
			}
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault calendar_create_event progress", "request_id", p.RequestID)
			}
			return ve.Execute(ctx, "vault_calendar_create_event", "google_oauth", opParams, 38, onUpdate)
		},
	}
}

func vaultCalendarFreeBusyTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Calendar Free/Busy",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "calendar_freebusy",
			Description: anthropic.String(
				"Check the user's Google Calendar for busy intervals in a window. " +
					"Call before calendar_create_event to surface conflicts. Returns busy " +
					"intervals and a 'free' boolean. Do NOT use to list event details — " +
					"use calendar_list_events for that. Do NOT include credentials."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"time_min": map[string]interface{}{
						"type":        "string",
						"description": "ISO 8601 start of the window to check (e.g. '2026-05-12T13:00:00-07:00').",
					},
					"time_max": map[string]interface{}{
						"type":        "string",
						"description": "ISO 8601 end of the window. Same format as time_min.",
					},
					"calendar_id": map[string]interface{}{
						"type":        "string",
						"description": "Calendar ID. Omit or use 'primary' for the user's main calendar.",
					},
				},
				Required: []string{"time_min", "time_max"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params struct {
				TimeMin    string `json:"time_min"`
				TimeMax    string `json:"time_max"`
				CalendarID string `json:"calendar_id,omitempty"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, err := json.Marshal(params)
			if err != nil {
				return ToolResult{Content: fmt.Sprintf("failed to encode params: %v", err), IsError: true}
			}
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault calendar_freebusy progress", "request_id", p.RequestID)
			}
			return ve.Execute(ctx, "vault_calendar_freebusy", "google_oauth", opParams, 28, onUpdate)
		},
	}
}

func vaultCalendarUpdateEventTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Calendar Update Event",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          40,
		Definition: anthropic.ToolParam{
			Name: "calendar_update_event",
			Description: anthropic.String(
				"Move or edit an existing Google Calendar event by ID. Use to reschedule, rename, " +
					"or update description/location. When changing start/end, the result includes " +
					"'conflicts' if the new slot is double-booked. Do NOT use to create a new event — " +
					"use calendar_create_event."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"event_id": map[string]interface{}{
						"type":        "string",
						"description": "The Google Calendar event ID (returned by calendar_create_event or calendar_list_events).",
					},
					"calendar_id": map[string]interface{}{"type": "string", "description": "Calendar ID. Omit or use 'primary'."},
					"title":       map[string]interface{}{"type": "string", "description": "New event title. Omit to keep current."},
					"start":       map[string]interface{}{"type": "string", "description": "New ISO 8601 start datetime with TZ offset. Omit to keep current."},
					"end":         map[string]interface{}{"type": "string", "description": "New ISO 8601 end datetime. Omit to keep current."},
					"description": map[string]interface{}{"type": "string", "description": "New description. Omit to keep current."},
					"location":    map[string]interface{}{"type": "string", "description": "New location. Omit to keep current."},
				},
				Required: []string{"event_id"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params map[string]any
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, _ := json.Marshal(params)
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault calendar_update_event progress", "request_id", p.RequestID)
			}
			return ve.Execute(ctx, "vault_calendar_update_event", "google_oauth", opParams, 38, onUpdate)
		},
	}
}

func vaultCalendarFindFreeSlotTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Calendar Find Free Slot",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          30,
		Definition: anthropic.ToolParam{
			Name: "calendar_find_free_slot",
			Description: anthropic.String(
				"Find the first contiguous free slot of N minutes within a time window. Use to " +
					"answer 'when am I next free for X minutes' or to pick a meeting time. Returns " +
					"{found, start, end} on success. Do NOT use to list events — use " +
					"calendar_list_events for that."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"duration_minutes":     map[string]interface{}{"type": "integer", "description": "Slot length in minutes."},
					"time_min":             map[string]interface{}{"type": "string", "description": "ISO 8601 window start (e.g. '2026-05-12T09:00:00-07:00')."},
					"time_max":             map[string]interface{}{"type": "string", "description": "ISO 8601 window end."},
					"calendar_id":          map[string]interface{}{"type": "string", "description": "Calendar ID. Omit or use 'primary'."},
					"working_hours_start":  map[string]interface{}{"type": "string", "description": "Optional 'HH:MM' lower bound per day (e.g. '09:00')."},
					"working_hours_end":    map[string]interface{}{"type": "string", "description": "Optional 'HH:MM' upper bound per day (e.g. '17:00')."},
				},
				Required: []string{"duration_minutes", "time_min", "time_max"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params map[string]any
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, _ := json.Marshal(params)
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault calendar_find_free_slot progress", "request_id", p.RequestID)
			}
			return ve.Execute(ctx, "vault_calendar_find_free_slot", "google_oauth", opParams, 28, onUpdate)
		},
	}
}

func vaultGmailWaitForRepliesTool(ve *VaultExecutor) SkillTool {
	return SkillTool{
		Label:                   "Gmail Wait For Replies",
		RequiredCredentialTypes: []string{"google_oauth"},
		TimeoutSeconds:          290,
		Definition: anthropic.ToolParam{
			Name: "gmail_wait_for_replies",
			Description: anthropic.String(
				"Poll Gmail for replies matching given criteria, returning when min_count are " +
					"found or the wait deadline elapses. Single call waits up to 5 minutes; for " +
					"longer deadlines (e.g. 1 hour), call repeatedly with the same 'since' " +
					"timestamp and accumulate elapsed time. Returns {found, count, messages, timed_out}."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]interface{}{
					"since":                  map[string]interface{}{"type": "string", "description": "RFC3339 timestamp — only replies AFTER this are counted. Required."},
					"subject_contains":       map[string]interface{}{"type": "string", "description": "Optional substring to match in subject (e.g. 'Re: Design review')."},
					"from":                   map[string]interface{}{"type": "string", "description": "Optional sender filter (email address or display name)."},
					"min_count":              map[string]interface{}{"type": "integer", "description": "How many matching replies to wait for. Default 1."},
					"poll_interval_seconds":  map[string]interface{}{"type": "integer", "description": "Seconds between polls. Default 30."},
					"max_wait_seconds":       map[string]interface{}{"type": "integer", "description": "Maximum seconds to wait this call. Capped at 270."},
				},
				Required: []string{"since"},
			},
		},
		Execute: func(ctx context.Context, raw json.RawMessage) ToolResult {
			var params map[string]any
			if err := json.Unmarshal(raw, &params); err != nil {
				return ToolResult{Content: fmt.Sprintf("invalid parameters: %v", err), IsError: true}
			}
			opParams, _ := json.Marshal(params)
			onUpdate := func(p types.VaultOperationProgress) {
				ve.log.Info("vault gmail_wait_for_replies progress", "request_id", p.RequestID)
			}
			return ve.Execute(ctx, "vault_gmail_wait_for_replies", "google_oauth", opParams, 285, onUpdate)
		},
	}
}
