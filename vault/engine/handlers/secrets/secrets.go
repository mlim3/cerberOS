package secrets

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// Handler serves POST /secrets/get, /secrets/put, and /secrets/delete.
type Handler struct {
	Manager secretmanager.SecretManager
	Auditor *audit.Logger
	Logger  *slog.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/secrets/get", h.SecretGet)
	mux.HandleFunc("/secrets/put", h.SecretPut)
	mux.HandleFunc("/secrets/delete", h.SecretDelete)
}

// SecretGet handles POST /secrets/get.
func (h *Handler) SecretGet(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "secret.get")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected secret read request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SecretGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected secret read request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	logger = logger.With(
		"agent_id", req.AgentID,
		"key_count", len(req.Keys),
	)
	logger.Info("received direct secret read request; resolving keys (values never logged)")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    req.Keys,
		Agent:   req.AgentID,
		Message: "direct secret read requested",
	})

	secrets, err := h.Manager.GetSecrets(req.Keys)
	if err != nil {
		logger.Warn("denied secret read: one or more keys could not be resolved or access is denied",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SecretGetResponse{Secrets: secrets})
	logger.Info("completed secret read request; returned resolved values to caller",
		"status", http.StatusOK,
		"resolved_key_count", len(secrets),
		"elapsed_ms", time.Since(start).Milliseconds())
}

// SecretPut handles POST /secrets/put.
func (h *Handler) SecretPut(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "secret.put")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected secret write request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SecretPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected secret write request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	logger = logger.With("key_name", req.Key)
	logger.Info("received secret write request; persisting value to backing store (value not logged)")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{req.Key},
		Message: "secret written",
	})

	if err := h.Manager.PutSecret(r.Context(), req.Key, req.Value); err != nil {
		logger.Warn("denied secret write: backing store rejected the put or access is denied",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
	logger.Info("completed secret write; value persisted to backing store",
		"status", http.StatusNoContent,
		"elapsed_ms", time.Since(start).Milliseconds())
}

// SecretDelete handles POST /secrets/delete.
func (h *Handler) SecretDelete(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "secret.delete")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected secret delete request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SecretDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected secret delete request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	logger = logger.With("key_name", req.Key)
	logger.Info("received secret delete request; removing value from backing store")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{req.Key},
		Message: "agent deleted secret",
	})

	if err := h.Manager.DeleteSecret(r.Context(), req.Key); err != nil {
		logger.Warn("denied secret delete: backing store rejected the delete or access is denied",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
	logger.Info("completed secret delete; value removed from backing store",
		"status", http.StatusNoContent,
		"elapsed_ms", time.Since(start).Milliseconds())
}
