package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type AgentHandler struct {
	repo *storage.AgentLogsRepository
}

// NewAgentHandler returns a new instance of AgentHandler
func NewAgentHandler(repo *storage.AgentLogsRepository) *AgentHandler {
	return &AgentHandler{repo: repo}
}

// HandleCreateTaskExecution creates a new task execution log
// @Summary Create task execution log
// @Description Creates a new task execution log for an agent
// @Tags agents
// @Accept json
// @Produce json
// @Param taskId path string true "Task ID"
// @Param request body object true "Task Execution Payload"
// @Success 201 "Created"
// @Failure 400 "Bad Request"
// @Failure 500 "Internal Server Error"
// @Router /api/v1/agents/tasks/{taskId}/executions [post]
func (h *AgentHandler) HandleCreateTaskExecution(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	if taskId == "" {
		http.Error(w, "taskId is required", http.StatusBadRequest)
		return
	}

	var req struct {
		ID           string `json:"id"`
		AgentID      string `json:"agent_id"`
		ActionType   string `json:"action_type"`
		Payload      []byte `json:"payload"`
		Status       string `json:"status"`
		ErrorContext string `json:"error_context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// The orchestrator requires agentId from the JSON body
	if req.AgentID == "" {
		http.Error(w, "agentId is required in the body", http.StatusBadRequest)
		return
	}

	params := storage.CreateTaskExecutionParams{
		AgentID:    req.AgentID,
		ActionType: req.ActionType,
		Payload:    req.Payload,
		Status:     req.Status,
	}

	if err := params.ID.Scan(req.ID); err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := params.TaskID.Scan(taskId); err != nil {
		http.Error(w, "invalid taskId", http.StatusBadRequest)
		return
	}

	if req.ErrorContext != "" {
		params.ErrorContext.String = req.ErrorContext
		params.ErrorContext.Valid = true
	}

	if err := h.repo.CreateTaskExecution(r.Context(), params); err != nil {
		http.Error(w, "failed to create task execution log", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// HandleGetExecutions fetches and returns the chronological log of an agent's work for a specific taskId
// @Summary Get task executions
// @Description Fetches and returns the chronological log of an agent's work for a specific taskId
// @Tags agents
// @Produce json
// @Param taskId path string true "Task ID"
// @Success 200 {array} storage.AgentLogsSchemaTaskExecution "List of task executions"
// @Failure 400 "Bad Request"
// @Failure 500 "Internal Server Error"
// @Router /api/v1/agents/tasks/{taskId}/executions [get]
func (h *AgentHandler) HandleGetExecutions(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	if taskId == "" {
		http.Error(w, "taskId is required", http.StatusBadRequest)
		return
	}

	var parsedTaskID pgtype.UUID
	if err := parsedTaskID.Scan(taskId); err != nil {
		http.Error(w, "invalid taskId format", http.StatusBadRequest)
		return
	}

	executions, err := h.repo.GetExecutionsByTaskID(r.Context(), parsedTaskID)
	if err != nil {
		http.Error(w, "failed to get task executions", http.StatusInternalServerError)
		return
	}

	// Handle empty result
	if executions == nil {
		executions = []storage.AgentLogsSchemaTaskExecution{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(executions); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}
