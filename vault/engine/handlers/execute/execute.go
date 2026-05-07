package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

// Handler serves POST /execute.
type Handler struct {
	Manager secretmanager.SecretManager
	Auditor *audit.Logger
	Logger  *slog.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/execute", h.Execute)
}

// Execute handles POST /execute.
// It resolves the per-user credential, dispatches the operation, and returns
// only the operation result — never the raw credential value.
func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "credential.execute")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected credential execute request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected credential execute request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.UserID == "" || req.CredentialType == "" || req.OperationType == "" {
		logger.Warn("rejected credential execute request: user_id, credential_type, and operation_type are required",
			"status", http.StatusBadRequest,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "user_id, credential_type, and operation_type are required", http.StatusBadRequest)
		return
	}

	credKey := fmt.Sprintf("users/%s/credentials/%s", req.UserID, req.CredentialType)
	if req.RequestID != "" {
		logger = logger.With("request_id", req.RequestID)
	}
	logger = logger.With(
		"user_id", req.UserID,
		"agent_id", req.AgentID,
		"task_id", req.TaskID,
		"credential_type", req.CredentialType,
		"vault_op", req.OperationType,
		"key_name", credKey,
	)
	logger.Info("received credentialed execute request from agent; resolving credential and dispatching operation (value never logged)")

	// Resolve per-user credential. Audit the key name only — never the value.
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindSecretAccess,
		Keys:    []string{credKey},
		Agent:   req.AgentID,
		Message: "vault execute: resolving credential",
	})

	secrets, err := h.Manager.GetSecrets([]string{credKey})
	if err != nil {
		elapsed := time.Since(start).Milliseconds()
		logger.Warn("denied credential execute: per-user credential could not be resolved or access is denied",
			"status", http.StatusForbidden,
			"error_code", ErrCodeMissingCredential,
			"error", err,
			"elapsed_ms", elapsed)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(ExecuteResponse{
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
		logger.Warn("credentialed execute completed with operation error; returning error response to agent (operation result not logged)",
			"status", status,
			"error_code", res.code,
			"timed_out", opCtx.Err() != nil,
			"elapsed_ms", elapsed)
		_ = json.NewEncoder(w).Encode(ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       status,
			ErrorCode:    res.code,
			ErrorMessage: res.err.Error(),
			ElapsedMS:    elapsed,
		})
		return
	}

	_ = json.NewEncoder(w).Encode(ExecuteResponse{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          StatusSuccess,
		OperationResult: res.result,
		ElapsedMS:       elapsed,
	})
	logger.Info("completed credentialed execute successfully; returning result to agent (result body not logged)",
		"status", StatusSuccess,
		"elapsed_ms", elapsed)
}
