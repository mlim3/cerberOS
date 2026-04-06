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
	"log"
	"net/http"
	"os"
	"time"
)

// TaskStatus values match the IO component's TaskStatus type.
const (
	StatusWorking         = "working"
	StatusAwaitingFeedback = "awaiting_feedback"
	StatusCompleted       = "completed"
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
	TaskID                   string  `json:"taskId"`
	Status                   string  `json:"status"`
	LastUpdate               string  `json:"lastUpdate"`
	ExpectedNextInputMinutes *int    `json:"expectedNextInputMinutes"`
	Timestamp                int64   `json:"timestamp"` // Unix ms
}

// credentialRequestEvent is the envelope for a credential request pushed to IO.
// Matches: { type: 'credential_request', payload: CredentialRequest } in io/core/src/types.ts
type credentialRequestEvent struct {
	Type    string                    `json:"type"`
	Payload CredentialRequestPayload  `json:"payload"`
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
	logger     *log.Logger
}

// New returns a new Client targeting baseURL (e.g. "http://localhost:3001").
// When baseURL is empty, the client is disabled and all methods are no-ops.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		logger: log.New(os.Stdout, "[io-client] ", log.LstdFlags|log.LUTC),
	}
}

// Disabled returns true when no IO_API_BASE was configured.
func (c *Client) Disabled() bool {
	return c.baseURL == ""
}

// PushStatus sends a task status update to the IO Component.
// expectedNextInputMinutes is the number of minutes until the next user input is
// expected: 0 = now, nil = unknown/done.
func (c *Client) PushStatus(taskID, status, lastUpdate string, expectedNextInputMinutes *int) error {
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
	return c.post(evt)
}

// PushCredentialRequest asks the IO Component to surface a credential-input
// modal to the user. The user's submitted value goes directly to the Memory
// vault — the orchestrator never sees the plaintext.
func (c *Client) PushCredentialRequest(req CredentialRequestPayload) error {
	if c.Disabled() {
		return nil
	}
	evt := credentialRequestEvent{
		Type:    "credential_request",
		Payload: req,
	}
	return c.post(evt)
}

// post marshals body as JSON and POSTs it to the IO stream-events endpoint.
func (c *Client) post(body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("io-client: marshal event: %w", err)
	}

	url := c.baseURL + streamEventsPath
	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		// IO may be down — log but do not return an error that would fail the task.
		c.logger.Printf("POST %s failed (IO may be down): %v", url, err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.Printf("POST %s returned %d", url, resp.StatusCode)
	}
	return nil
}
