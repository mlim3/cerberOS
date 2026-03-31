package types

import (
	"encoding/json"
	"time"
)

// HeartbeatEvent is published by the agent process on the aegis.heartbeat.<agent_id>
// subject every heartbeat interval. The Lifecycle Manager subscribes to
// aegis.heartbeat.* and uses these events to detect crashed agents.
type HeartbeatEvent struct {
	AgentID   string    `json:"agent_id"`
	TaskID    string    `json:"task_id"`
	TraceID   string    `json:"trace_id"`
	Timestamp time.Time `json:"timestamp"`
}

// TaskSpec is received from the Orchestrator via the Comms Interface.
type TaskSpec struct {
	TaskID         string            `json:"task_id"`
	RequiredSkills []string          `json:"required_skills"` // domain names only
	Instructions   string            `json:"instructions"`    // natural-language task description injected into the agent at spawn
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

// StateEvent is a single entry in an agent's state transition history.
// StateHistory is append-only — entries are never removed or modified.
type StateEvent struct {
	State     string    `json:"state"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

// AgentRecord is the catalog entry stored in the Registry.
type AgentRecord struct {
	AgentID      string `json:"agent_id"`
	VMID         string `json:"vm_id,omitempty"` // ID of the current microVM; changes on respawn; empty when suspended or terminated
	State        string `json:"state"`           // pending | spawning | active | idle | suspended | recovering | terminated
	FailureCount int    `json:"failure_count"`   // increments on each crash (→ recovering); resets to 0 on successful task completion (→ idle)

	SkillDomains  []string     `json:"skill_domains"`
	PermissionSet []string     `json:"permission_set"`
	AssignedTask  string       `json:"assigned_task,omitempty"`
	StateHistory  []StateEvent `json:"state_history"` // append-only ordered log of all state transitions
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
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
// RequestID is set by the Memory Interface on the wire (state.write) and used
// as the correlation key for state.write.ack routing. It is omitted from stored
// records. RequireAck instructs the Orchestrator to publish a state.write.ack
// so the caller can confirm persistence before proceeding.
type MemoryWrite struct {
	AgentID    string            `json:"agent_id"`
	SessionID  string            `json:"session_id"`
	DataType   string            `json:"data_type"`
	TTLHint    int               `json:"ttl_hint_seconds"`
	Payload    interface{}       `json:"payload"`
	Tags       map[string]string `json:"tags"`
	RequestID  string            `json:"request_id,omitempty"`
	RequireAck bool              `json:"require_ack,omitempty"`
}

// StateWriteAck is the confirmation sent by the Orchestrator on
// aegis.agents.state.write.ack in response to a state.write request.
// Status is "accepted" on success or "rejected" when the Memory Component
// refuses the payload (e.g. schema violation). RejectionReason is set when
// Status == "rejected" and must be logged — never silently discarded.
type StateWriteAck struct {
	RequestID       string `json:"request_id,omitempty"`
	AgentID         string `json:"agent_id"`
	Status          string `json:"status"`                     // "accepted" | "rejected" | "ok"
	RejectionReason string `json:"rejection_reason,omitempty"` // present when status == "rejected"
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

// CredentialRequest is sent to the Orchestrator to authorize a scoped permission
// token for an agent at spawn time, or to revoke it at termination. The Orchestrator
// proxies this to the Credential Vault which registers a scoped policy and returns
// an opaque permission_token reference (never a raw credential value).
//
// RequestID is the correlation key echoed in the CredentialResponse envelope's
// correlation_id so the Credential Broker can route the response to the waiting
// PreAuthorize call.
type CredentialRequest struct {
	RequestID    string   `json:"request_id"` // UUID; correlation key for response routing
	AgentID      string   `json:"agent_id"`
	TaskID       string   `json:"task_id"`
	Operation    string   `json:"operation"`     // "authorize" | "revoke"
	SkillDomains []string `json:"skill_domains"` // required skill domains for Vault scope resolution
	TTLSeconds   int      `json:"ttl_seconds"`   // policy token TTL; 0 uses server default (3600)
}

// CredentialResponse is received from the Orchestrator carrying the result of a
// credential.request (authorize). On "granted" the Vault returns an opaque
// permission_token reference — never a raw credential value (NFR-08).
//
// RequestID echoes the originating CredentialRequest.RequestID and is used as a
// fallback correlation key when the envelope correlation_id is unavailable.
type CredentialResponse struct {
	RequestID       string `json:"request_id"`                 // echoes CredentialRequest.RequestID
	Status          string `json:"status"`                     // "granted" | "denied" | "error"
	PermissionToken string `json:"permission_token,omitempty"` // opaque reference; present on "granted"
	ExpiresAt       string `json:"expires_at,omitempty"`       // ISO 8601; present on "granted"
	ErrorCode       string `json:"error_code,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"` // must not expose vault internals or paths
}

// TaskFailed is published to aegis.orchestrator.task.failed when a task cannot be
// executed due to a non-recoverable provisioning or credential failure. ErrorMessage
// must be user-safe — it must not expose internal paths, credential details, or vault
// implementation specifics.
type TaskFailed struct {
	TaskID       string `json:"task_id"`
	AgentID      string `json:"agent_id,omitempty"`
	ErrorCode    string `json:"error_code"`      // e.g. "VAULT_UNREACHABLE", "PROVISION_FAILED", "CONTEXT_BUDGET_EXCEEDED"
	ErrorMessage string `json:"error_message"`   // user-safe description
	Phase        string `json:"phase,omitempty"` // provisioning phase where failure occurred, e.g. "skill_resolution"
	TraceID      string `json:"trace_id"`
}

// VaultOperationRequest is sent to the Orchestrator (routed to the Vault) to execute
// a credentialed operation on behalf of an agent. The agent never receives the raw
// credential — only the operation_result flows back.
//
// request_id is the idempotency key. Safe to resubmit with the same request_id after
// a crash; the Vault guarantees exactly-once execution (EDD ADR-004).
type VaultOperationRequest struct {
	RequestID       string          `json:"request_id"` // UUID; idempotency key for safe resubmission
	AgentID         string          `json:"agent_id"`
	TaskID          string          `json:"task_id"`
	PermissionToken string          `json:"permission_token"` // opaque reference from prior authorize — never a raw secret
	OperationType   string          `json:"operation_type"`   // e.g. "web_fetch", "storage_read"
	OperationParams json.RawMessage `json:"operation_params"` // schema defined per operation_type by the Vault
	TimeoutSeconds  int             `json:"timeout_seconds"`  // 1–300; hard deadline forwarded to Vault
	CredentialType  string          `json:"credential_type"`  // e.g. "web_api_key"; Vault resolves the secret internally
}

// VaultOperationResult is received from the Orchestrator after the Vault has
// executed a credentialed operation. Contains operation output only — never
// the raw credential value (EDD ADR-004, NFR-08).
type VaultOperationResult struct {
	RequestID       string          `json:"request_id"`
	AgentID         string          `json:"agent_id"`
	Status          string          `json:"status"`                     // "success" | "timed_out" | "scope_violation" | "execution_error"
	OperationResult json.RawMessage `json:"operation_result,omitempty"` // present on success; operation output only
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"` // must not expose vault internals or paths
	ElapsedMS       int             `json:"elapsed_ms"`
}

// CrashSnapshot is the agent state saved to the Memory Interface at the start of
// the crash recovery sequence (EDD §6.3, Step 1). Written with DataType "snapshot"
// and context tag "crash_snapshot". The respawned agent receives this as part of
// its spawn context so it can resume from a known-good checkpoint.
//
// UnknownVaultRequestIDs lists request_ids that were in-flight at crash time with
// no corresponding result. The recovered agent MUST resubmit them with the original
// request_id — the Vault's idempotency guarantee ensures exactly-once execution.
type CrashSnapshot struct {
	AgentID                string    `json:"agent_id"`
	TaskID                 string    `json:"task_id"`
	FailureCount           int       `json:"failure_count"` // value at time of crash, before recovery increment
	State                  string    `json:"state"`         // state at time of crash (typically "active")
	SkillDomains           []string  `json:"skill_domains"`
	PermissionSet          []string  `json:"permission_set"`
	UnknownVaultRequestIDs []string  `json:"unknown_vault_request_ids"` // in-flight request_ids with no result at crash time
	CrashedAt              time.Time `json:"crashed_at"`
}

// MemoryReadRequest is sent to the Orchestrator to retrieve filtered memory context.
// The Orchestrator routes this to the Memory Component.
// DataType filters by MemoryWrite.DataType; when set with an empty AgentID, all
// agents' records of that type are returned (used for component-wide startup recovery).
type MemoryReadRequest struct {
	AgentID    string `json:"agent_id"`
	DataType   string `json:"data_type,omitempty"`
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

// SkillSearchResult is a single entry returned by skills.Manager.Search (EDD §13.5).
// Contains only domain path, command name, and description — parameters are
// withheld per the progressive disclosure contract. Call GetSpec for the full
// parameter schema of a specific command.
type SkillSearchResult struct {
	Domain      string  `json:"domain"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Score       float64 `json:"score"` // cosine similarity in [0, 1]; higher is more relevant
}

// SessionEntry is one node in the agent's append-only session log tree (EDD §13.1).
// Each entry is written via state.write (DataType "episode") before the turn it
// represents completes. VaultRequestID is set on "tool_call" entries that dispatch
// a vault.execute.request — crash recovery uses this field to identify in-flight
// operations that need resubmission (EDD §6.3).
type SessionEntry struct {
	EntryID        string    `json:"entry_id"`
	ParentEntryID  string    `json:"parent_entry_id,omitempty"`
	TurnType       string    `json:"turn_type"` // "user_message" | "assistant_response" | "tool_call" | "tool_result" | "compaction"
	Content        string    `json:"content"`
	Timestamp      time.Time `json:"timestamp"`
	VaultRequestID string    `json:"vault_request_id,omitempty"` // set on tool_call entries that trigger vault execution
}

// VaultOperationProgress is received from the Orchestrator as a transient progress
// heartbeat during long-running Vault operations (aegis.agents.vault.execute.progress).
// Delivery is at-most-once (core NATS). Progress events must not enter LLM context;
// they are forwarded to monitoring output only. Losing an event is acceptable and
// must not affect operation correctness.
type VaultOperationProgress struct {
	RequestID    string `json:"request_id"`
	AgentID      string `json:"agent_id"`
	ProgressType string `json:"progress_type"` // "heartbeat" | "milestone"
	Message      string `json:"message"`
	ElapsedMS    int    `json:"elapsed_ms"`
}

// VaultCancelRequest is published to aegis.orchestrator.vault.execute.cancel when
// the local deadline fires before a vault.execute.result arrives (EDD §13.1 Phase 2).
// The Orchestrator forwards this to the Vault so it can abort the in-flight operation.
type VaultCancelRequest struct {
	RequestID     string `json:"request_id"`
	AgentID       string `json:"agent_id"`
	TaskID        string `json:"task_id"`
	OperationType string `json:"operation_type"`
	Reason        string `json:"reason"` // "local_timeout" | "context_cancelled"
}

// Metrics event types — published by agent-process subprocesses to
// aegis.metrics.event (at-most-once core NATS) for aggregation by the
// aegis-agents component into Prometheus counters and histograms.
const (
	MetricsEventVaultExecuteComplete = "vault_execute_complete"
	MetricsEventCompactionTriggered  = "compaction_triggered"
	MetricsEventContextOverflow      = "context_overflow"
)

// MetricsEvent is the payload published by agent-process subprocesses to
// aegis.metrics.event. The aegis-agents process subscribes and records the
// corresponding Prometheus observations. Delivery is at-most-once; losing
// an event produces a small under-count but never a correctness failure.
type MetricsEvent struct {
	AgentID       string `json:"agent_id"`
	EventType     string `json:"event_type"`               // one of the MetricsEvent* constants
	OperationType string `json:"operation_type,omitempty"` // set for vault_execute_complete
	ElapsedMS     int    `json:"elapsed_ms,omitempty"`     // set for vault_execute_complete
}

// Audit event kind constants — the 15 defined event types (EDD §8.8).
// Every AuditEvent.EventType must be one of these values.
const (
	AuditEventCredentialGrant      = "credential_grant"
	AuditEventCredentialDeny       = "credential_deny"
	AuditEventCredentialRevoke     = "credential_revoke"
	AuditEventScopeViolation       = "scope_violation"
	AuditEventVaultExecuteRequest  = "vault_execute_request"
	AuditEventVaultExecuteResult   = "vault_execute_result"
	AuditEventVaultExecuteTimeout  = "vault_execute_timeout"
	AuditEventStateTransition      = "state_transition"
	AuditEventProvisioningStart    = "provisioning_start"
	AuditEventProvisioningComplete = "provisioning_complete"
	AuditEventProvisioningFail     = "provisioning_fail"
	AuditEventRecoveryAttempt      = "recovery_attempt"
	AuditEventTaskAccepted         = "task_accepted"
	AuditEventTaskCompleted        = "task_completed"
	AuditEventTaskFailed           = "task_failed"
)

// AuditEvent is published to aegis.orchestrator.audit.event (EDD §8.8).
// It must never contain raw credential values, operation_result payloads, or PII.
// Details carries event-specific metadata as a flat string map — this constraint
// prevents accidental nesting of structured data that could carry sensitive values.
type AuditEvent struct {
	EventID   string            `json:"event_id"`   // UUID; idempotency key
	EventType string            `json:"event_type"` // one of the AuditEvent* constants
	AgentID   string            `json:"agent_id,omitempty"`
	TaskID    string            `json:"task_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Details   map[string]string `json:"details,omitempty"` // event-specific metadata; never credentials or PII
}

// SteeringDirective is sent by the Orchestrator to a running agent microVM on
// aegis.agents.steering.<agent_id> (core NATS, at-most-once) to redirect the
// agent without requiring task termination and re-spawn (OQ-08).
//
// The agent process monitors this subject during the Act phase. On receipt the
// directive is applied before the next Reason phase — or immediately if
// InterruptTool is true, in which case the in-flight tool call is cancelled via
// context cancellation and a [TOOL_INTERRUPTED] result is injected.
//
// Type values:
//   - "redirect"        — update the agent's working instructions; optionally interrupt.
//   - "abort_tool"      — interrupt the current tool immediately; InterruptTool must be true.
//   - "inject_context"  — add information to the agent's context without changing its goal.
//   - "cancel"          — terminate the task gracefully; agent exits after acknowledging.
type SteeringDirective struct {
	DirectiveID   string `json:"directive_id"` // UUID v4; idempotency key
	AgentID       string `json:"agent_id"`
	TaskID        string `json:"task_id"`
	TraceID       string `json:"trace_id"`
	Type          string `json:"type"`                     // "redirect" | "abort_tool" | "inject_context" | "cancel"
	Instructions  string `json:"instructions"`             // new/additional instructions for the agent
	InterruptTool bool   `json:"interrupt_tool,omitempty"` // if true, cancel in-flight tool via ctx cancellation
	Priority      int    `json:"priority,omitempty"`       // 1–10; higher overrides lower pending directive
}

// SteeringAck is published by the agent process to aegis.orchestrator.steering.ack
// (JetStream, at-least-once) to confirm receipt and application of a SteeringDirective.
// The Orchestrator uses this to confirm the directive was acted upon.
type SteeringAck struct {
	DirectiveID string `json:"directive_id"` // echoes SteeringDirective.DirectiveID
	AgentID     string `json:"agent_id"`
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`           // "received" | "applied" | "ignored_stale"
	Reason      string `json:"reason,omitempty"` // human-readable explanation when status != "applied"
}

// DeadLetterEvent is published to aegis.orchestrator.error (MessageType: "dead.letter")
// when an inbound JetStream message exhausts its redelivery budget without being
// successfully acknowledged by any handler. The Orchestrator uses this to detect
// stalled tasks and trigger intervention or manual replay.
//
// OriginalEnvelope contains the full wire bytes of the original inbound message —
// the complete outbound envelope as sent by the remote component including
// message_id, message_type, correlation_id, and payload. This allows the
// Orchestrator to identify the stalled operation and correlate it to a task.
type DeadLetterEvent struct {
	// OriginalSubject is the NATS subject the message was received on.
	OriginalSubject string `json:"original_subject"`

	// ConsumerName is the durable JetStream consumer that was processing the message.
	ConsumerName string `json:"consumer_name"`

	// MessageType is extracted from the original message envelope, if present.
	MessageType string `json:"message_type,omitempty"`

	// CorrelationID is extracted from the original message envelope (task_id,
	// request_id, or query_id). Use this to correlate the stalled message to a task.
	CorrelationID string `json:"correlation_id,omitempty"`

	// OriginalEnvelope is the full wire-format message received from NATS, including
	// the complete outbound envelope and payload. Embedded verbatim for replay.
	OriginalEnvelope json.RawMessage `json:"original_envelope"`

	// DeliveryAttempts is the number of times JetStream attempted delivery before
	// the message was dead-lettered.
	DeliveryAttempts int `json:"delivery_attempts"`

	// FailureReason describes why the message was dead-lettered.
	// Always "max_redelivery_exceeded" for budget exhaustion.
	FailureReason string `json:"failure_reason"`

	// DeadLetteredAt is the UTC timestamp when the dead-letter event was emitted.
	DeadLetteredAt time.Time `json:"dead_lettered_at"`
}
