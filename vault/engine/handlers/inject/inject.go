package inject

import (
	"encoding/json"
	"net/http"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
)

// Handler serves POST /inject.
type Handler struct {
	PP      *preprocessor.Preprocessor
	Auditor *audit.Logger
}

// Inject handles POST /inject.
func (h *Handler) Inject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req InjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInjection,
		Agent:   req.Agent,
		Keys:    req.Keys,
		Message: "agent requested secret injection",
	})

	result, err := h.PP.Process(req.Agent, []byte(req.Script))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(common.ErrorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(InjectResponse{
		Agent:  req.Agent,
		Script: string(result.Script),
	})
}
