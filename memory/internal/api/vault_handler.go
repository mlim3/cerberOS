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

func (h *VaultHandler) logAccessEvent(ctx context.Context, userID, status, path string) {
	traceIDStr, ok := ctx.Value(TraceIDKey{}).(string)
	if !ok || traceIDStr == "" {
		return // No trace ID, cannot log properly
	}

	eventID, _ := uuid.NewRandom()
	traceUUID, _ := uuid.Parse(traceIDStr)

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
		TraceID:     pgtype.UUID{Bytes: traceUUID, Valid: true},
		ServiceName: pgtype.Text{String: "VaultService", Valid: true},
		Severity:    pgtype.Text{String: "INFO", Valid: true},
		Message:     "VAULT_ACCESS",
		Metadata:    metadataBytes,
		CreatedAt:   now,
	})

	if err != nil {
		slog.Error("failed to log vault access event", "error", err, "traceID", traceIDStr)
	}
}

func (h *VaultHandler) HandleSaveSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "invalid userId format", http.StatusBadRequest)
		return
	}

	var req struct {
		KeyName string `json:"key_name"`
		Value   string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.KeyName == "" || req.Value == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "key_name and value are required", http.StatusBadRequest)
		return
	}

	ciphertext, nonce, err := h.manager.Encrypt(req.Value)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "failed to encrypt secret", http.StatusInternalServerError)
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
		http.Error(w, "failed to save secret", http.StatusInternalServerError)
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.WriteHeader(http.StatusCreated)
}

func (h *VaultHandler) HandleGetSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "invalid userId format", http.StatusBadRequest)
		return
	}

	keyName := r.URL.Query().Get("key_name")
	if keyName == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "key_name query parameter is required", http.StatusBadRequest)
		return
	}

	secret, err := h.repo.GetSecretByKey(r.Context(), storage.GetSecretByKeyParams{
		UserID:  pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName: keyName,
	})
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "secret not found", http.StatusNotFound)
		return
	}

	plaintext, err := h.manager.Decrypt(secret.EncryptedValue, secret.Nonce)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "failed to decrypt secret", http.StatusInternalServerError)
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"key_name": keyName,
		"value":    plaintext,
	})
}

func (h *VaultHandler) HandleUpdateSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "invalid userId format", http.StatusBadRequest)
		return
	}

	keyName := r.PathValue("keyName")
	if keyName == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "keyName is required", http.StatusBadRequest)
		return
	}

	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Value == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "value is required", http.StatusBadRequest)
		return
	}

	ciphertext, nonce, err := h.manager.Encrypt(req.Value)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "failed to encrypt secret", http.StatusInternalServerError)
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
		http.Error(w, "failed to update secret", http.StatusInternalServerError)
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.WriteHeader(http.StatusOK)
}

func (h *VaultHandler) HandleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	userIdStr := r.PathValue("userId")
	if userIdStr == "" {
		http.Error(w, "userId is required", http.StatusBadRequest)
		return
	}

	userUUID, err := uuid.Parse(userIdStr)
	if err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "invalid userId format", http.StatusBadRequest)
		return
	}

	keyName := r.PathValue("keyName")
	if keyName == "" {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "keyName is required", http.StatusBadRequest)
		return
	}

	params := storage.DeleteSecretParams{
		UserID:  pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName: keyName,
	}

	if err := h.repo.DeleteSecret(r.Context(), params); err != nil {
		h.logAccessEvent(r.Context(), userIdStr, "denied", r.URL.Path)
		http.Error(w, "failed to delete secret", http.StatusInternalServerError)
		return
	}

	h.logAccessEvent(r.Context(), userIdStr, "granted", r.URL.Path)
	w.WriteHeader(http.StatusOK)
}
