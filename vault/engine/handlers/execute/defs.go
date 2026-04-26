package execute

import "encoding/json"

// Supported operation_type values routed by the execute handler.
const (
	OperationTypeWebSearch = "web_search"
)

// Credential type constants map to secret key names stored in OpenBao.
const (
	CredentialTypeSearchAPIKey = "search_api_key"

	// SecretKeyTavily is the OpenBao key name used to store the Tavily API key.
	SecretKeyTavily = "TAVILY_API_KEY"
)

// Status values for OperationResult.
const (
	StatusSuccess        = "success"
	StatusTimedOut       = "timed_out"
	StatusScopeViolation = "scope_violation"
	StatusExecError      = "execution_error"
)

// OperationRequest mirrors types.VaultOperationRequest from the agents-component.
// The vault engine receives this from the orchestrator via POST /execute and uses
// it to route and execute a credentialed operation.
//
// PermissionToken is validated for presence but its value is opaque to the vault
// engine — scope enforcement is performed by the Orchestrator before routing here.
// The raw credential value is resolved internally from SecretManager and must
// never be returned to the caller.
type OperationRequest struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	TaskID          string          `json:"task_id"`
	PermissionToken string          `json:"permission_token"`
	OperationType   string          `json:"operation_type"`
	OperationParams json.RawMessage `json:"operation_params"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	CredentialType  string          `json:"credential_type"`
}

// OperationResult mirrors types.VaultOperationResult from the agents-component.
// OperationResult carries only the operation output — the credential used to
// perform the operation is never included in any field of this struct.
type OperationResult struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	Status          string          `json:"status"`
	OperationResult json.RawMessage `json:"operation_result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"` // user-safe; must not expose vault paths or key values
	ElapsedMS       int64           `json:"elapsed_ms"`
}
