package types

// CredentialRequest is the NATS payload agents publish to request
// pre-authorization, revocation, or user-supplied credentials.
type CredentialRequest struct {
	RequestID    string   `json:"request_id"`
	AgentID      string   `json:"agent_id"`
	TaskID       string   `json:"task_id"`
	Operation    string   `json:"operation"`
	SkillDomains []string `json:"skill_domains,omitempty"`
	TTLSeconds   int      `json:"ttl_seconds,omitempty"`
	TraceID      string   `json:"trace_id,omitempty"`
	KeyName      string   `json:"key_name,omitempty"`
	Label        string   `json:"label,omitempty"`
	Description  string   `json:"description,omitempty"`
}

// CredentialResponse is the NATS payload the orchestrator publishes back to
// agents after handling a credential request.
type CredentialResponse struct {
	RequestID       string `json:"request_id"`
	Status          string `json:"status"`
	PermissionToken string `json:"permission_token,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
}
