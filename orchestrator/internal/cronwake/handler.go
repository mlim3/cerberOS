// Package cronwake exposes an HTTP entrypoint for external cron schedulers
// (Vercel Cron, Kubernetes CronJob, cloud scheduler) to wake the orchestrator
// with a maintenance-oriented system prompt and synthetic user task.
package cronwake

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/observability"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

const headerCronSecret = "X-Aegis-Cron-Secret"

// defaultMaintenanceRawInput is used when neither the request body nor
// CRON_WAKE_RAW_INPUT provides a work description.
const defaultMaintenanceRawInput = "Scheduled orchestrator maintenance: review memory health, extract or refresh facts from approved source text where configured, and kick off or schedule fact decay / TTL passes as appropriate. Produce a minimal, safe execution plan using only allowed domains."

// TaskIngester is the dispatcher entry the cron handler invokes.
type TaskIngester interface {
	HandleInboundTask(ctx context.Context, task types.UserTask) error
}

// Handler implements POST /v1/cron/wake.
type Handler struct {
	cfg    *config.OrchestratorConfig
	ingest TaskIngester
}

// NewHandler returns a handler. When cfg.CronWakeSecret is empty, ServeHTTP
// responds with 404 so the route is not exposed accidentally.
func NewHandler(cfg *config.OrchestratorConfig, ingest TaskIngester) *Handler {
	return &Handler{cfg: cfg, ingest: ingest}
}

// wakeRequestBody is optional JSON from the cron caller.
type wakeRequestBody struct {
	SystemPrompt string `json:"system_prompt"`
	RawInput     string `json:"raw_input"`
	JobName      string `json:"job_name"`
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil || h.ingest == nil {
		http.Error(w, "not configured", http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(h.cfg.CronWakeSecret) == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	got := strings.TrimSpace(r.Header.Get(headerCronSecret))
	if !secretMatch(h.cfg.CronWakeSecret, got) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body := wakeRequestBody{}
	if r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(strings.TrimSpace(string(raw))) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}

	sys := firstNonEmpty(strings.TrimSpace(body.SystemPrompt), strings.TrimSpace(h.cfg.CronWakeSystemPrompt))
	raw := firstNonEmpty(strings.TrimSpace(body.RawInput), strings.TrimSpace(h.cfg.CronWakeRawInput), defaultMaintenanceRawInput)

	payload := map[string]any{
		"raw_input":       raw,
		"system_prompt":   sys,
		"maintenance":     true,
		"cron_wake":       true,
		"source":          "cron_wake",
		"orchestrator_id": h.cfg.NodeID,
	}
	if jn := strings.TrimSpace(body.JobName); jn != "" {
		payload["job_name"] = jn
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	timeoutSec := h.cfg.CronWakeTimeoutSeconds
	if timeoutSec < 30 {
		timeoutSec = 3600
	}
	if timeoutSec > 86400 {
		timeoutSec = 86400
	}

	userID := strings.TrimSpace(h.cfg.CronWakeUserID)
	if userID == "" {
		userID = "system"
	}
	callbackTopic := strings.TrimSpace(h.cfg.CronWakeCallbackTopic)
	if callbackTopic == "" {
		callbackTopic = "aegis.orchestrator.cron.wake.results"
	}

	task := types.UserTask{
		TaskID:         uuid.NewString(),
		UserID:         userID,
		Priority:       5,
		TimeoutSeconds: timeoutSec,
		Payload:        payloadBytes,
		CallbackTopic:  callbackTopic,
	}

	ctx := observability.WithModule(context.Background(), "cron_wake")
	ctx = observability.WithTraceID(ctx, uuid.NewString())

	log := observability.LogFromContext(ctx)
	log.Info("cron wake received",
		"task_id", task.TaskID,
		"job_name", strings.TrimSpace(body.JobName),
		"user_id", task.UserID,
		"callback_topic", task.CallbackTopic,
		"timeout_seconds", task.TimeoutSeconds,
	)

	if err := h.ingest.HandleInboundTask(ctx, task); err != nil {
		log.Error("cron wake dispatch failed", "task_id", task.TaskID, "error", err)
		http.Error(w, "dispatch failed", http.StatusBadGateway)
		return
	}

	log.Info("cron wake dispatched",
		"task_id", task.TaskID,
		"job_name", strings.TrimSpace(body.JobName),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "accepted",
		"task_id":  task.TaskID,
		"job_name": strings.TrimSpace(body.JobName),
	})
}

func secretMatch(expected, got string) bool {
	if len(expected) != len(got) {
		return false
	}
	// Equal length required so ConstantTimeCompare runs in constant time.
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
