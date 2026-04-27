package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type ChatHandler struct {
	repo *storage.ChatRepository
}

func NewChatHandler(repo *storage.ChatRepository) *ChatHandler {
	return &ChatHandler{
		repo: repo,
	}
}

type CreateConversationRequest struct {
	UserID         uuid.UUID  `json:"userId"`
	ConversationID *uuid.UUID `json:"conversationId,omitempty"`
	Title          string     `json:"title,omitempty"`
}

type ConversationResponse struct {
	ConversationID     uuid.UUID `json:"conversationId"`
	UserID             uuid.UUID `json:"userId"`
	Title              string    `json:"title"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
	LastMessagePreview string    `json:"lastMessagePreview,omitempty"`
	MessageCount       int32     `json:"messageCount"`
	LatestTaskID       *uuid.UUID `json:"latestTaskId,omitempty"`
	LatestTaskStatus   *string    `json:"latestTaskStatus,omitempty"`
}

type CreateTaskRequest struct {
	UserID              uuid.UUID  `json:"userId"`
	TaskID              *uuid.UUID `json:"taskId,omitempty"`
	ConversationID      *uuid.UUID `json:"conversationId,omitempty"`
	Title               string     `json:"title,omitempty"`
	OrchestratorTaskRef string     `json:"orchestratorTaskRef,omitempty"`
	TraceID             string     `json:"traceId,omitempty"`
	Status              string     `json:"status,omitempty"`
	InputSummary        string     `json:"inputSummary,omitempty"`
}

type TaskResponse struct {
	TaskID              uuid.UUID  `json:"taskId"`
	ConversationID      uuid.UUID  `json:"conversationId"`
	UserID              uuid.UUID  `json:"userId"`
	OrchestratorTaskRef *string    `json:"orchestratorTaskRef,omitempty"`
	TraceID             *string    `json:"traceId,omitempty"`
	Status              string     `json:"status"`
	InputSummary        *string    `json:"inputSummary,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	CompletedAt         *time.Time `json:"completedAt,omitempty"`
}

type CreateMessageRequest struct {
	UserID         uuid.UUID  `json:"userId"`
	Role           string     `json:"role"`
	Content        string     `json:"content"`
	TokenCount     *int32     `json:"tokenCount,omitempty"`
	IdempotencyKey *uuid.UUID `json:"idempotencyKey,omitempty"`
}

type MessageResponse struct {
	MessageID      uuid.UUID `json:"messageId"`
	ConversationID uuid.UUID `json:"conversationId"`
	UserID         uuid.UUID `json:"userId"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	TokenCount     *int32    `json:"tokenCount,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// HandleListConversations lists conversations for a user.
// @Summary List conversations
// @Description Retrieves conversation summaries for a specific user
// @Tags chat
// @Produce json
// @Param userId query string true "User ID"
// @Param limit query int false "Limit number of conversations (default: 100)"
// @Success 200 {object} map[string][]ConversationResponse "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/conversations [get]
func (h *ChatHandler) HandleListConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := parseUserIDQuery(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "userId query parameter is required", nil)
		return
	}

	exists, err := h.repo.UserExists(ctx, pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to validate user", err.Error())
		return
	}
	if !exists {
		writeJSONError(w, http.StatusNotFound, "not_found", "user not found", nil)
		return
	}

	limit := parseLimit(r.URL.Query().Get("limit"), 100)
	conversations, err := h.repo.ListConversationsByUser(ctx, pgtype.UUID{Bytes: userID, Valid: true}, limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to list conversations", err.Error())
		return
	}

	resp := make([]ConversationResponse, 0, len(conversations))
	for _, rec := range conversations {
		resp = append(resp, conversationResponse(rec))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"conversations": resp}))
}

