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

type SystemLogHandler struct {
	repo *storage.LogRepository
}

func NewSystemLogHandler(repo *storage.LogRepository) *SystemLogHandler {
	return &SystemLogHandler{
		repo: repo,
	}
}

type CreateSystemEventRequest struct {
	TraceID     *uuid.UUID     `json:"traceId,omitempty"`
	ServiceName *string        `json:"serviceName,omitempty"`
	Severity    *string        `json:"severity,omitempty"`
	Message     string         `json:"message"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type SystemEventResponse struct {
	EventID     uuid.UUID      `json:"eventId"`
	TraceID     *uuid.UUID     `json:"traceId,omitempty"`
	ServiceName *string        `json:"serviceName,omitempty"`
	Severity    *string        `json:"severity,omitempty"`
	Message     string         `json:"message"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
}

func (h *SystemLogHandler) HandleCreateSystemEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req CreateSystemEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid request payload", nil))
		return
	}

	// Validate required fields
	if req.Message == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "message is required", nil))
		return
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to generate event ID", nil))
		return
	}

	var metadataBytes []byte
	if req.Metadata != nil {
		metadataBytes, err = json.Marshal(req.Metadata)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid metadata payload", nil))
			return
		}
	}

	params := storage.CreateSystemEventParams{
		ID:        pgtype.UUID{Bytes: eventID, Valid: true},
		Message:   req.Message,
		Metadata:  metadataBytes,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	}

	if req.TraceID != nil {
		params.TraceID = pgtype.UUID{Bytes: *req.TraceID, Valid: true}
	}

	if req.ServiceName != nil {
		params.ServiceName = pgtype.Text{String: *req.ServiceName, Valid: true}
	}

	if req.Severity != nil {
		params.Severity = pgtype.Text{String: *req.Severity, Valid: true}
	}

	event, err := h.repo.CreateSystemEvent(ctx, params)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to create system event", err.Error()))
		return
	}

	resp := SystemEventResponse{
		EventID:   event.ID.Bytes,
		Message:   event.Message,
		CreatedAt: event.CreatedAt.Time,
	}

	if event.TraceID.Valid {
		traceID := uuid.UUID(event.TraceID.Bytes)
		resp.TraceID = &traceID
	}
	if event.ServiceName.Valid {
		serviceName := event.ServiceName.String
		resp.ServiceName = &serviceName
	}
	if event.Severity.Valid {
		severity := event.Severity.String
		resp.Severity = &severity
	}
	if len(event.Metadata) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(event.Metadata, &metadata); err == nil {
			resp.Metadata = metadata
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"eventId":   resp.EventID,
		"createdAt": resp.CreatedAt,
	}))
}

func (h *SystemLogHandler) HandleListSystemEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limitStr := r.URL.Query().Get("limit")
	limit := int32(100) // default limit
	if limitStr != "" {
		l, err := strconv.ParseInt(limitStr, 10, 32)
		if err == nil && l > 0 {
			limit = int32(l)
		}
	}

	params := storage.ListSystemEventsParams{
		Limit: limit,
	}

	serviceName := r.URL.Query().Get("serviceName")
	if serviceName != "" {
		params.ServiceName = pgtype.Text{String: serviceName, Valid: true}
	}

	severity := r.URL.Query().Get("severity")
	if severity != "" {
		params.Severity = pgtype.Text{String: severity, Valid: true}
	}

	events, err := h.repo.ListSystemEvents(ctx, params)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to list system events", err.Error()))
		return
	}

	var respEvents []SystemEventResponse
	for _, event := range events {
		respEvent := SystemEventResponse{
			EventID:   event.ID.Bytes,
			Message:   event.Message,
			CreatedAt: event.CreatedAt.Time,
		}

		if event.TraceID.Valid {
			traceID := uuid.UUID(event.TraceID.Bytes)
			respEvent.TraceID = &traceID
		}
		if event.ServiceName.Valid {
			serviceName := event.ServiceName.String
			respEvent.ServiceName = &serviceName
		}
		if event.Severity.Valid {
			severity := event.Severity.String
			respEvent.Severity = &severity
		}
		if len(event.Metadata) > 0 {
			var metadata map[string]any
			if err := json.Unmarshal(event.Metadata, &metadata); err == nil {
				respEvent.Metadata = metadata
			}
		}

		respEvents = append(respEvents, respEvent)
	}

	if respEvents == nil {
		respEvents = []SystemEventResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"events": respEvents}))
}
