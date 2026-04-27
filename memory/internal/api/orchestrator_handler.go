package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

const maxOrchestratorPayloadBytes = 256 * 1024

type OrchestratorHandler struct {
	repo *storage.OrchestratorRepository
}

type WriteOrchestratorRecordRequest struct {
	OrchestratorTaskRef string          `json:"orchestrator_task_ref"`
	TaskID              string          `json:"task_id"`
	PlanID              string          `json:"plan_id,omitempty"`
	SubtaskID           string          `json:"subtask_id,omitempty"`
	TraceID             string          `json:"trace_id,omitempty"`
	DataType            string          `json:"data_type"`
	Timestamp           string          `json:"timestamp"`
	Payload             json.RawMessage `json:"payload" swaggertype:"object"`
	TTLSeconds          int32           `json:"ttl_seconds,omitempty"`
}

type OrchestratorRecordResponse struct {
	ID                  string      `json:"id,omitempty"`
	OrchestratorTaskRef string      `json:"orchestrator_task_ref"`
	TaskID              string      `json:"task_id"`
	PlanID              *string     `json:"plan_id,omitempty"`
	SubtaskID           *string     `json:"subtask_id,omitempty"`
	TraceID             *string     `json:"trace_id,omitempty"`
	DataType            string      `json:"data_type"`
	Timestamp           time.Time   `json:"timestamp"`
	Payload             interface{} `json:"payload"`
	TTLSeconds          int32       `json:"ttl_seconds,omitempty"`
	CreatedAt           time.Time   `json:"created_at,omitempty"`
}

func NewOrchestratorHandler(repo *storage.OrchestratorRepository) *OrchestratorHandler {
	return &OrchestratorHandler{repo: repo}
}

// HandleWriteRecord persists one orchestrator record.
// @Summary Write orchestrator record
// @Description Creates or upserts one orchestrator record depending on data_type semantics
// @Tags orchestrator
// @Accept json
// @Produce json
// @Param request body WriteOrchestratorRecordRequest true "Orchestrator record payload"
// @Success 201 {object} map[string]interface{} "Created"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/orchestrator/records [post]
func (h *OrchestratorHandler) HandleWriteRecord(w http.ResponseWriter, r *http.Request) {
	var req WriteOrchestratorRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid request body", nil)
		return
	}
	if stringsBlank(req.OrchestratorTaskRef) || stringsBlank(req.TaskID) || stringsBlank(req.DataType) || stringsBlank(req.Timestamp) || len(req.Payload) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "orchestrator_task_ref, task_id, data_type, timestamp, and payload are required", nil)
		return
	}
	if len(req.Payload) > maxOrchestratorPayloadBytes {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "payload exceeds 256KB limit", nil)
		return
	}
	if !storage.ValidOrchestratorDataType(req.DataType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid data_type", nil)
		return
	}
	ts, err := time.Parse(time.RFC3339, req.Timestamp)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "timestamp must be RFC3339", nil)
		return
	}

	record, err := h.repo.WriteRecord(r.Context(), storage.WriteOrchestratorRecordParams{
		OrchestratorTaskRef: req.OrchestratorTaskRef,
		TaskID:              req.TaskID,
		PlanID:              req.PlanID,
		SubtaskID:           req.SubtaskID,
		TraceID:             req.TraceID,
		DataType:            req.DataType,
		Timestamp:           ts,
		Payload:             req.Payload,
		TTLSeconds:          req.TTLSeconds,
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrUnknownOrchestratorDataType),
			errors.Is(err, storage.ErrMissingPlanID),
			errors.Is(err, storage.ErrMissingSubtaskID):
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", err.Error(), nil)
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to write orchestrator record", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"id":     uuid.UUID(record.ID.Bytes).String(),
		"record": orchestratorRecordResponse(record, true),
	}))
}

