package bus

const (
	StreamTasks      = "AEGIS_TASKS"
	StreamUI         = "AEGIS_UI"
	StreamAgents     = "AEGIS_AGENTS"
	StreamRuntime    = "AEGIS_RUNTIME"
	StreamVault      = "AEGIS_VAULT"
	StreamMemory     = "AEGIS_MEMORY"
	StreamMonitoring = "AEGIS_MONITORING"
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
	SubjectRuntimeCompleted = "aegis.runtime.completed"
	SubjectMemorySaved      = "aegis.memory.saved"
	SubjectUIAction         = "aegis.ui.action"
	SubjectHealthDatabus    = "aegis.health.databus"
	SubjectHealthRecovery   = "aegis.health.recovery_completed"
	SubjectDLQ              = "aegis.dlq"
	SubjectPersonalization  = "aegis.personalization.get"
)
