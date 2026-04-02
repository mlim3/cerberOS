package healthz

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
)

type Handler struct {
	Auditor *audit.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.Healthz)
}

// Healthz serves GET /healthz.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInfo,
		Message: "healthz request received",
		Time:    time.Now(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(HealthzResponse{Status: "ok"})
}
