package bus

const (
	StreamTasks      = "AEGIS_TASKS"
	StreamUI         = "AEGIS_UI"
	StreamAgents     = "AEGIS_AGENTS"
	StreamRuntime    = "AEGIS_RUNTIME"
	StreamVault      = "AEGIS_VAULT"
	StreamMemory     = "AEGIS_MEMORY"
	StreamMonitoring = "AEGIS_MONITORING"
	StreamDLQ        = "AEGIS_DLQ"
	// Transient JetStream domains (memory-backed, short retention) — EDD §8.1 at-most-once style delivery.
	StreamCapabilityTransient   = "AEGIS_CAPABILITY_TRANSIENT"
	StreamVaultProgressTransient = "AEGIS_VAULTPROGRESS_TRANSIENT"
)

const (
	SubjectTasks            = "aegis.tasks.>"
	SubjectUI               = "aegis.ui.>"
	SubjectAgents           = "aegis.agents.>"
	SubjectRuntime          = "aegis.runtime.>"
	SubjectVault            = "aegis.vault.>"
	SubjectMemory           = "aegis.memory.>"
	SubjectMonitoring       = "aegis.monitoring.>"
	SubjectAll              = "aegis.>"
	SubjectTasksRouted      = "aegis.tasks.routed"
	SubjectTasksPlanCreated = "aegis.tasks.plan_created"
	SubjectAgentsCreated    = "aegis.agents.created"
	SubjectAgentsFailed     = "aegis.agents.failed"
	SubjectAgentsTerminated = "aegis.agents.terminated"
	// EDD §9.2 / M3: agent-only subscribe; Permission Manager / Vault may publish.
	SubjectAgentsVaultExecuteResult = "aegis.agents.vault.execute.result"
	SubjectAgentsCredentialResponse = "aegis.agents.credential.response"
	// SubjectAgentsLeafWildcard matches single-token agent events (created, failed, …) but not nested paths like SubjectAgentsVaultExecuteResult.
	SubjectAgentsLeafWildcard = "aegis.agents.*"
	SubjectRuntimeCompleted = "aegis.runtime.completed"
	SubjectMemorySaved      = "aegis.memory.saved"
	SubjectUIAction         = "aegis.ui.action"
	SubjectHealthDatabus    = "aegis.health.databus"
	SubjectHealthRecovery   = "aegis.health.recovery_completed"
	SubjectDLQ                 = "aegis.dlq"
	SubjectDLQPattern          = "aegis.dlq.>" // SR-DB-006: admin-only subscribe
	SubjectPersonalization     = "aegis.personalization.get"
	SubjectMonitoringHealth    = "aegis.monitoring.health.>"   // FR-DB-006: high priority
	SubjectMonitoringResource  = "aegis.monitoring.resource.>" // FR-DB-006: standard priority
	// At-most-once transient domains (no overlap with aegis.vault.> durable stream — use distinct prefix).
	SubjectCapability   = "aegis.capability.>"
	SubjectVaultProgress = "aegis.vaultprogress.>"
)

// AegisStreamNames lists all JetStream streams created by EnsureStreams (for metrics polling).
func AegisStreamNames() []string {
	return []string{
		StreamTasks, StreamUI, StreamAgents, StreamRuntime,
		StreamVault, StreamMemory, StreamMonitoring, StreamDLQ,
		StreamCapabilityTransient, StreamVaultProgressTransient,
	}
}
