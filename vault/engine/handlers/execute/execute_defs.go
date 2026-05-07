package execute

// ExecuteRequest is the JSON body for POST /execute.
// The vault looks up the credential for user_id+credential_type, runs the
// operation, and returns only the result — never the raw credential value.
type ExecuteRequest struct {
	RequestID       string         `json:"request_id"`
	AgentID         string         `json:"agent_id"`
	TaskID          string         `json:"task_id"`
	UserID          string         `json:"user_id"`
	PermissionToken string         `json:"permission_token"`
	OperationType   string         `json:"operation_type"`
	OperationParams map[string]any `json:"operation_params"`
	CredentialType  string         `json:"credential_type"`
	TimeoutSeconds  int            `json:"timeout_seconds"`
}

// ExecuteResponse is the JSON body for a successful POST /execute.
type ExecuteResponse struct {
	RequestID       string         `json:"request_id"`
	AgentID         string         `json:"agent_id"`
	Status          string         `json:"status"`
	OperationResult map[string]any `json:"operation_result,omitempty"`
	ErrorCode       string         `json:"error_code,omitempty"`
	ErrorMessage    string         `json:"error_message,omitempty"`
	ElapsedMS       int64          `json:"elapsed_ms"`
}

// Status values for ExecuteResponse.
const (
	StatusSuccess        = "success"
	StatusScopeViolation = "scope_violation"
	StatusExecutionError = "execution_error"
	StatusTimedOut       = "timed_out"
)

// Error codes for ExecuteResponse.
const (
	ErrCodeMissingCredential  = "MISSING_CREDENTIAL"
	ErrCodeUnsupportedOp      = "UNSUPPORTED_OPERATION"
	ErrCodeInvalidParams      = "INVALID_PARAMS"
	ErrCodeUpstreamError      = "UPSTREAM_ERROR"
)