// HandleQueryRecords returns all records matching the query.
// @Summary Query orchestrator records
// @Description Retrieves orchestrator records by data_type and task/orchestrator filters
// @Tags orchestrator
// @Produce json
// @Param data_type query string true "Data type"
// @Param task_id query string false "Task ID"
// @Param orchestrator_task_ref query string false "Orchestrator task ref"
// @Param from_timestamp query string false "Inclusive lower bound timestamp"
// @Param to_timestamp query string false "Inclusive upper bound timestamp"
// @Param state_filter query string false "Use not_terminal to exclude terminal task states"
// @Success 200 {object} map[string]interface{} "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/orchestrator/records [get]
func (h *OrchestratorHandler) HandleQueryRecords(w http.ResponseWriter, r *http.Request) {
	dataType := r.URL.Query().Get("data_type")
	taskID := r.URL.Query().Get("task_id")
	orchRef := r.URL.Query().Get("orchestrator_task_ref")
	stateFilter := r.URL.Query().Get("state_filter")
	if stringsBlank(dataType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "data_type is required", nil)
		return
	}
	if taskID == "" && orchRef == "" && stateFilter == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "task_id or orchestrator_task_ref is required unless state_filter is used", nil)
		return
	}
	if !storage.ValidOrchestratorDataType(dataType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid data_type", nil)
		return
	}
	if stateFilter != "" && stateFilter != "not_terminal" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "state_filter must be not_terminal", nil)
		return
	}

	var fromTS *time.Time
	if raw := r.URL.Query().Get("from_timestamp"); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "from_timestamp must be RFC3339", nil)
			return
		}
		fromTS = &ts
	}
	var toTS *time.Time
	if raw := r.URL.Query().Get("to_timestamp"); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "to_timestamp must be RFC3339", nil)
			return
		}
		toTS = &ts
	}

	records, err := h.repo.QueryRecords(r.Context(), storage.QueryOrchestratorRecordsParams{
		DataType:            dataType,
		TaskID:              taskID,
		OrchestratorTaskRef: orchRef,
		FromTimestamp:       fromTS,
		ToTimestamp:         toTS,
		StateFilter:         stateFilter,
	})
	if err != nil {
		if errors.Is(err, storage.ErrUnknownOrchestratorDataType) {
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid data_type", nil)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to query orchestrator records", err.Error())
		return
	}

	resp := make([]OrchestratorRecordResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, orchestratorRecordResponse(rec, false))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"records": resp}))
}

// HandleReadLatest returns the latest record by task_id and data_type.
// @Summary Read latest orchestrator record
// @Description Retrieves the latest orchestrator record for a task and data_type
// @Tags orchestrator
// @Produce json
// @Param task_id query string true "Task ID"
// @Param data_type query string true "Data type"
// @Success 200 {object} map[string]interface{} "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/orchestrator/records/latest [get]
func (h *OrchestratorHandler) HandleReadLatest(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	dataType := r.URL.Query().Get("data_type")
	if stringsBlank(taskID) || stringsBlank(dataType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "task_id and data_type are required", nil)
		return
	}
	if !storage.ValidOrchestratorDataType(dataType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid data_type", nil)
		return
	}

	record, err := h.repo.ReadLatest(r.Context(), taskID, dataType)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrUnknownOrchestratorDataType):
			writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid data_type", nil)
		case errors.Is(err, storage.ErrOrchestratorRecordNotFound):
			writeJSONError(w, http.StatusNotFound, "not_found", "record not found", nil)
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to read latest orchestrator record", err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"record": orchestratorRecordResponse(record, false),
	}))
}

func orchestratorRecordResponse(rec storage.OrchestratorRecord, includeID bool) OrchestratorRecordResponse {
	var planID *string
	if rec.PlanID.Valid {
		v := rec.PlanID.String
		planID = &v
	}
	var subtaskID *string
	if rec.SubtaskID.Valid {
		v := rec.SubtaskID.String
		subtaskID = &v
	}
	var traceID *string
	if rec.TraceID.Valid {
		v := rec.TraceID.String
		traceID = &v
	}
	resp := OrchestratorRecordResponse{
		OrchestratorTaskRef: rec.OrchestratorTaskRef,
		TaskID:              rec.TaskID,
		PlanID:              planID,
		SubtaskID:           subtaskID,
		TraceID:             traceID,
		DataType:            rec.DataType,
		Timestamp:           rec.Timestamp.Time,
		Payload:             rec.PayloadJSON(),
		TTLSeconds:          rec.TTLSeconds,
		CreatedAt:           rec.CreatedAt.Time,
	}
	if includeID {
		resp.ID = uuid.UUID(rec.ID.Bytes).String()
	}
	return resp
}

func stringsBlank(v string) bool {
	return strings.TrimSpace(v) == ""
}