// HandleCreateConversation creates a conversation for a user.
// @Summary Create conversation
// @Description Creates a new conversation for a specific user
// @Tags chat
// @Accept json
// @Produce json
// @Param request body CreateConversationRequest true "Conversation Payload"
// @Success 201 {object} ConversationResponse "Created"
// @Success 200 {object} ConversationResponse "Already Exists"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/conversations [post]
func (h *ChatHandler) HandleCreateConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req CreateConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid request payload", nil)
		return
	}
	if req.UserID == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "userId is required", nil)
		return
	}

	exists, err := h.repo.UserExists(ctx, pgtype.UUID{Bytes: req.UserID, Valid: true})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to validate user", err.Error())
		return
	}
	if !exists {
		writeJSONError(w, http.StatusNotFound, "not_found", "user not found", nil)
		return
	}

	conversationID := uuid.Nil
	if req.ConversationID != nil {
		conversationID = *req.ConversationID
	} else {
		conversationID, err = uuid.NewV7()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to generate conversation ID", nil)
			return
		}
	}

	rec, created, err := h.repo.CreateConversation(
		ctx,
		pgtype.UUID{Bytes: conversationID, Valid: true},
		pgtype.UUID{Bytes: req.UserID, Valid: true},
		req.Title,
		time.Now().UTC(),
	)
	if err != nil {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to create conversation"
		if errors.Is(err, storage.ErrConversationOwnership) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "conversation not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"conversation": conversationResponse(rec)}))
}

// HandleCreateTask creates a task linked to a conversation.
// @Summary Create task
// @Description Creates a new task for an existing conversation or creates a conversation first when none is provided
// @Tags chat
// @Accept json
// @Produce json
// @Param request body CreateTaskRequest true "Task Payload"
// @Success 201 {object} TaskResponse "Created"
// @Success 200 {object} TaskResponse "Already Exists"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/tasks [post]
func (h *ChatHandler) HandleCreateTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid request payload", nil)
		return
	}
	if req.UserID == uuid.Nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "userId is required", nil)
		return
	}

	exists, err := h.repo.UserExists(ctx, pgtype.UUID{Bytes: req.UserID, Valid: true})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to validate user", err.Error())
		return
	}
	if !exists {
		writeJSONError(w, http.StatusNotFound, "not_found", "user not found", nil)
		return
	}

	now := time.Now().UTC()
	conversationID := uuid.Nil
	if req.ConversationID != nil {
		conversationID = *req.ConversationID
	} else {
		conversationID, err = uuid.NewV7()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to generate conversation ID", nil)
			return
		}
	}

	if _, _, err := h.repo.CreateConversation(
		ctx,
		pgtype.UUID{Bytes: conversationID, Valid: true},
		pgtype.UUID{Bytes: req.UserID, Valid: true},
		req.Title,
		now,
	); err != nil {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to create conversation"
		if errors.Is(err, storage.ErrConversationOwnership) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "conversation not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	if _, _, err := h.repo.SetConversationTitle(
		ctx,
		pgtype.UUID{Bytes: conversationID, Valid: true},
		pgtype.UUID{Bytes: req.UserID, Valid: true},
		req.Title,
		now,
	); err != nil && !errors.Is(err, storage.ErrConversationNotFound) {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to update conversation title"
		if errors.Is(err, storage.ErrConversationOwnership) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "conversation not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	taskID := uuid.Nil
	if req.TaskID != nil {
		taskID = *req.TaskID
	} else {
		taskID, err = uuid.NewV7()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to generate task ID", nil)
			return
		}
	}

	orchestratorTaskRef := textValue(req.OrchestratorTaskRef)
	traceID := textValue(req.TraceID)
	inputSummary := textValue(req.InputSummary)
	rec, created, err := h.repo.CreateTask(
		ctx,
		pgtype.UUID{Bytes: taskID, Valid: true},
		pgtype.UUID{Bytes: conversationID, Valid: true},
		pgtype.UUID{Bytes: req.UserID, Valid: true},
		orchestratorTaskRef,
		traceID,
		req.Status,
		inputSummary,
		now,
	)
	if err != nil {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to create task"
		if errors.Is(err, storage.ErrConversationOwnership) || errors.Is(err, storage.ErrConversationNotFound) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "conversation not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"task": taskResponse(rec)}))
}

// HandleGetTask returns one task owned by the given user.
// @Summary Get task
// @Description Retrieves a task and its conversation mapping for a specific user
// @Tags chat
// @Produce json
// @Param taskId path string true "Task ID"
// @Param userId query string true "User ID"
// @Success 200 {object} TaskResponse "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/tasks/{taskId} [get]
func (h *ChatHandler) HandleGetTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	taskID, err := uuid.Parse(r.PathValue("taskId"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid taskId format", nil)
		return
	}
	userID, ok := parseUserIDQuery(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "userId query parameter is required", nil)
		return
	}

	task, err := h.repo.GetTask(ctx, pgtype.UUID{Bytes: taskID, Valid: true}, pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to get task"
		if errors.Is(err, storage.ErrConversationNotFound) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "task not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"task": taskResponse(task)}))
}

