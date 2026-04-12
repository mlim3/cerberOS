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

type CreateMessageRequest struct {
	UserID         uuid.UUID  `json:"userId"`
	Role           string     `json:"role"`
	Content        string     `json:"content"`
	TokenCount     *int32     `json:"tokenCount,omitempty"`
	IdempotencyKey *uuid.UUID `json:"idempotencyKey,omitempty"`
}

type MessageResponse struct {
	MessageID  uuid.UUID `json:"messageId"`
	SessionID  uuid.UUID `json:"sessionId"`
	UserID     uuid.UUID `json:"userId"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	TokenCount *int32    `json:"tokenCount,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// HandleCreateMessage creates a new chat message
// @Summary Create a new chat message
// @Description Creates a new message in a chat session
// @Tags chat
// @Accept json
// @Produce json
// @Param sessionId path string true "Session ID"
// @Param request body CreateMessageRequest true "Message Payload"
// @Success 201 {object} MessageResponse "Created"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/chat/{sessionId}/messages [post]
func (h *ChatHandler) HandleCreateMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionIDStr := r.PathValue("sessionId")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid sessionId format", nil))
		return
	}

	var req CreateMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid request payload", nil))
		return
	}

	// Validate required fields
	if req.UserID == uuid.Nil || req.Role == "" || req.Content == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId, role and content are required", nil))
		return
	}

	if req.Role != "user" && req.Role != "assistant" && req.Role != "system" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "role must be user, assistant, or system", nil))
		return
	}

	userExists, err := h.repo.UserExists(ctx, pgtype.UUID{Bytes: req.UserID, Valid: true})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to validate user", err.Error()))
		return
	}
	if !userExists {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "user not found", nil))
		return
	}

	if err := h.repo.ValidateSessionOwnership(ctx, pgtype.UUID{Bytes: sessionID, Valid: true}, pgtype.UUID{Bytes: req.UserID, Valid: true}); err != nil {
		if errors.Is(err, storage.ErrSessionOwnership) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(ErrorResponse("not_found", "session not found", nil))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to validate session ownership", err.Error()))
		return
	}

	messageID, err := uuid.NewV7()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to generate message ID", nil))
		return
	}

	params := storage.CreateChatMessageParams{
		ID:        pgtype.UUID{Bytes: messageID, Valid: true},
		SessionID: pgtype.UUID{Bytes: sessionID, Valid: true},
		UserID:    pgtype.UUID{Bytes: req.UserID, Valid: true},
		Role:      req.Role,
		Content:   req.Content,
		CreatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(ErrorResponse("conflict", "idempotency key conflicts with different payload", nil))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to create message", err.Error()))
		return
	}

	var tokenCount *int32
	if msg.TokenCount.Valid {
		tokenCount = &msg.TokenCount.Int32
	}

	resp := MessageResponse{
		MessageID:  msg.ID.Bytes,
		SessionID:  msg.SessionID.Bytes,
		UserID:     msg.UserID.Bytes,
		Role:       msg.Role,
		Content:    msg.Content,
		TokenCount: tokenCount,
		CreatedAt:  msg.CreatedAt.Time,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"message": resp}))
}

// HandleListMessages lists messages in a chat session
// @Summary List chat messages
// @Description Retrieves a list of messages for a specific chat session
// @Tags chat
// @Produce json
// @Param sessionId path string true "Session ID"
// @Param limit query int false "Limit number of messages (default: 100)"
// @Success 200 {object} map[string][]MessageResponse "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/chat/{sessionId}/messages [get]
func (h *ChatHandler) HandleListMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionIDStr := r.PathValue("sessionId")
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid sessionId format", nil))
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := int32(100) // default limit
	if limitStr != "" {
		l, err := strconv.ParseInt(limitStr, 10, 32)
		if err == nil && l > 0 {
			limit = int32(l)
		}
	}

	messages, err := h.repo.ListMessagesBySession(ctx, pgtype.UUID{Bytes: sessionID, Valid: true}, limit)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to list messages", err.Error()))
		return
	}

	var respMessages []MessageResponse
	for _, msg := range messages {
		var tokenCount *int32
		if msg.TokenCount.Valid {
			tokenCount = &msg.TokenCount.Int32
		}

		respMessages = append(respMessages, MessageResponse{
			MessageID:  msg.ID.Bytes,
			SessionID:  msg.SessionID.Bytes,
			UserID:     msg.UserID.Bytes,
			Role:       msg.Role,
			Content:    msg.Content,
			TokenCount: tokenCount,
			CreatedAt:  msg.CreatedAt.Time,
		})
	}

	if respMessages == nil {
		respMessages = []MessageResponse{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"messages": respMessages}))
}
