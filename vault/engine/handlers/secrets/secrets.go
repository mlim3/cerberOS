package secrets

import (
	"encoding/json"
	"net/http"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// Handler serves POST /secrets/get, /secrets/put, and /secrets/delete.
type Handler struct {
	Manager secretmanager.SecretManager
	Auditor *audit.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/secrets/get", h.SecretGet)
	mux.HandleFunc("/secrets/put", h.SecretPut)
	mux.HandleFunc("/secrets/delete", h.SecretDelete)
}

// SecretGet handles POST /secrets/get.
func (h *Handler) SecretGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SecretGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    req.Keys,
		Agent:   req.AgentID,
		Message: "direct secret read requested",
	})

	secrets, err := h.Manager.GetSecrets(req.Keys)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SecretGetResponse{Secrets: secrets})
}

// SecretPut handles POST /secrets/put.
func (h *Handler) SecretPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SecretPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{req.Key},
		Message: "secret written",
	})

	if err := h.Manager.PutSecret(r.Context(), req.Key, req.Value); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SecretDelete handles POST /secrets/delete.
func (h *Handler) SecretDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SecretDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{req.Key},
		Message: "agent deleted secret",
	})

	if err := h.Manager.DeleteSecret(r.Context(), req.Key); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
