package credentials

import (
	"encoding/json"
	"fmt"
	"net/http"

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
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/credentials/put", h.CredentialPut)
	mux.HandleFunc("/credentials/get", h.CredentialGet)
	mux.HandleFunc("/credentials/delete", h.CredentialDelete)
}

// CredentialPut handles POST /credentials/put.
func (h *Handler) CredentialPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CredentialPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.CredentialType == "" {
		http.Error(w, "user_id and credential_type are required", http.StatusBadRequest)
		return
	}

	key := credentialKey(req.UserID, req.CredentialType)
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{key},
		Message: "user credential written",
	})

	if err := h.Manager.PutSecret(r.Context(), key, req.Value); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CredentialGet handles POST /credentials/get (internal use only — not exposed to end users).
func (h *Handler) CredentialGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CredentialGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.CredentialType == "" {
		http.Error(w, "user_id and credential_type are required", http.StatusBadRequest)
		return
	}

	key := credentialKey(req.UserID, req.CredentialType)
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{key},
		Message: "user credential read",
	})

	secrets, err := h.Manager.GetSecrets([]string{key})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CredentialGetResponse{Value: secrets[key]})
}

// CredentialDelete handles POST /credentials/delete.
func (h *Handler) CredentialDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CredentialDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.CredentialType == "" {
		http.Error(w, "user_id and credential_type are required", http.StatusBadRequest)
		return
	}

	key := credentialKey(req.UserID, req.CredentialType)
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{key},
		Message: "user credential deleted",
	})

	if err := h.Manager.DeleteSecret(r.Context(), key); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