// HandleCreateMessage creates a new chat message
// @Summary Create a new chat message
// @Description Creates a new message in a chat session
// @Tags chat
// @Accept json
// @Produce json
// @Param conversationId path string true "Conversation ID"
// @Param request body CreateMessageRequest true "Message Payload"
// @Success 201 {object} MessageResponse "Created"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/chat/{conversationId}/messages [post]
func (h *ChatHandler) HandleCreateMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conversationIDStr := conversationPathValue(r)
	conversationID, err := uuid.Parse(conversationIDStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid conversationId format", nil)
		return
	}

	var req CreateMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid request payload", nil)
		return
	}
	if req.UserID == uuid.Nil || req.Role == "" || req.Content == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "userId, role and content are required", nil)
		return
	}
	if req.Role != "user" && req.Role != "assistant" && req.Role != "system" {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "role must be user, assistant, or system", nil)
		return
	}

	userExists, err := h.repo.UserExists(ctx, pgtype.UUID{Bytes: req.UserID, Valid: true})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to validate user", err.Error())
		return
	}
	if !userExists {
		writeJSONError(w, http.StatusNotFound, "not_found", "user not found", nil)
		return
	}

	now := time.Now().UTC()
	if err := h.repo.EnsureConversation(
		ctx,
		pgtype.UUID{Bytes: conversationID, Valid: true},
		pgtype.UUID{Bytes: req.UserID, Valid: true},
		req.Content,
		now,
	); err != nil {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to validate conversation ownership"
		if errors.Is(err, storage.ErrConversationOwnership) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "conversation not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	messageID, err := uuid.NewV7()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to generate message ID", nil)
		return
	}

	params := storage.CreateChatMessageParams{
		ID:             pgtype.UUID{Bytes: messageID, Valid: true},
		ConversationID: pgtype.UUID{Bytes: conversationID, Valid: true},
		UserID:         pgtype.UUID{Bytes: req.UserID, Valid: true},
		Role:           req.Role,
		Content:        req.Content,
		CreatedAt:      pgtype.Timestamptz{Time: now, Valid: true},
	}
	if req.TokenCount != nil {
		params.TokenCount = pgtype.Int4{Int32: *req.TokenCount, Valid: true}
	}
	if req.IdempotencyKey != nil {
		params.IdempotencyKey = pgtype.UUID{Bytes: *req.IdempotencyKey, Valid: true}
	}

	msg, err := h.repo.CreateMessage(ctx, params)
	if err != nil {
		if errors.Is(err, storage.ErrIdempotencyConflict) {
			writeJSONError(w, http.StatusConflict, "conflict", "idempotency key conflicts with different payload", nil)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to create message", err.Error())
		return
	}
	if err := h.repo.TouchConversation(ctx, pgtype.UUID{Bytes: conversationID, Valid: true}, now); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to update conversation metadata", err.Error())
		return
	}

	resp := messageResponse(msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"message": resp}))
}

// HandleListMessages lists messages in a chat session
// @Summary List chat messages
// @Description Retrieves a list of messages for a specific chat session
// @Tags chat
// @Produce json
// @Param conversationId path string true "Conversation ID"
// @Param userId query string true "User ID"
// @Param limit query int false "Limit number of messages (default: 100)"
// @Success 200 {object} map[string][]MessageResponse "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 404 {object} map[string]interface{} "Not Found"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/chat/{conversationId}/messages [get]
func (h *ChatHandler) HandleListMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	conversationIDStr := conversationPathValue(r)
	conversationID, err := uuid.Parse(conversationIDStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "invalid conversationId format", nil)
		return
	}
	userID, ok := parseUserIDQuery(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid_argument", "userId query parameter is required", nil)
		return
	}

	userExists, err := h.repo.UserExists(ctx, pgtype.UUID{Bytes: userID, Valid: true})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to validate user", err.Error())
		return
	}
	if !userExists {
		writeJSONError(w, http.StatusNotFound, "not_found", "user not found", nil)
		return
	}

	if err := h.repo.ValidateConversationOwnership(ctx, pgtype.UUID{Bytes: conversationID, Valid: true}, pgtype.UUID{Bytes: userID, Valid: true}); err != nil {
		status := http.StatusInternalServerError
		code := "internal"
		msg := "failed to validate conversation ownership"
		if errors.Is(err, storage.ErrConversationOwnership) || errors.Is(err, storage.ErrConversationNotFound) {
			status = http.StatusNotFound
			code = "not_found"
			msg = "conversation not found"
		}
		writeJSONError(w, status, code, msg, err.Error())
		return
	}

	messages, err := h.repo.ListMessagesByConversation(ctx, pgtype.UUID{Bytes: conversationID, Valid: true}, parseLimit(r.URL.Query().Get("limit"), 100))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "failed to list messages", err.Error())
		return
	}

	respMessages := make([]MessageResponse, 0, len(messages))
	for _, msg := range messages {
		respMessages = append(respMessages, messageResponse(msg))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"messages": respMessages}))
}

