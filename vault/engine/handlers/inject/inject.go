package inject

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
)

// Handler serves POST /inject.
type Handler struct {
	PP      *preprocessor.Preprocessor
	Auditor *audit.Logger
	Logger  *slog.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/inject", h.Inject)
}

// Inject handles POST /inject.
func (h *Handler) Inject(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "secret.inject")
	start := time.Now()

	if r.Method != http.MethodPost {
		logger.Warn("rejected secret injection request: method not allowed; only POST is accepted",
			"status", http.StatusMethodNotAllowed,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req InjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("rejected secret injection request: malformed json body",
			"status", http.StatusBadRequest,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	logger = logger.With(
		"agent_id", req.Agent,
		"key_count", len(req.Keys),
	)
	logger.Info("received secret injection request from agent; resolving requested keys (values never logged)")

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInjection,
		Agent:   req.Agent,
		Keys:    req.Keys,
		Message: "agent requested secret injection",
	})

	result, err := h.PP.Process(req.Agent, []byte(req.Script))
	if err != nil {
		logger.Warn("denied secret injection: preprocessor or secret resolution failed; agent receives 403",
			"status", http.StatusForbidden,
			"error", err,
			"elapsed_ms", time.Since(start).Milliseconds())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(InjectResponse{
		Agent:  req.Agent,
		Script: string(result.Script),
	})
	logger.Info("completed secret injection; returned rendered script with credentials substituted",
		"status", http.StatusOK,
		"elapsed_ms", time.Since(start).Milliseconds())
}
