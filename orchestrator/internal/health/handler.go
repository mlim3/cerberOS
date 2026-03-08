// Package health implements the /health HTTP endpoint (§12.2)
// and the dependency health monitoring loop (§12.1).
//
// /health returns:
//
//	{
//	  "status": "healthy|degraded|unhealthy",
//	  "active_tasks": 42,
//	  "queue_depth": 3,
//	  "vault_reachable": true,
//	  "memory_reachable": true,
//	  "nats_connected": true,
//	  "uptime_seconds": 86400,
//	  "node_id": "orchestrator-node-1"
//	}
//
// Status rules (§12.2):
//   - healthy:   all dependencies reachable, queue_depth < high_water_mark
//   - degraded:  one non-critical dependency unreachable
//   - unhealthy: Vault or Memory unreachable
//
// Dependency check intervals (§12.1):
//   - Vault:           every 10s
//   - Memory:          every 10s
//   - NATS:            every 5s
//   - Agents Component: every 30s (capability_query probe)
package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
)

// HealthStatus represents the /health response body (§12.2).
type HealthStatus struct {
	Status         string `json:"status"`          // healthy | degraded | unhealthy
	ActiveTasks    int    `json:"active_tasks"`
	QueueDepth     int64  `json:"queue_depth"`
	VaultReachable bool   `json:"vault_reachable"`
	MemoryReachable bool  `json:"memory_reachable"`
	NATSConnected  bool   `json:"nats_connected"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	NodeID         string `json:"node_id"`
}

// ActiveTaskProvider is implemented by Task Dispatcher / Monitor to report
// current active task counts.
type ActiveTaskProvider interface {
	GetActiveTaskCount() int
}

// Handler holds all dependencies needed to respond to /health requests.
type Handler struct {
	vault   interfaces.VaultClient
	memory  interfaces.MemoryClient
	nats    interfaces.NATSClient
	tasks   ActiveTaskProvider
	nodeID  string
	startAt time.Time

	// Atomic health state — updated by background check loop
	vaultOK  atomic.Bool
	memoryOK atomic.Bool

	queueDepth atomic.Int64
}

// New creates a new health Handler.
func New(
	vault interfaces.VaultClient,
	memory interfaces.MemoryClient,
	nats interfaces.NATSClient,
	tasks ActiveTaskProvider,
	nodeID string,
) *Handler {
	h := &Handler{
		vault:   vault,
		memory:  memory,
		nats:    nats,
		tasks:   tasks,
		nodeID:  nodeID,
		startAt: time.Now(),
	}
	h.vaultOK.Store(true)
	h.memoryOK.Store(true)
	return h
}

// ServeHTTP handles GET /health. Returns JSON HealthStatus (§12.2).
//
// TODO Phase 7: implement
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// TODO Phase 7: build HealthStatus from atomic state + active task count
	status := HealthStatus{
		Status:          "healthy",
		ActiveTasks:     h.tasks.GetActiveTaskCount(),
		VaultReachable:  h.vaultOK.Load(),
		MemoryReachable: h.memoryOK.Load(),
		NATSConnected:   h.nats.IsConnected(),
		UptimeSeconds:   int64(time.Since(h.startAt).Seconds()),
		NodeID:          h.nodeID,
		QueueDepth:      h.queueDepth.Load(),
	}

	// Status rules
	if !status.VaultReachable || !status.MemoryReachable {
		status.Status = "unhealthy"
	} else if !status.NATSConnected {
		status.Status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// StartMonitorLoop starts background goroutines that probe each dependency
// on its configured interval. Updates atomic state read by ServeHTTP (§12.1).
//
// TODO Phase 7: implement per-dependency check loops:
//   - Vault:  every vaultIntervalSeconds  → h.vaultOK.Store(...)
//   - Memory: every memoryIntervalSeconds → h.memoryOK.Store(...)
//   - NATS:   every natsIntervalSeconds   → h.nats.IsConnected()
func (h *Handler) StartMonitorLoop(vaultIntervalSeconds, memoryIntervalSeconds int) {
	// TODO Phase 7
}

// SetQueueDepth updates the queue depth reported in /health.
// Called by Task Dispatcher when queue depth changes.
func (h *Handler) SetQueueDepth(depth int64) {
	h.queueDepth.Store(depth)
}
