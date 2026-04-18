package api

import (
	"fmt"
	"net/http"
)

// MetricsProvider is the narrow view on the dispatcher that the metrics
// handler needs. Avoids a direct import cycle with the dispatcher package.
type MetricsProvider interface {
	GetMetrics() (received, completed, failed, violations, decompositionFailed, queueDepth int64)
}

// MetricsHandler serves GET /metrics in Prometheus text exposition format
// (version 0.0.4). It only needs atomic counter snapshots from the dispatcher
// so it deliberately avoids pulling in the full prometheus/client_golang
// runtime — keeping the orchestrator binary small and its dep graph minimal.
type MetricsHandler struct {
	Provider MetricsProvider
}

// ServeHTTP writes the counter snapshot in text format. Prometheus scrapers
// parse this without issue and it renders as-is when visited from a browser.
func (m *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.Provider == nil {
		http.Error(w, "metrics provider not configured", http.StatusServiceUnavailable)
		return
	}

	received, completed, failed, violations, decomp, queue := m.Provider.GetMetrics()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprint(w, "# HELP orchestrator_tasks_received_total Total user tasks accepted into the dispatcher.\n")
	fmt.Fprint(w, "# TYPE orchestrator_tasks_received_total counter\n")
	fmt.Fprintf(w, "orchestrator_tasks_received_total %d\n", received)

	fmt.Fprint(w, "# HELP orchestrator_tasks_completed_total Tasks that reached terminal success.\n")
	fmt.Fprint(w, "# TYPE orchestrator_tasks_completed_total counter\n")
	fmt.Fprintf(w, "orchestrator_tasks_completed_total %d\n", completed)

	fmt.Fprint(w, "# HELP orchestrator_tasks_failed_total Tasks that reached terminal failure.\n")
	fmt.Fprint(w, "# TYPE orchestrator_tasks_failed_total counter\n")
	fmt.Fprintf(w, "orchestrator_tasks_failed_total %d\n", failed)

	fmt.Fprint(w, "# HELP orchestrator_policy_violations_total Tasks denied by the Policy Enforcer.\n")
	fmt.Fprint(w, "# TYPE orchestrator_policy_violations_total counter\n")
	fmt.Fprintf(w, "orchestrator_policy_violations_total %d\n", violations)

	fmt.Fprint(w, "# HELP orchestrator_decomposition_failed_total Planner agent decomposition failures.\n")
	fmt.Fprint(w, "# TYPE orchestrator_decomposition_failed_total counter\n")
	fmt.Fprintf(w, "orchestrator_decomposition_failed_total %d\n", decomp)

	fmt.Fprint(w, "# HELP orchestrator_queue_depth Current in-flight task count in the dispatcher.\n")
	fmt.Fprint(w, "# TYPE orchestrator_queue_depth gauge\n")
	fmt.Fprintf(w, "orchestrator_queue_depth %d\n", queue)
}
