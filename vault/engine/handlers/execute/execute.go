package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// Handler serves POST /execute.
type Handler struct {
	Manager secretmanager.SecretManager
	Auditor *audit.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/execute", h.Execute)
}

// Execute handles POST /execute.
// It resolves the per-user credential, dispatches the operation, and returns
// only the operation result — never the raw credential value.
func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.UserID == "" || req.CredentialType == "" || req.OperationType == "" {
		http.Error(w, "user_id, credential_type, and operation_type are required", http.StatusBadRequest)
		return
	}

	start := time.Now()

	// Resolve per-user credential. Audit the key name only — never the value.
	credKey := fmt.Sprintf("users/%s/credentials/%s", req.UserID, req.CredentialType)
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{credKey},
		Agent:   req.AgentID,
		Message: "vault execute: resolving credential",
	})

	secrets, err := h.Manager.GetSecrets([]string{credKey})
	if err != nil {
		elapsed := time.Since(start).Milliseconds()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusScopeViolation,
			ErrorCode:    ErrCodeMissingCredential,
			ErrorMessage: "credential not found or access denied",
			ElapsedMS:    elapsed,
		})
		return
	}

	credential := secrets[credKey]

	// Build a context with the caller's timeout (default 30s, hard cap 300s).
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}
	opCtx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	// Dispatch operation — credential is used here and goes no further.
	res := dispatchOperation(opCtx, req.OperationType, credential, req.OperationParams)
	elapsed := time.Since(start).Milliseconds()

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInjection,
		Keys:    []string{credKey},
		Agent:   req.AgentID,
		Message: fmt.Sprintf("vault execute: operation %s completed", req.OperationType),
	})

	w.Header().Set("Content-Type", "application/json")

	if res.err != nil {
		status := StatusExecutionError
		if opCtx.Err() != nil {
			status = StatusTimedOut
		}
		json.NewEncoder(w).Encode(ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       status,
			ErrorCode:    res.code,
			ErrorMessage: res.err.Error(),
			ElapsedMS:    elapsed,
		})
		return
	}

	json.NewEncoder(w).Encode(ExecuteResponse{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          StatusSuccess,
		OperationResult: res.result,
		ElapsedMS:       elapsed,
	})
}
