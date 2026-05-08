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
	"github.com/mlim3/cerberOS/vault/engine/websearch"
)

// Handler serves POST /execute.
type Handler struct {
	Manager  secretmanager.SecretManager
	Auditor  *audit.Logger
	Logger   *slog.Logger
	searcher websearch.SearchProvider
}

// New returns a Handler wired with the default Tavily search provider.
func New(manager secretmanager.SecretManager, auditor *audit.Logger) *Handler {
	return &Handler{Manager: manager, Auditor: auditor, searcher: websearch.NewTavilyProvider(0)}
}

// NewWithSearcher returns a Handler with a custom SearchProvider injected.
// Intended for unit tests only.
func NewWithSearcher(manager secretmanager.SecretManager, auditor *audit.Logger, s websearch.SearchProvider) *Handler {
	return &Handler{Manager: manager, Auditor: auditor, searcher: s}
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
		elapsed := time.Since(start).Milliseconds()
		logger.Warn("rejected credential execute request: malformed json body",
			"status", StatusExecutionError,
			"error_code", ErrCodeInvalidParams,
			"error", err,
			"elapsed_ms", elapsed)
		writeExecuteError(w, ExecuteResponse{
			Status:       StatusExecutionError,
			ErrorCode:    ErrCodeInvalidParams,
			ErrorMessage: "invalid request body",
			ElapsedMS:    elapsed,
		})
		return
	}

	req.CredentialType = defaultCredentialType(req.OperationType, req.CredentialType)
	if req.OperationType == "" {
		elapsed := time.Since(start).Milliseconds()
		logger.Warn("rejected credential execute request: operation_type is required",
			"status", StatusExecutionError,
			"error_code", ErrCodeInvalidParams,
			"elapsed_ms", elapsed)
		writeExecuteError(w, ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecutionError,
			ErrorCode:    ErrCodeInvalidParams,
			ErrorMessage: "operation_type is required",
			ElapsedMS:    elapsed,
		})
		return
	}
	if req.RequestID == "" || req.AgentID == "" || req.PermissionToken == "" {
		elapsed := time.Since(start).Milliseconds()
		logger.Warn("rejected credential execute request: request_id, agent_id, and permission_token are required",
			"status", StatusExecutionError,
			"error_code", ErrCodeInvalidParams,
			"elapsed_ms", elapsed)
		writeExecuteError(w, ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecutionError,
			ErrorCode:    ErrCodeInvalidParams,
			ErrorMessage: "request_id, agent_id, and permission_token are required",
			ElapsedMS:    elapsed,
		})
		return
	}
	if !isSupportedOperation(req.OperationType) {
		elapsed := time.Since(start).Milliseconds()
		logger.Warn("rejected credential execute request: unsupported operation type",
			"status", StatusScopeViolation,
			"error_code", ErrCodeUnsupportedOp,
			"vault_op", req.OperationType,
			"elapsed_ms", elapsed)
		writeExecuteError(w, ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusScopeViolation,
			ErrorCode:    ErrCodeUnsupportedOp,
			ErrorMessage: "unsupported operation type",
			ElapsedMS:    elapsed,
		})
		return
	}

	credKey := credentialSecretKey(req.UserID, req.CredentialType)
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
		logger.Warn("denied credential execute: credential could not be resolved or access is denied",
			"status", StatusExecutionError,
			"error_code", ErrCodeCredentialUnavailable,
			"error", err,
			"elapsed_ms", elapsed)
		writeExecuteError(w, ExecuteResponse{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecutionError,
			ErrorCode:    ErrCodeCredentialUnavailable,
			ErrorMessage: "credential unavailable",
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
	res := dispatchOperation(opCtx, req.OperationType, credential, req.OperationParams, h.searcher)
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

func writeExecuteError(w http.ResponseWriter, resp ExecuteResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func defaultCredentialType(operationType, credentialType string) string {
	if credentialType != "" {
		return credentialType
	}
	if operationType == OperationTypeWebSearch {
		return CredentialTypeSearchAPIKey
	}
	return credentialType
}

func credentialSecretKey(userID, credentialType string) string {
	if userID != "" {
		return fmt.Sprintf("users/%s/credentials/%s", userID, credentialType)
	}
	if credentialType == CredentialTypeSearchAPIKey {
		return SecretKeyTavily
	}
	return credentialType
}

func isSupportedOperation(operationType string) bool {
	switch operationType {
	case "vault_google_search",
		"vault_github_request",
		"vault_web_fetch",
		OperationTypeWebSearch,
		"vault_data_read",
		"vault_data_write",
		"vault_comms_send",
		"vault_storage_read",
		"vault_storage_write",
		"vault_storage_list":
		return true
	default:
		return false
	}
}
