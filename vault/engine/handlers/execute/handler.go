// Package execute handles vault-delegated operation execution. It exposes
// POST /execute which accepts an OperationRequest from the orchestrator,
// resolves the required credential from SecretManager, dispatches to the
// appropriate operation provider, and returns an OperationResult.
//
// The credential value is resolved inline and must never appear in any
// OperationResult field, log message, or audit event.
package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
	"github.com/mlim3/cerberOS/vault/engine/websearch"
)

const (
	defaultTimeoutSeconds = 30
	maxTimeoutSeconds     = 300
)

// Handler routes vault-delegated operation execution requests.
type Handler struct {
	Manager  secretmanager.SecretManager
	Auditor  *audit.Logger
	searcher websearch.SearchProvider
}

// New returns a Handler wired with the given SecretManager and a default
// TavilyProvider (30 s timeout). Use NewWithSearcher in tests to inject
// a mock SearchProvider.
func New(manager secretmanager.SecretManager, auditor *audit.Logger) *Handler {
	return &Handler{
		Manager:  manager,
		Auditor:  auditor,
		searcher: websearch.NewTavilyProvider(0),
	}
}

// NewWithSearcher returns a Handler with a custom SearchProvider injected.
// Intended for unit tests only.
func NewWithSearcher(manager secretmanager.SecretManager, auditor *audit.Logger, s websearch.SearchProvider) *Handler {
	return &Handler{Manager: manager, Auditor: auditor, searcher: s}
}

// Register mounts the execute handler on the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/execute", h.Execute)
}

// Execute is the POST /execute handler. It deserialises the OperationRequest,
// validates required fields, resolves the credential, runs the operation, and
// writes an OperationResult JSON response.
//
// HTTP status is always 200 — operation-level failures are expressed via
// OperationResult.Status ("execution_error", "timed_out", "scope_violation")
// so the orchestrator can distinguish transport errors from logical failures.
func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()

	var req OperationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeResult(w, OperationResult{
			Status:       StatusExecError,
			ErrorCode:    "INVALID_REQUEST",
			ErrorMessage: "could not parse operation request",
			ElapsedMS:    time.Since(start).Milliseconds(),
		})
		return
	}

	if req.RequestID == "" || req.AgentID == "" || req.PermissionToken == "" {
		writeResult(w, OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecError,
			ErrorCode:    "INVALID_REQUEST",
			ErrorMessage: "request_id, agent_id, and permission_token are required",
			ElapsedMS:    time.Since(start).Milliseconds(),
		})
		return
	}

	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSeconds
	}
	if timeoutSec > maxTimeoutSeconds {
		timeoutSec = maxTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var result OperationResult
	result.RequestID = req.RequestID
	result.AgentID = req.AgentID

	switch req.OperationType {
	case OperationTypeWebSearch:
		result = h.handleWebSearch(ctx, req, start)
	default:
		h.Auditor.Log(audit.Event{
			Kind:    audit.KindWarning,
			Agent:   req.AgentID,
			Message: fmt.Sprintf("unsupported operation_type: %s", req.OperationType),
		})
		result = OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusScopeViolation,
			ErrorCode:    "UNSUPPORTED_OPERATION",
			ErrorMessage: fmt.Sprintf("operation type %q is not supported", req.OperationType),
			ElapsedMS:    time.Since(start).Milliseconds(),
		}
	}

	writeResult(w, result)
}

// handleWebSearch resolves the Tavily API key from SecretManager, calls the
// search provider, and returns an OperationResult. The API key is never
// included in the returned result.
func (h *Handler) handleWebSearch(ctx context.Context, req OperationRequest, start time.Time) OperationResult {
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInfo,
		Agent:   req.AgentID,
		Keys:    []string{SecretKeyTavily},
		Message: "resolving search credential for web_search operation",
	})

	secrets, err := h.Manager.GetSecrets([]string{SecretKeyTavily})
	if err != nil {
		h.Auditor.Log(audit.Event{
			Kind:    audit.KindError,
			Agent:   req.AgentID,
			Message: "failed to resolve search API credential",
			Error:   "credential resolution failed", // no internal detail in audit
		})
		return OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecError,
			ErrorCode:    "CREDENTIAL_UNAVAILABLE",
			ErrorMessage: "search API credential could not be resolved",
			ElapsedMS:    time.Since(start).Milliseconds(),
		}
	}
	apiKey := secrets[SecretKeyTavily]

	var params websearch.SearchParams
	if err := json.Unmarshal(req.OperationParams, &params); err != nil {
		return OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecError,
			ErrorCode:    "INVALID_PARAMS",
			ErrorMessage: "operation_params could not be parsed for web_search",
			ElapsedMS:    time.Since(start).Milliseconds(),
		}
	}
	if params.Query == "" {
		return OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecError,
			ErrorCode:    "INVALID_PARAMS",
			ErrorMessage: "web_search requires a non-empty query parameter",
			ElapsedMS:    time.Since(start).Milliseconds(),
		}
	}

	searchResult, err := h.searcher.Search(ctx, apiKey, params)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			h.Auditor.Log(audit.Event{
				Kind:    audit.KindWarning,
				Agent:   req.AgentID,
				Message: "web_search operation timed out",
			})
			return OperationResult{
				RequestID:    req.RequestID,
				AgentID:      req.AgentID,
				Status:       StatusTimedOut,
				ErrorCode:    "TIMEOUT",
				ErrorMessage: "web search did not complete within the allowed time",
				ElapsedMS:    elapsed,
			}
		}
		h.Auditor.Log(audit.Event{
			Kind:    audit.KindError,
			Agent:   req.AgentID,
			Message: "web_search execution failed",
			Error:   "search provider returned an error", // no provider detail exposed
		})
		return OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecError,
			ErrorCode:    "SEARCH_FAILED",
			ErrorMessage: "web search could not be completed",
			ElapsedMS:    elapsed,
		}
	}

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInfo,
		Agent:   req.AgentID,
		Message: fmt.Sprintf("web_search completed: %d results", len(searchResult.Results)),
	})

	resultBytes, err := json.Marshal(searchResult)
	if err != nil {
		return OperationResult{
			RequestID:    req.RequestID,
			AgentID:      req.AgentID,
			Status:       StatusExecError,
			ErrorCode:    "INTERNAL",
			ErrorMessage: "failed to serialize search results",
			ElapsedMS:    elapsed,
		}
	}

	return OperationResult{
		RequestID:       req.RequestID,
		AgentID:         req.AgentID,
		Status:          StatusSuccess,
		OperationResult: resultBytes,
		ElapsedMS:       elapsed,
	}
}

func writeResult(w http.ResponseWriter, result OperationResult) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}
