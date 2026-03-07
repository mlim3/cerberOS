package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/logic"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type VaultHandler struct {
	repo    *storage.VaultRepository
	manager *logic.VaultManager
}

func NewVaultHandler(repo *storage.VaultRepository, manager *logic.VaultManager) *VaultHandler {
	return &VaultHandler{
		repo:    repo,
		manager: manager,
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
		http.Error(w, "invalid userId format", http.StatusBadRequest)
		return
	}

	var req struct {
		KeyName string `json:"key_name"`
		Value   string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.KeyName == "" || req.Value == "" {
		http.Error(w, "key_name and value are required", http.StatusBadRequest)
		return
	}

	ciphertext, nonce, err := h.manager.Encrypt(req.Value)
	if err != nil {
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
		http.Error(w, "failed to save secret", http.StatusInternalServerError)
		return
	}

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
		http.Error(w, "invalid userId format", http.StatusBadRequest)
		return
	}
	
	keyName := r.URL.Query().Get("key_name")
	if keyName == "" {
		http.Error(w, "key_name query parameter is required", http.StatusBadRequest)
		return
	}

	secret, err := h.repo.GetSecretByKey(r.Context(), storage.GetSecretByKeyParams{
		UserID:  pgtype.UUID{Bytes: userUUID, Valid: true},
		KeyName: keyName,
	})
	if err != nil {
		http.Error(w, "secret not found", http.StatusNotFound)
		return
	}

	plaintext, err := h.manager.Decrypt(secret.EncryptedValue, secret.Nonce)
	if err != nil {
		http.Error(w, "failed to decrypt secret", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"key_name": keyName,
		"value":    plaintext,
	})
}
