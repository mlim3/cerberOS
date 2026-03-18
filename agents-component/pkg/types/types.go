package types

import "time"

// TaskSpec is received from the Orchestrator via the Comms Interface.
type TaskSpec struct {
	TaskID         string            `json:"task_id"`
	RequiredSkills []string          `json:"required_skills"` // domain names only
	Metadata       map[string]string `json:"metadata"`
	TraceID        string            `json:"trace_id"`
	UserContextID  string            `json:"user_context_id,omitempty"` // echoed in all outbound events
}

// TaskAccepted is published to aegis.orchestrator.task.accepted immediately on
// task receipt — before any provisioning work begins (EDD §8.3).
// Deadline: within 5 seconds of receiving task.inbound.
type TaskAccepted struct {
	TaskID                string     `json:"task_id"`
	AgentID               string     `json:"agent_id"`
	AgentType             string     `json:"agent_type"`                        // "new_provision" | "existing_assigned"
	EstimatedCompletionAt *time.Time `json:"estimated_completion_at,omitempty"` // ISO 8601; null when unknown
	UserContextID         string     `json:"user_context_id,omitempty"`
	TraceID               string     `json:"trace_id"`
}

// AgentRecord is the catalog entry stored in the Registry.
type AgentRecord struct {
	AgentID       string    `json:"agent_id"`
	State         string    `json:"state"` // idle | active | terminated
	SkillDomains  []string  `json:"skill_domains"`
	PermissionSet []string  `json:"permission_set"`
	AssignedTask  string    `json:"assigned_task,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SkillSpec is the leaf-level parameter schema for a skill command.
type SkillSpec struct {
	Parameters map[string]ParameterDef `json:"parameters"`
}

// ParameterDef describes a single parameter in a skill's call spec.
type ParameterDef struct {
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

// SkillNode is a node in the three-level skill hierarchy (domain → command → spec).
//
// Domain nodes require only Name and Level. Command nodes must satisfy the full
// Tool Contract (EDD §13.2): Label, Description, and all SkillSpec parameters must
// have descriptions. The Skill Hierarchy Manager enforces this at registration time.
type SkillNode struct {
	Name  string `json:"name"`
	Level string `json:"level"` // domain | command | spec

	// Tool Contract fields — required at command level (EDD §13.2).
	Label                   string   `json:"label,omitempty"`                     // human-readable display name; monitoring and audit logs only — never shown to the LLM
	Description             string   `json:"description,omitempty"`               // what the tool does and when NOT to use it; max 300 chars
	RequiredCredentialTypes []string `json:"required_credential_types,omitempty"` // empty = no vault execution needed
	TimeoutSeconds          int      `json:"timeout_seconds,omitempty"`           // 0 = default (30s); hard max 300s

	Children map[string]*SkillNode `json:"children,omitempty"`
	Spec     *SkillSpec            `json:"spec,omitempty"` // only at leaf level
}

// MemoryWrite is the tagged payload sent to the Memory Component.
type MemoryWrite struct {
	AgentID   string            `json:"agent_id"`
	SessionID string            `json:"session_id"`
	DataType  string            `json:"data_type"`
	TTLHint   int               `json:"ttl_hint_seconds"`
	Payload   interface{}       `json:"payload"`
	Tags      map[string]string `json:"tags"`
}

// Envelope is the standard NATS message wrapper for all inter-component messages.
type Envelope struct {
	MessageID   string      `json:"message_id"`
	Source      string      `json:"source"`
	Destination string      `json:"destination"`
	Timestamp   time.Time   `json:"timestamp"`
	Payload     interface{} `json:"payload"`
	TraceID     string      `json:"trace_id"`
}

// TaskResult is published to the Orchestrator on task completion.
type TaskResult struct {
	TaskID  string      `json:"task_id"`
	AgentID string      `json:"agent_id"`
	Success bool        `json:"success"`
	Output  interface{} `json:"output,omitempty"`
	Error   string      `json:"error,omitempty"`
	TraceID string      `json:"trace_id"`
}

// StatusUpdate is published to the Orchestrator for progress events.
type StatusUpdate struct {
	TaskID  string `json:"task_id"`
	AgentID string `json:"agent_id"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	TraceID string `json:"trace_id"`
}

// CapabilityQuery is received from the Orchestrator asking whether an agent
// with the specified skill domains exists.
type CapabilityQuery struct {
	QueryID string   `json:"query_id"`
	Domains []string `json:"domains"`
	TraceID string   `json:"trace_id"`
}

// CapabilityResponse answers an Orchestrator capability query.
type CapabilityResponse struct {
	QueryID  string   `json:"query_id"`
	Domains  []string `json:"domains"`
	HasMatch bool     `json:"has_match"`
	TraceID  string   `json:"trace_id"`
}

// CredentialRequest is sent to the Orchestrator to obtain a scoped credential.
// The Orchestrator proxies this to the Credential Vault and returns a CredentialResponse.
type CredentialRequest struct {
	AgentID       string   `json:"agent_id"`
	PermissionSet []string `json:"permission_set"`
	TraceID       string   `json:"trace_id"`
}

// CredentialResponse is received from the Orchestrator carrying the scoped token
// returned by the Credential Vault.
type CredentialResponse struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
	TraceID string `json:"trace_id"`
}

// MemoryReadRequest is sent to the Orchestrator to retrieve filtered memory context.
// The Orchestrator routes this to the Memory Component.
type MemoryReadRequest struct {
	AgentID    string `json:"agent_id"`
	ContextTag string `json:"context_tag"`
	TraceID    string `json:"trace_id"`
}

// MemoryResponse is received from the Orchestrator carrying records returned
// by the Memory Component in response to a MemoryReadRequest.
type MemoryResponse struct {
	AgentID string        `json:"agent_id"`
	Records []MemoryWrite `json:"records"`
	TraceID string        `json:"trace_id"`
}
