package types

import "encoding/json"

// VaultExecuteRequest is the payload agents publish to aegis.orchestrator.vault.execute.request.
// The orchestrator resolves user_id from task state — agents never provide it directly.
type VaultExecuteRequest struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	TaskID          string          `json:"task_id"`
	PermissionToken string          `json:"permission_token"`
	OperationType   string          `json:"operation_type"`
	OperationParams json.RawMessage `json:"operation_params"`
	CredentialType  string          `json:"credential_type"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
}

// VaultExecuteResult is published to aegis.agents.vault.execute.result after the
// Vault runs the operation. operation_result contains the output only — never the
// raw credential value.
type VaultExecuteResult struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	Status          string          `json:"status"`
	OperationResult json.RawMessage `json:"operation_result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	ElapsedMS       int64           `json:"elapsed_ms"`
}

// Vault execute status values — mirror vault engine constants.
const (
	VaultExecStatusSuccess        = "success"
	VaultExecStatusScopeViolation = "scope_violation"
	VaultExecStatusExecutionError = "execution_error"
	VaultExecStatusTimedOut       = "timed_out"
)
