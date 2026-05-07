package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
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
// @Router /api/v1/agent/{taskId}/executions [post]
// @Router /api/v1/agents/tasks/{taskId}/executions [post]
func (h *AgentHandler) HandleCreateTaskExecution(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	if taskId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "taskId is required", nil))
		return
	}

	var req struct {
		AgentID       string          `json:"agentId"`
		ActionType    string          `json:"actionType"`
		Payload       json.RawMessage `json:"payload"`
		Status        string          `json:"status"`
		ErrorContext  string          `json:"errorContext"`
		LegacyAgentID string          `json:"agent_id"`
		LegacyAction  string          `json:"action_type"`
		LegacyErrCtx  string          `json:"error_context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}

	if req.AgentID == "" {
		req.AgentID = req.LegacyAgentID
	}
	if req.ActionType == "" {
		req.ActionType = req.LegacyAction
	}
	if req.ErrorContext == "" {
		req.ErrorContext = req.LegacyErrCtx
	}
	if req.AgentID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "agentId is required in the body", nil))
		return
	}
	if req.ActionType != "tool_call" && req.ActionType != "reasoning_step" && req.ActionType != "final_answer" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "actionType must be tool_call, reasoning_step, or final_answer", nil))
		return
	}
	if req.Status != "pending" && req.Status != "success" && req.Status != "failed" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "status must be pending, success, or failed", nil))
		return
	}
	if len(req.Payload) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "payload is required", nil))
		return
	}

	executionID, err := uuid.NewV7()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to generate executionId", nil))
		return
	}
	createdAt := time.Now().UTC()

	params := storage.CreateTaskExecutionParams{
		ID:         pgtype.UUID{Bytes: executionID, Valid: true},
		AgentID:    req.AgentID,
		ActionType: req.ActionType,
		Payload:    req.Payload,
		Status:     req.Status,
	}

	if err := params.TaskID.Scan(taskId); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid taskId", nil))
		return
	}

	if req.ErrorContext != "" {
		params.ErrorContext.String = req.ErrorContext
		params.ErrorContext.Valid = true
	}

	if err := h.repo.CreateTaskExecution(r.Context(), params); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to create task execution log", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"executionId": executionID,
		"createdAt":   createdAt,
	}))
}

// HandleGetExecutions fetches and returns the chronological log of an agent's work for a specific taskId
// @Summary Get task executions
// @Description Fetches and returns the chronological log of an agent's work for a specific taskId
// @Tags agents
// @Produce json
// @Param taskId path string true "Task ID"
// @Param limit query int false "Limit number of executions (default: 100)"
// @Success 200 {object} map[string]interface{} "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/agent/{taskId}/executions [get]
// @Router /api/v1/agents/tasks/{taskId}/executions [get]
func (h *AgentHandler) HandleGetExecutions(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	if taskId == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "taskId is required", nil))
		return
	}

	var parsedTaskID pgtype.UUID
	if err := parsedTaskID.Scan(taskId); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid taskId format", nil))
		return
	}

	limit := int32(100)
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		parsed, err := strconv.ParseInt(limitStr, 10, 32)
		if err != nil || parsed <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid limit", nil))
			return
		}
		limit = int32(parsed)
	}

	executions, err := h.repo.GetExecutionsByTaskIDLimit(r.Context(), parsedTaskID, limit)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to get task executions", err.Error()))
		return
	}

	type executionResponse struct {
		ExecutionID  uuid.UUID       `json:"executionId"`
		TaskID       uuid.UUID       `json:"taskId"`
		AgentID      string          `json:"agentId"`
		ActionType   string          `json:"actionType"`
		Payload      json.RawMessage `json:"payload"`
		Status       string          `json:"status"`
		ErrorContext *string         `json:"errorContext,omitempty"`
		CreatedAt    time.Time       `json:"createdAt"`
	}

	formatted := make([]executionResponse, 0, len(executions))
	for _, e := range executions {
		row := executionResponse{
			ExecutionID: uuid.UUID(e.ID.Bytes),
			TaskID:      uuid.UUID(e.TaskID.Bytes),
			AgentID:     e.AgentID,
			ActionType:  e.ActionType,
			Payload:     e.Payload,
			Status:      e.Status,
			CreatedAt:   e.CreatedAt.Time,
		}
		if e.ErrorContext.Valid {
			v := e.ErrorContext.String
			row.ErrorContext = &v
		}
		formatted = append(formatted, row)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"executions": formatted}))
}
