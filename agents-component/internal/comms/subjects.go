package comms

// Inbound subjects — messages the Agents Component receives from the Orchestrator.
// All require SubscribeDurable (at-least-once) except those marked at-most-once.
const (
	// At-least-once inbound subjects.
	SubjectTaskInbound           = "aegis.agents.task.inbound"
	SubjectLifecycleTerminate    = "aegis.agents.lifecycle.terminate"
	SubjectCredentialResponse    = "aegis.agents.credential.response"
	SubjectVaultExecuteResult    = "aegis.agents.vault.execute.result"
	SubjectStateWriteAck         = "aegis.agents.state.write.ack"
	SubjectStateReadResponse     = "aegis.agents.state.read.response"
	SubjectClarificationResponse = "aegis.agents.clarification.response"

	// At-most-once inbound subjects — use Subscribe, not SubscribeDurable.
	SubjectCapabilityQuery      = "aegis.agents.capability.query"
	SubjectVaultExecuteProgress = "aegis.agents.vault.execute.progress"
)

// Outbound subjects — messages the Agents Component sends to the Orchestrator.
// All use JetStream (at-least-once) except SubjectCapabilityResponse (Transient: true).
const (
	SubjectTaskAccepted         = "aegis.orchestrator.task.accepted"
	SubjectTaskResult           = "aegis.orchestrator.task.result"
	SubjectTaskFailed           = "aegis.orchestrator.task.failed"
	SubjectAgentStatus          = "aegis.orchestrator.agent.status"
	SubjectCredentialRequest    = "aegis.orchestrator.credential.request"
	SubjectVaultExecuteRequest  = "aegis.orchestrator.vault.execute.request"
	SubjectStateWrite           = "aegis.orchestrator.state.write"
	SubjectStateReadRequest     = "aegis.orchestrator.state.read.request"
	SubjectClarificationRequest = "aegis.orchestrator.clarification.request"
	SubjectAuditEvent           = "aegis.orchestrator.audit.event"
	SubjectError                = "aegis.orchestrator.error"

	// At-most-once outbound subject — set Transient: true in PublishOptions.
	SubjectCapabilityResponse = "aegis.orchestrator.capability.response"
)

// Durable consumer names for SubscribeDurable calls.
// Each name is stable across restarts — the NATS server uses it to track
// delivery position. One consumer name per inbound at-least-once subject.
const (
	ConsumerTaskInbound           = "agents-task-inbound"
	ConsumerLifecycleTerminate    = "agents-lifecycle-terminate"
	ConsumerCredentialResponse    = "agents-credential-response"
	ConsumerVaultExecuteResult    = "agents-vault-execute-result"
	ConsumerStateWriteAck         = "agents-state-write-ack"
	ConsumerStateReadResponse     = "agents-state-read-response"
	ConsumerClarificationResponse = "agents-clarification-response"
)

// Heartbeat subjects — published directly by agent-process binaries (core NATS,
// at-most-once). Kept outside the aegis.agents.* and aegis.orchestrator.*
// namespaces so they are not captured by JetStream streams.
const (
	// SubjectHeartbeatAll is the wildcard subscription used by the Lifecycle Manager.
	SubjectHeartbeatAll = "aegis.heartbeat.*"

	// SubjectHeartbeatPrefix is prepended to an agent_id to form the per-agent subject.
	// Use HeartbeatSubject(agentID) rather than constructing the string directly.
	SubjectHeartbeatPrefix = "aegis.heartbeat."

	MsgTypeHeartbeat = "agent.heartbeat"
)

// HeartbeatSubject returns the NATS subject for a specific agent's heartbeat.
func HeartbeatSubject(agentID string) string {
	return SubjectHeartbeatPrefix + agentID
}

// Message type constants for the envelope MessageType field (dot-notation).
// Every Publish call must set opts.MessageType to one of these values.
const (
	MsgTypeTaskInbound           = "task.inbound"
	MsgTypeTaskAccepted          = "task.accepted"
	MsgTypeTaskResult            = "task.result"
	MsgTypeTaskFailed            = "task.failed"
	MsgTypeCapabilityQuery       = "capability.query"
	MsgTypeCapabilityResponse    = "capability.response"
	MsgTypeAgentStatus           = "agent.status"
	MsgTypeCredentialRequest     = "credential.request"
	MsgTypeCredentialResponse    = "credential.response"
	MsgTypeVaultExecuteRequest   = "vault.execute.request"
	MsgTypeVaultExecuteResult    = "vault.execute.result"
	MsgTypeVaultExecuteProgress  = "vault.execute.progress"
	MsgTypeStateWrite            = "state.write"
	MsgTypeStateWriteAck         = "state.write.ack"
	MsgTypeStateReadRequest      = "state.read.request"
	MsgTypeStateReadResponse     = "state.read.response"
	MsgTypeClarificationRequest  = "clarification.request"
	MsgTypeClarificationResponse = "clarification.response"
	MsgTypeAuditEvent            = "audit.event"
	MsgTypeError                 = "error"
	MsgTypeLifecycleTerminate    = "lifecycle.terminate"
)
