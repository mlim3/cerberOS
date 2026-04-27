package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type VaultHandler struct {
	repo    *storage.VaultRepository
	manager *logic.VaultManager
	logRepo *storage.LogRepository
}

func NewVaultHandler(repo *storage.VaultRepository, manager *logic.VaultManager, logRepo *storage.LogRepository) *VaultHandler {
	return &VaultHandler{
		repo:    repo,
		manager: manager,
		logRepo: logRepo,
	}
}

func (h *VaultHandler) validateUserExists(ctx context.Context, userUUID uuid.UUID) (bool, error) {
	return h.repo.UserExists(ctx, pgtype.UUID{Bytes: userUUID, Valid: true})
}

func (h *VaultHandler) logAccessEvent(ctx context.Context, userID, status, path string) {
	traceID, ok := traceIDFromContext(ctx)
	if !ok {
		// Non-HTTP/background callers may not carry context trace; initialize one.
		traceID = uuid.New()
	}

	eventID, _ := uuid.NewRandom()

	now := pgtype.Timestamptz{}
	now.Valid = true
	now.Time = time.Now()

	metadataBytes, _ := json.Marshal(map[string]string{
		"userId": userID,
		"path":   path,
		"status": status,
	})

	_, err := h.logRepo.CreateSystemEvent(ctx, storage.CreateSystemEventParams{
		ID:          pgtype.UUID{Bytes: eventID, Valid: true},
		TraceID:     pgtype.UUID{Bytes: traceID, Valid: true},
		ServiceName: pgtype.Text{String: "VaultService", Valid: true},
		Severity:    pgtype.Text{String: "INFO", Valid: true},
		Message:     "VAULT_ACCESS",
		Metadata:    metadataBytes,
		CreatedAt:   now,
	})

	if err != nil {
		slog.Error("failed to log vault access event", "error", err, "traceID", traceID.String())
	}
}

// HandleSaveSecret creates a new secret
// @Summary Create a secret
// @Description Saves a new encrypted secret for a user
// @Tags vault
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param userId path string true "User ID"
// @Param request body map[string]string true "Secret Payload (key_name, value)"
// @Success 201 "Created"
// @Failure 400 "Bad Request"
// @Failure 500 "Internal Server Error"
// @Router /api/v1/vault/{userId}/secrets [post]
func (h *VaultHandler) HandleSaveSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid userId format", nil))
		return
	}
	exists, err := h.validateUserExists(r.Context(), userUUID)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to validate user", nil))
		return
	}
	if !exists {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "user not found", nil))
		return
	}

	var req struct {
		KeyName string `json:"key_name"`
		Value   string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}

	if req.KeyName == "" || req.Value == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "key_name and value are required", nil))
		return
	}

	ciphertext, nonce, err := h.manager.Encrypt(req.Value)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to encrypt secret", nil))
		return
	}

	secretID, _ := uuid.NewRandom()

	params := storage.SaveSecretParams{
		ID:             pgtype.UUID{Bytes: secretID, Valid: true},
		UserID:         pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName:        req.KeyName,
		EncryptedValue: ciphertext,
		Nonce:          nonce,
	}

	if err := h.repo.SaveSecret(r.Context(), params); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to save secret", nil))
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"key_name": req.KeyName,
		"created":  true,
	}))
}

// HandleGetSecret retrieves a secret
// @Summary Get a secret
// @Description Retrieves and decrypts a secret for a user
// @Tags vault
// @Produce json
// @Security ApiKeyAuth
// @Param userId path string true "User ID"
// @Param key_name query string true "Key Name"
// @Success 200 {object} map[string]string "OK"
// @Failure 400 "Bad Request"
// @Failure 404 "Not Found"
// @Failure 500 "Internal Server Error"
// @Router /api/v1/vault/{userId}/secrets [get]
func (h *VaultHandler) HandleGetSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid userId format", nil))
		return
	}
	exists, err := h.validateUserExists(r.Context(), userUUID)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to validate user", nil))
		return
	}
	if !exists {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "user not found", nil))
		return
	}

	keyName := r.URL.Query().Get("key_name")
	if keyName == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "key_name query parameter is required", nil))
		return
	}

	secret, err := h.repo.GetSecretByKey(r.Context(), storage.GetSecretByKeyParams{
		UserID:  pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName: keyName,
	})
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "secret not found", nil))
		return
	}

	plaintext, err := h.manager.Decrypt(secret.EncryptedValue, secret.Nonce)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to decrypt secret", nil))
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]string{
		"key_name": keyName,
		"value":    plaintext,
	}))
}

// HandleUpdateSecret updates an existing secret
// @Summary Update a secret
// @Description Updates an encrypted secret for a user
// @Tags vault
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param userId path string true "User ID"
// @Param keyName path string true "Key Name"
// @Param request body map[string]string true "Secret Payload (value)"
// @Success 200 "OK"
// @Failure 400 "Bad Request"
// @Failure 500 "Internal Server Error"
// @Router /api/v1/vault/{userId}/secrets/{keyName} [put]
func (h *VaultHandler) HandleUpdateSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid userId format", nil))
		return
	}
	exists, err := h.validateUserExists(r.Context(), userUUID)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to validate user", nil))
		return
	}
	if !exists {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "user not found", nil))
		return
	}

	keyName := r.PathValue("keyName")
	if keyName == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "keyName is required", nil))
		return
	}

	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}

	if req.Value == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "value is required", nil))
		return
	}

	ciphertext, nonce, err := h.manager.Encrypt(req.Value)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to encrypt secret", nil))
		return
	}

	secretID, _ := uuid.NewRandom()

	params := storage.SaveSecretParams{
		ID:             pgtype.UUID{Bytes: secretID, Valid: true},
		UserID:         pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName:        keyName,
		EncryptedValue: ciphertext,
		Nonce:          nonce,
	}

	// SaveSecret does an upsert on conflict
	if err := h.repo.SaveSecret(r.Context(), params); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to update secret", nil))
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"key_name": keyName,
		"updated":  true,
	}))
}

// HandleDeleteSecret deletes an existing secret
// @Summary Delete a secret
// @Description Deletes an encrypted secret for a user
// @Tags vault
// @Security ApiKeyAuth
// @Param userId path string true "User ID"
// @Param keyName path string true "Key Name"
// @Success 200 "OK"
// @Failure 400 "Bad Request"
// @Failure 500 "Internal Server Error"
// @Router /api/v1/vault/{userId}/secrets/{keyName} [delete]
func (h *VaultHandler) HandleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid userId format", nil))
		return
	}
	exists, err := h.validateUserExists(r.Context(), userUUID)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to validate user", nil))
		return
	}
	if !exists {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "user not found", nil))
		return
	}

	keyName := r.PathValue("keyName")
	if keyName == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "keyName is required", nil))
		return
	}

	params := storage.DeleteSecretParams{
		UserID:  pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName: keyName,
	}

	if err := h.repo.DeleteSecret(r.Context(), params); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to delete secret", nil))
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"key_name": keyName,
		"deleted":  true,
	}))
}
