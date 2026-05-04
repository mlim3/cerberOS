package credentials

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// credentialKey builds the per-user vault path for a credential type.
func credentialKey(userID, credentialType string) string {
	return fmt.Sprintf("users/%s/credentials/%s", userID, credentialType)
}

// Handler serves POST /credentials/put, /credentials/get, and /credentials/delete.
type Handler struct {
	Manager secretmanager.SecretManager
	Auditor *audit.Logger
	Logger  *slog.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/credentials/put", h.CredentialPut)
	mux.HandleFunc("/credentials/get", h.CredentialGet)
	mux.HandleFunc("/credentials/delete", h.CredentialDelete)
}

// CredentialPut handles POST /credentials/put.
func (h *Handler) CredentialPut(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "credential.put")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected credential write request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CredentialPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected credential write request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.CredentialType == "" {
		logger.Warn("rejected credential write request: user_id and credential_type are required",
			"status", http.StatusBadRequest,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "user_id and credential_type are required", http.StatusBadRequest)
		return
	}

	key := credentialKey(req.UserID, req.CredentialType)
	logger = logger.With(
		"user_id", req.UserID,
		"credential_type", req.CredentialType,
		"key_name", key,
	)
	logger.Info("received user credential write request from io; persisting to vault (value not logged)")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{key},
		Message: "user credential written",
	})

	if err := h.Manager.PutSecret(r.Context(), key, req.Value); err != nil {
		logger.Warn("denied credential write: vault backing store rejected the put",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
	logger.Info("completed user credential write; value persisted under per-user vault path",
		"status", http.StatusNoContent,
		"elapsed_ms", time.Since(start).Milliseconds())
}

// CredentialGet handles POST /credentials/get (internal use only — not exposed to end users).
func (h *Handler) CredentialGet(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "credential.get")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected credential read request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CredentialGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected credential read request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.CredentialType == "" {
		logger.Warn("rejected credential read request: user_id and credential_type are required",
			"status", http.StatusBadRequest,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "user_id and credential_type are required", http.StatusBadRequest)
		return
	}

	key := credentialKey(req.UserID, req.CredentialType)
	logger = logger.With(
		"user_id", req.UserID,
		"credential_type", req.CredentialType,
		"key_name", key,
	)
	logger.Info("received internal credential read request; resolving from vault (value never logged)")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{key},
		Message: "user credential read",
	})

	secrets, err := h.Manager.GetSecrets([]string{key})
	if err != nil {
		logger.Warn("denied credential read: vault backing store rejected the get or no value at this path",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CredentialGetResponse{Value: secrets[key]})
	logger.Info("completed internal credential read; returned value to caller (value not logged)",
		"status", http.StatusOK,
		"elapsed_ms", time.Since(start).Milliseconds())
}

// CredentialDelete handles POST /credentials/delete.
func (h *Handler) CredentialDelete(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "credential.delete")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected credential delete request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CredentialDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected credential delete request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.CredentialType == "" {
		logger.Warn("rejected credential delete request: user_id and credential_type are required",
			"status", http.StatusBadRequest,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "user_id and credential_type are required", http.StatusBadRequest)
		return
	}

	key := credentialKey(req.UserID, req.CredentialType)
	logger = logger.With(
		"user_id", req.UserID,
		"credential_type", req.CredentialType,
		"key_name", key,
	)
	logger.Info("received user credential delete request; removing value from vault")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{key},
		Message: "user credential deleted",
	})

	if err := h.Manager.DeleteSecret(r.Context(), key); err != nil {
		logger.Warn("denied credential delete: vault backing store rejected the delete",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
	logger.Info("completed user credential delete; value removed from vault",
		"status", http.StatusNoContent,
		"elapsed_ms", time.Since(start).Milliseconds())
}
