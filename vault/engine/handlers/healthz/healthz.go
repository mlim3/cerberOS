package healthz

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers/common"
)

type Handler struct {
	Auditor *audit.Logger
	Logger  *slog.Logger
}

// Register mounts this handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.Healthz)
}

// Healthz serves GET /healthz.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	logger, _ := common.RequestLogger(h.Logger, r, "health.check")
	start := time.Now()

	h.Auditor.Log(audit.Event{
		Kind:    audit.KindInfo,
		Message: "healthz request received",
		Time:    time.Now(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(HealthzResponse{Status: "ok"})

	logger.Debug("served vault healthz probe; reporting ok",
		"status", http.StatusOK,
		"elapsed_ms", time.Since(start).Milliseconds())
}
