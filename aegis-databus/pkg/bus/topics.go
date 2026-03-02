package bus

const (
	StreamTasks      = "AEGIS_TASKS"
	StreamAgents     = "AEGIS_AGENTS"
	StreamRuntime    = "AEGIS_RUNTIME"
	StreamVault      = "AEGIS_VAULT"
	StreamMemory     = "AEGIS_MEMORY"
	StreamMonitoring = "AEGIS_MONITORING"
)

const (
	SubjectTasks            = "aegis.tasks.>"
	SubjectAgents           = "aegis.agents.>"
	SubjectRuntime          = "aegis.runtime.>"
	SubjectVault            = "aegis.vault.>"
	SubjectMemory           = "aegis.memory.>"
	SubjectMonitoring       = "aegis.monitoring.>"
	SubjectAll              = "aegis.>"
	SubjectTasksRouted      = "aegis.tasks.routed"
	SubjectAgentsCreated    = "aegis.agents.created"
	SubjectRuntimeCompleted = "aegis.runtime.completed"
)
