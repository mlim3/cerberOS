package api

import "net/http"

// AppHandlers holds all application-layer HTTP handlers.
// Pass it to RegisterRoutes to mount every API route onto a mux.
// Both the production server (cmd/server/main.go) and the integration
// test server (tests/service_test_setup_test.go) call RegisterRoutes so
// the two surfaces stay in sync automatically.
type AppHandlers struct {
	Chat          *ChatHandler
	Orchestrator  *OrchestratorHandler
	SystemLog     *SystemLogHandler
	PersonalInfo  *PersonalInfoHandler
	Vault         *VaultHandler
	Agent         *AgentHandler
	ScheduledJobs *ScheduledJobsHandler
	Users         *UsersHandler
	SkillCache    *SkillCacheHandler
}

// RegisterRoutes mounts all application routes onto mux.
// Infrastructure-only endpoints (healthz, metrics, swagger) are omitted
// because they require server-specific dependencies and are registered
// directly in cmd/server/main.go.
func RegisterRoutes(mux *http.ServeMux, h AppHandlers) {
	// Users (demo-mode roster)
	mux.HandleFunc("GET /api/v1/users", h.Users.HandleListUsers)
	mux.HandleFunc("POST /api/v1/users", h.Users.HandleCreateUser)

	// Chat
	mux.HandleFunc("GET /api/v1/conversations", h.Chat.HandleListConversations)
	mux.HandleFunc("POST /api/v1/conversations", h.Chat.HandleCreateConversation)
	mux.HandleFunc("POST /api/v1/tasks", h.Chat.HandleCreateTask)
	mux.HandleFunc("GET /api/v1/tasks/{taskId}", h.Chat.HandleGetTask)
	mux.HandleFunc("POST /api/v1/chat/{conversationId}/messages", h.Chat.HandleCreateMessage)
	mux.HandleFunc("GET /api/v1/chat/{conversationId}/messages", h.Chat.HandleListMessages)
	mux.HandleFunc("GET /api/v1/chat/{conversationId}/history", h.Chat.HandleGetSessionHistory)

	// Orchestrator (internal — protected by vault key)
	orchestratorMux := http.NewServeMux()
	orchestratorMux.HandleFunc("POST /api/v1/orchestrator/records", h.Orchestrator.HandleWriteRecord)
	orchestratorMux.HandleFunc("GET /api/v1/orchestrator/records", h.Orchestrator.HandleQueryRecords)
	orchestratorMux.HandleFunc("GET /api/v1/orchestrator/records/latest", h.Orchestrator.HandleReadLatest)
	mux.Handle("/api/v1/orchestrator/", http.StripPrefix("", RequireVaultKey(orchestratorMux)))

	// Personal Info
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/save", h.PersonalInfo.Save)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/query", h.PersonalInfo.Query)
	mux.HandleFunc("GET /api/v1/personal_info/{userId}/all", h.PersonalInfo.GetAll)
	mux.HandleFunc("PUT /api/v1/personal_info/{userId}/facts/{factId}", h.PersonalInfo.UpdateFact)
	mux.HandleFunc("DELETE /api/v1/personal_info/{userId}/facts/{factId}", h.PersonalInfo.DeleteFact)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/facts/{factId}/archive", h.PersonalInfo.ArchiveFact)
	mux.HandleFunc("POST /api/v1/personal_info/{userId}/facts/{factId}/supersede", h.PersonalInfo.SupersedeFact)

	// System Log
	mux.HandleFunc("POST /api/v1/system/events", h.SystemLog.HandleCreateSystemEvent)
	mux.HandleFunc("GET /api/v1/system/events", h.SystemLog.HandleListSystemEvents)
	mux.HandleFunc("GET /api/v1/system/events/search", h.SystemLog.HandleSearchSystemEvents)

	// Scheduled Jobs (protected by vault key)
	mux.Handle("POST /api/v1/scheduled_jobs", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleCreateScheduledJob)))
	mux.Handle("POST /api/v1/scheduled_jobs/run_due", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleRunDueJobs)))
	mux.Handle("GET /api/v1/scheduled_jobs/{jobId}/runs", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleListScheduledJobRuns)))
	mux.Handle("GET /api/v1/user_crons", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleListUserCrons)))
	mux.Handle("DELETE /api/v1/scheduled_jobs/{jobId}", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleDeleteUserCron)))
	mux.Handle("POST /api/v1/idempotency/claim", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleClaimIdempotency)))
	mux.Handle("POST /api/v1/idempotency/complete", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleCompleteIdempotency)))
	mux.Handle("POST /api/v1/system/maintenance/run", RequireVaultKey(http.HandlerFunc(h.ScheduledJobs.HandleRunSystemMaintenance)))

	// Vault (internal — protected by vault key)
	vaultMux := http.NewServeMux()
	vaultMux.HandleFunc("POST /api/v1/vault/{userId}/secrets", h.Vault.HandleSaveSecret)
	vaultMux.HandleFunc("PUT /api/v1/vault/{userId}/secrets/{keyName}", h.Vault.HandleUpdateSecret)
	vaultMux.HandleFunc("GET /api/v1/vault/{userId}/secrets", h.Vault.HandleGetSecret)
	vaultMux.HandleFunc("DELETE /api/v1/vault/{userId}/secrets/{keyName}", h.Vault.HandleDeleteSecret)
	mux.Handle("/api/v1/vault/", http.StripPrefix("", RequireVaultKey(vaultMux)))

	// Skill Cache (internal — called by Orchestrator on behalf of Agents Component)
	if h.SkillCache != nil {
		mux.HandleFunc("POST /api/v1/skills/cache", h.SkillCache.Upsert)
		mux.HandleFunc("POST /api/v1/skills/cache/search", h.SkillCache.Search)
		mux.HandleFunc("POST /api/v1/skills/cache/seed-check", h.SkillCache.CheckSeedHash)
		mux.HandleFunc("GET /api/v1/skills/cache/{domain}", h.SkillCache.ListByDomain)
		mux.HandleFunc("DELETE /api/v1/skills/cache/{domain}/{name}", h.SkillCache.Delete)
	}

	// Agent Logs
	mux.HandleFunc("POST /api/v1/agent/{taskId}/executions", h.Agent.HandleCreateTaskExecution)
	mux.HandleFunc("GET /api/v1/agent/{taskId}/executions", h.Agent.HandleGetExecutions)
	mux.HandleFunc("GET /api/v1/agents/{agentId}/logs", h.Agent.HandleGetAgentLogs)
	// Legacy routes retained temporarily for backward compatibility.
	mux.HandleFunc("POST /api/v1/agents/tasks/{taskId}/executions", h.Agent.HandleCreateTaskExecution)
	mux.HandleFunc("GET /api/v1/agents/tasks/{taskId}/executions", h.Agent.HandleGetExecutions)
}