func messageResponse(msg storage.ChatSchemaMessage) MessageResponse {
	var tokenCount *int32
	if msg.TokenCount.Valid {
		tokenCount = &msg.TokenCount.Int32
	}
	return MessageResponse{
		MessageID:      msg.ID.Bytes,
		ConversationID: msg.ConversationID.Bytes,
		UserID:         msg.UserID.Bytes,
		Role:           msg.Role,
		Content:        msg.Content,
		TokenCount:     tokenCount,
		CreatedAt:      msg.CreatedAt.Time,
	}
}

func conversationResponse(rec storage.ConversationRecord) ConversationResponse {
	var latestTaskID *uuid.UUID
	if rec.LatestTaskID.Valid {
		v := uuid.UUID(rec.LatestTaskID.Bytes)
		latestTaskID = &v
	}
	var latestTaskStatus *string
	if rec.LatestTaskStatus.Valid {
		v := rec.LatestTaskStatus.String
		latestTaskStatus = &v
	}
	return ConversationResponse{
		ConversationID:     rec.ID.Bytes,
		UserID:             rec.UserID.Bytes,
		Title:              rec.Title,
		CreatedAt:          rec.CreatedAt.Time,
		UpdatedAt:          rec.UpdatedAt.Time,
		LastMessagePreview: rec.LastMessagePreview,
		MessageCount:       rec.MessageCount,
		LatestTaskID:       latestTaskID,
		LatestTaskStatus:   latestTaskStatus,
	}
}

func taskResponse(rec storage.TaskRecord) TaskResponse {
	var orchestratorTaskRef *string
	if rec.OrchestratorTaskRef.Valid {
		v := rec.OrchestratorTaskRef.String
		orchestratorTaskRef = &v
	}
	var traceID *string
	if rec.TraceID.Valid {
		v := rec.TraceID.String
		traceID = &v
	}
	var inputSummary *string
	if rec.InputSummary.Valid {
		v := rec.InputSummary.String
		inputSummary = &v
	}
	var completedAt *time.Time
	if rec.CompletedAt.Valid {
		v := rec.CompletedAt.Time
		completedAt = &v
	}
	return TaskResponse{
		TaskID:              rec.ID.Bytes,
		ConversationID:      rec.ConversationID.Bytes,
		UserID:              rec.UserID.Bytes,
		OrchestratorTaskRef: orchestratorTaskRef,
		TraceID:             traceID,
		Status:              rec.Status,
		InputSummary:        inputSummary,
		CreatedAt:           rec.CreatedAt.Time,
		UpdatedAt:           rec.UpdatedAt.Time,
		CompletedAt:         completedAt,
	}
}

func parseLimit(raw string, def int32) int32 {
	limit := def
	if raw == "" {
		return limit
	}
	l, err := strconv.ParseInt(raw, 10, 32)
	if err == nil && l > 0 {
		limit = int32(l)
	}
	return limit
}

func parseUserIDQuery(r *http.Request) (uuid.UUID, bool) {
	raw := r.URL.Query().Get("userId")
	if raw == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func writeJSONError(w http.ResponseWriter, status int, code, message string, details any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse(code, message, details))
}

func textValue(v string) pgtype.Text {
	if v == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: v, Valid: true}
}

func conversationPathValue(r *http.Request) string {
	if v := r.PathValue("conversationId"); v != "" {
		return v
	}
	return r.PathValue("sessionId")
}
