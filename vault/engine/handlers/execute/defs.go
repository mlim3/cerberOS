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

// OperationRequest mirrors types.VaultOperationRequest from the agents-component.
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
type OperationResult struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	Status          string          `json:"status"`
	OperationResult json.RawMessage `json:"operation_result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	ElapsedMS       int64           `json:"elapsed_ms"`
}
