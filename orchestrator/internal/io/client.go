// Package io implements the orchestrator's outbound client to the IO Component.
//
// The IO Component exposes an HTTP endpoint at POST /api/orchestrator/stream-events
// that accepts status updates and credential requests. The orchestrator pushes events
// here so the web dashboard can display real-time task progress to the user.
//
// When IO_API_BASE is not set, all methods are no-ops — the orchestrator runs
// normally without a connected UI.
//
// Reference: io/api/src/index.ts — POST /api/orchestrator/stream-events
package io

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
)

// TaskStatus values match the IO component's TaskStatus type.
const (
	StatusWorking          = "working"
	StatusAwaitingFeedback = "awaiting_feedback"
	StatusCompleted        = "completed"
)

// streamEventsPath is the IO endpoint that accepts orchestrator-pushed events.
const streamEventsPath = "/api/orchestrator/stream-events"

// statusEvent is the envelope for a status update pushed to the IO Component.
// Matches: { type: 'status', payload: StatusUpdate } in io/core/src/types.ts
type statusEvent struct {
	Type    string        `json:"type"`
	Payload statusPayload `json:"payload"`
}

type statusPayload struct {
	TaskID                   string `json:"taskId"`
	Status                   string `json:"status"`
	LastUpdate               string `json:"lastUpdate"`
	ExpectedNextInputMinutes *int   `json:"expectedNextInputMinutes"`
	Timestamp                int64  `json:"timestamp"` // Unix ms
}

// credentialRequestEvent is the envelope for a credential request pushed to IO.
// Matches: { type: 'credential_request', payload: CredentialRequest } in io/core/src/types.ts
type credentialRequestEvent struct {
	Type    string                   `json:"type"`
	Payload CredentialRequestPayload `json:"payload"`
}

// planPreviewEvent is the envelope for a plan preview pushed to IO.
// Matches: { type: 'plan_preview', payload: PlanPreview } in io/core/src/types.ts.
type planPreviewEvent struct {
	Type    string             `json:"type"`
	Payload PlanPreviewPayload `json:"payload"`
}

// PlanPreviewSubtask is a summary of a single subtask shown to the user while
// they decide whether to approve the plan. Only information relevant for the
// human decision is exposed (no internal IDs beyond SubtaskID).
type PlanPreviewSubtask struct {
	SubtaskID    string   `json:"subtaskId"`
	Action       string   `json:"action"`
	Instructions string   `json:"instructions"`
	DependsOn    []string `json:"dependsOn"`
	Domains      []string `json:"domains"`
}

// PlanPreviewPayload is the data shown on the IO dashboard when the user
// must approve/reject a plan.
type PlanPreviewPayload struct {
	TaskID              string               `json:"taskId"`
	OrchestratorTaskRef string               `json:"orchestratorTaskRef"`
	PlanID              string               `json:"planId"`
	Subtasks            []PlanPreviewSubtask `json:"subtasks"`
	ExpiresInSeconds    int                  `json:"expiresInSeconds"`
}

// CredentialRequestPayload is the data the orchestrator sends when it needs
// the user to supply a secret (e.g. API key, password) via the IO dashboard.
type CredentialRequestPayload struct {
	TaskID      string `json:"taskId"`
	RequestID   string `json:"requestId"`
	UserID      string `json:"userId"`
	KeyName     string `json:"keyName"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Client pushes events to the IO Component over HTTP.
// All methods are safe to call concurrently.
type Client struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

// New returns a new Client targeting baseURL (e.g. "http://localhost:3001").
// When baseURL is empty, the client is disabled and all methods are no-ops.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		logger: observability.LoggerWithModule("io-client"),
	}
}

// Disabled returns true when no IO_API_BASE was configured.
func (c *Client) Disabled() bool {
	return c.baseURL == ""
}

// PushStatus sends a task status update to the IO Component.
// expectedNextInputMinutes is the number of minutes until the next user input is
// expected: 0 = now, nil = unknown/done.
// traceID is propagated on the traceparent header so IO logs match orchestrator NATS traces.
func (c *Client) PushStatus(taskID, status, lastUpdate string, expectedNextInputMinutes *int, traceID string) error {
	if c.Disabled() {
		return nil
	}

	evt := statusEvent{
		Type: "status",
		Payload: statusPayload{
			TaskID:                   taskID,
			Status:                   status,
			LastUpdate:               lastUpdate,
			ExpectedNextInputMinutes: expectedNextInputMinutes,
			Timestamp:                time.Now().UnixMilli(),
		},
	}
	return c.post(evt, traceID)
}

// PushPlanPreview asks the IO Component to show the user a plan preview card
// with Approve/Reject buttons. IO forwards the user's decision back to the
// orchestrator on the `aegis.orchestrator.plan.decision` NATS subject.
func (c *Client) PushPlanPreview(payload PlanPreviewPayload) error {
	if c.Disabled() {
		return nil
	}
	return c.post(planPreviewEvent{Type: "plan_preview", Payload: payload}, "")
}

// PushCredentialRequest asks the IO Component to surface a credential-input
// modal to the user. The user's submitted value goes directly to the Memory
// vault — the orchestrator never sees the plaintext.
func (c *Client) PushCredentialRequest(req CredentialRequestPayload, traceID string) error {
	if c.Disabled() {
		return nil
	}
	evt := credentialRequestEvent{
		Type:    "credential_request",
		Payload: req,
	}
	return c.post(evt, traceID)
}

// post marshals body as JSON and POSTs it to the IO stream-events endpoint.
func (c *Client) post(body any, traceID string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("io-client: marshal event: %w", err)
	}

	url := c.baseURL + streamEventsPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("io-client: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if tp := observability.TraceparentForOutgoingHTTP(traceID); tp != "" {
		req.Header.Set("traceparent", tp)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// IO may be down — log but do not return an error that would fail the task.
		c.logger.Warn("IO post failed", "task_id", taskIDFromEvent(body), "url", url, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.Warn("IO post returned non-OK", "task_id", taskIDFromEvent(body), "url", url, "status", resp.StatusCode)
	}
	return nil
}

func taskIDFromEvent(body any) string {
	switch evt := body.(type) {
	case statusEvent:
		return evt.Payload.TaskID
	case planPreviewEvent:
		return evt.Payload.TaskID
	case credentialRequestEvent:
		return evt.Payload.TaskID
	default:
		return ""
	}
}
