package heartbeat

import (
	"encoding/json"
	"net/http"
	"time"
)

// HTTPHandler renders the current heartbeat snapshot as JSON on
// GET /heartbeats.
type HTTPHandler struct {
	Sweeper *Sweeper
}

// ServeHTTP implements http.Handler.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Sweeper == nil {
		http.Error(w, "sweeper not configured", http.StatusServiceUnavailable)
		return
	}

	snap := h.Sweeper.Snapshot()
	total := len(snap)
	alive := 0
	stale := 0
	for _, s := range snap {
		switch s.Health {
		case HealthAlive:
			alive++
		case HealthStale:
			stale++
		}
	}

	resp := map[string]any{
		"generated_at":   time.Now().UTC(),
		"sweep_interval": h.Sweeper.sweepInterval.String(),
		"stale_after":    h.Sweeper.staleAfter.String(),
		"total":          total,
		"alive":          alive,
		"stale":          stale,
		"services":       snap,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
