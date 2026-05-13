package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/scheduleutil"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

// HTTP client for optional orchestrator webhook (bounded latency).
var orchestratorHookHTTPClient = &http.Client{Timeout: 15 * time.Second}

// ScheduledJobsHandler implements /api/v1/scheduled_jobs* endpoints.
type ScheduledJobsHandler struct {
	repo     *storage.ScheduledJobsRepository
	userCron storage.UserCronDispatch
	wake     func()
}

// NewScheduledJobsHandler constructs the handler. Pass userCron=nil in tests or when NATS is disabled.
func NewScheduledJobsHandler(repo *storage.ScheduledJobsRepository, userCron storage.UserCronDispatch) *ScheduledJobsHandler {
	return &ScheduledJobsHandler{repo: repo, userCron: userCron}
}

func (h *ScheduledJobsHandler) SetWake(fn func()) {
	h.wake = fn
}

type createScheduledJobRequest struct {
	JobType         string         `json:"jobType"`
	TargetKind      string         `json:"targetKind"`
	TargetService   string         `json:"targetService"`
	Status          string         `json:"status"`
	ScheduleKind    string         `json:"scheduleKind"`
	IntervalSeconds *float64       `json:"intervalSeconds,omitempty"`
	Name            string         `json:"name"`
	Payload         map[string]any `json:"payload"`
	NextRunAt       string         `json:"nextRunAt"`
	UserID          string         `json:"userId"`
	TimeZone        string         `json:"timeZone"`
	CronExpression  string         `json:"cronExpression"`
}

type claimIdempotencyRequest struct {
	Key        string `json:"key"`
	AgentID    string `json:"agentId"`
	JobID      string `json:"jobId,omitempty"`
	RunID      string `json:"runId,omitempty"`
	TTLSeconds int    `json:"ttlSeconds"`
}

type completeIdempotencyRequest struct {
	Key      string         `json:"key"`
	Status   string         `json:"status,omitempty"`
	Result   map[string]any `json:"result"`
	JobID    string         `json:"jobId,omitempty"`
	RunID    string         `json:"runId,omitempty"`
	JobState map[string]any `json:"jobState,omitempty"`
}

// RunDue executes the same processing as POST /api/v1/scheduled_jobs/run_due (used by memory-server ticker).
func (h *ScheduledJobsHandler) RunDue(ctx context.Context) error {
	jobs, err := h.repo.ClaimDueJobs(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if _, err := h.executeJob(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

// HandleCreateScheduledJob POST /api/v1/scheduled_jobs
func (h *ScheduledJobsHandler) HandleCreateScheduledJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req createScheduledJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid JSON body", nil))
		return
	}

	req.JobType = strings.TrimSpace(req.JobType)
	req.TargetKind = strings.TrimSpace(req.TargetKind)
	req.TargetService = strings.TrimSpace(req.TargetService)
	req.Status = strings.TrimSpace(req.Status)
	req.ScheduleKind = strings.TrimSpace(req.ScheduleKind)
	req.Name = strings.TrimSpace(req.Name)
	req.UserID = strings.TrimSpace(req.UserID)

	if req.JobType == "user_cron" {
		if req.TargetKind == "" {
			req.TargetKind = "user"
		}
		if req.TargetService == "" {
			req.TargetService = "orchestrator"
		}
	}

	if req.JobType == "" || req.TargetKind == "" || req.TargetService == "" || req.Name == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "jobType, targetKind, targetService, and name are required", nil))
		return
	}
	// MT-5 (#186): every scheduled_jobs row carries a real owning user_id.
	// The handler rejects the request before the repository so the 400 cites
	// the right field; the repo would also reject via ErrScheduledJobMissingUserID.
	if req.UserID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}
	if req.ScheduleKind == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "scheduleKind is required", nil))
		return
	}

	if req.JobType == "user_cron" {
		if req.UserID == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required for user_cron", nil))
			return
		}
		if req.ScheduleKind == "cron" {
			if err := scheduleutil.ValidateCron(strings.TrimSpace(req.CronExpression)); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", err.Error(), nil))
				return
			}
		} else if req.ScheduleKind == "interval" {
			if req.IntervalSeconds == nil || *req.IntervalSeconds <= 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "intervalSeconds must be positive for interval schedules", nil))
				return
			}
		}
	}

	var interval pgtype.Int4
	if req.ScheduleKind == "interval" {
		if req.IntervalSeconds == nil || *req.IntervalSeconds <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "intervalSeconds must be positive for interval schedules", nil))
			return
		}
		interval = pgtype.Int4{Int32: int32(*req.IntervalSeconds), Valid: true}
	}

	nextRun, err := time.Parse(time.RFC3339, strings.TrimSpace(req.NextRunAt))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "nextRunAt must be RFC3339", nil))
		return
	}

	if req.Payload == nil {
		req.Payload = map[string]any{}
	}
	if req.JobType == "user_cron" {
		if _, ok := req.Payload["userId"]; !ok || strings.TrimSpace(asPayloadString(req.Payload["userId"])) == "" {
			req.Payload["userId"] = req.UserID
		}
	}

	payloadBytes, err := storage.MarshalPayloadMap(req.Payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "payload must be JSON-serializable", nil))
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}

	row, err := h.repo.CreateJob(r.Context(), storage.CreateScheduledJobParams{
		ID:              id,
		JobType:         req.JobType,
		TargetKind:      req.TargetKind,
		TargetService:   req.TargetService,
		Status:          req.Status,
		ScheduleKind:    req.ScheduleKind,
		IntervalSeconds: interval,
		Name:            req.Name,
		Payload:         payloadBytes,
		UserID:          req.UserID,
		TimeZone:        strings.TrimSpace(req.TimeZone),
		CronExpression:  strings.TrimSpace(req.CronExpression),
		State:           []byte(`{}`),
		NextRunAt:       nextRun.UTC(),
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to create scheduled job", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(scheduledJobToMap(row)))
	if h.wake != nil {
		h.wake()
	}
}

func asPayloadString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// HandleRunDueJobs POST /api/v1/scheduled_jobs/run_due
func (h *ScheduledJobsHandler) HandleRunDueJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	jobs, err := h.repo.ClaimDueJobs(ctx, now)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to claim due jobs", err.Error()))
		return
	}

	var runs []map[string]any
	for _, job := range jobs {
		runMap, err := h.executeJob(ctx, job)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(ErrorResponse("internal", "job execution failed", err.Error()))
			return
		}
		runs = append(runs, runMap)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"runs": runs}))
}

func (h *ScheduledJobsHandler) executeJob(ctx context.Context, job storage.ScheduledJob) (map[string]any, error) {
	started := time.Now().UTC()
	runID, err := uuid.NewV7()
	if err != nil {
		runID = uuid.New()
	}

	var trace pgtype.UUID
	if tid, ok := traceIDFromContext(ctx); ok {
		trace = pgtype.UUID{Bytes: [16]byte(tid), Valid: true}
	}

	initialDetail, err := json.Marshal(map[string]any{
		"jobType":        job.JobType,
		"targetKind":     job.TargetKind,
		"scheduledJobId": job.ID.String(),
		"runId":          runID.String(),
		"phase":          "dispatching",
	})
	if err != nil {
		initialDetail = []byte(`{}`)
	}

	run := storage.ScheduledJobRun{
		ID:            runID,
		JobID:         job.ID,
		Status:        "dispatching",
		TargetService: job.TargetService,
		Detail:        initialDetail,
		TraceID:       trace,
		StartedAt:     started,
	}
	if err := h.repo.InsertRun(ctx, run); err != nil {
		return nil, err
	}

	detail, status := h.runJobBody(ctx, job, runID)

	detailBytes, err := json.Marshal(detail)
	if err != nil {
		detailBytes = []byte(`{}`)
	}

	var finishedAt pgtype.Timestamptz
	if status != "dispatched" {
		finishedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	if err := h.repo.UpdateRunStatus(ctx, runID, status, detailBytes, finishedAt); err != nil {
		return nil, err
	}
	run.Status = status
	run.Detail = detailBytes
	run.FinishedAt = finishedAt
	return scheduledRunToMap(run), nil
}

func (h *ScheduledJobsHandler) runJobBody(ctx context.Context, job storage.ScheduledJob, runID uuid.UUID) (map[string]any, string) {
	if job.JobType == "user_cron" {
		return h.runUserCron(ctx, job, runID)
	}

	switch job.TargetKind {
	case "internal":
		return h.runInternalJob(ctx, job)
	case "external":
		return h.runExternalJob(ctx, job)
	default:
		return map[string]any{"error": "unknown targetKind"}, "failed"
	}
}

func (h *ScheduledJobsHandler) runUserCron(ctx context.Context, job storage.ScheduledJob, runID uuid.UUID) (map[string]any, string) {
	detail := map[string]any{
		"jobType":        job.JobType,
		"targetKind":     job.TargetKind,
		"scheduledJobId": job.ID.String(),
		"runId":          runID.String(),
	}
	if h.userCron == nil {
		detail["note"] = "user_cron NATS dispatch not configured"
		return detail, "completed"
	}
	if err := h.userCron(ctx, job, runID); err != nil {
		detail["error"] = err.Error()
		return detail, "failed"
	}
	detail["dispatched"] = true
	var jobState any = map[string]any{}
	if len(job.State) > 0 {
		_ = json.Unmarshal(job.State, &jobState)
	}
	detail["jobState"] = jobState
	return detail, "dispatched"
}

func (h *ScheduledJobsHandler) runInternalJob(ctx context.Context, job storage.ScheduledJob) (map[string]any, string) {
	detail, status := h.execInternalMaintenance(ctx, job.JobType)
	if job.ID != uuid.Nil {
		detail["scheduledJobId"] = job.ID.String()
	}
	return detail, status
}

func systemMaintenanceJobTypes() []string {
	return []string{
		// Sweeps — execute due scheduled jobs immediately (normally also on 1m ticker).
		"scheduled_run_due_sweep",
		"dead_job_reprocessing_sweep",
		// Observability heartbeat / inventory.
		"system_monitoring_heartbeat",
		"reconciliation_inventory",
		// Data maintenance.
		"fact_decay_scan",
		"orphan_cleanup_inventory",
		// Credential / key rotation audits (operators perform rotation in Vault/Secrets; never log secrets).
		"credential_rotation_audit",
		// Performance + infra hooks (stubs extend per environment).
		"performance_health_check",
		"journal_queue_audit",
		// DR / backups (coordinate with Postgres operator snapshots or pg_dump; verify separately).
		"disaster_recovery_coordination",
		"backup_verification_ping",
	}
}

func allowedSystemMaintenance(jobType string) bool {
	jobType = strings.TrimSpace(jobType)
	for _, t := range systemMaintenanceJobTypes() {
		if t == jobType {
			return true
		}
	}
	return false
}

type systemMaintenanceRequest struct {
	JobType string `json:"jobType"`
}

// HandleRunSystemMaintenance POST /api/v1/system/maintenance/run
// Dispatches deterministic internal maintenance without persisting synthetic scheduled_jobs rows.
func (h *ScheduledJobsHandler) HandleRunSystemMaintenance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req systemMaintenanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid JSON body", nil))
		return
	}
	jobType := strings.TrimSpace(req.JobType)
	if jobType == "" || !allowedSystemMaintenance(jobType) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "unsupported or missing jobType", nil))
		return
	}

	detail, status := h.execInternalMaintenance(r.Context(), jobType)

	w.Header().Set("Content-Type", "application/json")
	if status == "completed" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"status": status, "detail": detail}))
		return
	}
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(ErrorResponse("failed", strings.TrimSpace(detailErrString(detail)), detail))
}

func detailErrString(detail map[string]any) string {
	if detail == nil {
		return "maintenance step failed"
	}
	if v, ok := detail["error"].(string); ok && v != "" {
		return v
	}
	return "maintenance step failed"
}

// execInternalMaintenance runs a named maintenance step (persisted internal jobs reuse the same switch).
func (h *ScheduledJobsHandler) execInternalMaintenance(ctx context.Context, jobType string) (map[string]any, string) {
	jobType = strings.TrimSpace(jobType)
	switch jobType {

	case "scheduled_run_due_sweep", "dead_job_reprocessing_sweep":
		if err := h.RunDue(ctx); err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType": jobType,
			"ok":      true,
			"note":    "RunDue sweep completed",
		}, "completed"

	case "system_monitoring_heartbeat":
		now := time.Now().UTC()
		since := now.Add(-24 * time.Hour)
		active, err := h.repo.CountActiveScheduledJobs(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		due, err := h.repo.CountDueScheduledJobs(ctx, now)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		failedRuns24h, err := h.repo.CountRunsByStatusSince(ctx, "failed", since)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		okRuns24h, err := h.repo.CountRunsByStatusSince(ctx, "completed", since)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":          jobType,
			"asOf":             now.Format(time.RFC3339Nano),
			"activeJobs":       active,
			"dueJobsNow":       due,
			"completedRuns24h": okRuns24h,
			"failedRuns24h":    failedRuns24h,
			"natsUrlSet":       strings.TrimSpace(os.Getenv("NATS_URL")) != "",
			"orchestratorWebhookSet": func() bool {
				return strings.TrimSpace(os.Getenv("ORCHESTRATOR_SCHEDULED_JOB_URL")) != ""
			}(),
		}, "completed"

	case "reconciliation_inventory":
		active, err := h.repo.CountActiveScheduledJobs(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		due, err := h.repo.CountDueScheduledJobs(ctx, time.Now().UTC())
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		orphans, err := h.repo.CountOrphanScheduledJobRuns(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":                   jobType,
			"activeScheduledJobs":       active,
			"dueScheduledJobs":          due,
			"orphanScheduledJobRunsEst": orphans,
			"note":                      "Orphan runs should stay 0 while FK enforced; nonzero indicates schema/load issues.",
		}, "completed"

	case "fact_decay_scan":
		n, err := h.repo.FactCountForDecayScan(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":       jobType,
			"factsObserved": n,
			"note":          "fact_decay_scan completed (stub TTL/decay wiring can extend this step)",
		}, "completed"

	case "orphan_cleanup_inventory":
		n, err := h.repo.CountOrphanScheduledJobRuns(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":    jobType,
			"orphanRuns": n,
			"note":       "Automated deletes are risky; DELETE orphans only after operator review.",
		}, "completed"

	case "credential_rotation_audit":
		internalKey := strings.TrimSpace(os.Getenv("INTERNAL_VAULT_API_KEY")) != ""
		return map[string]any{
			"jobType": jobType,
			"signals": map[string]any{
				"memoryInternalVaultAuthConfigured": internalKey,
				"natsUrlConfigured":                 strings.TrimSpace(os.Getenv("NATS_URL")) != "",
				"orchestratorScheduledWebhookConfigured": strings.TrimSpace(os.Getenv(
					"ORCHESTRATOR_SCHEDULED_JOB_URL")) != "",
			},
			"note": "Rotate API/JWT/signing secrets via cluster Secret rotation (IO, orchestrator); OpenBao handles broker creds.",
		}, "completed"

	case "performance_health_check":
		latency, err := h.repo.DBPingLatency(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":          jobType,
			"dbPingMsApprox":   float64(latency.Nanoseconds()) / 1e6,
			"metricsPathHint":  "/internal/metrics",
			"histogramScraper": "Prefer Prometheus scraping memory-api pods for CPU/GC histograms.",
		}, "completed"

	case "journal_queue_audit":
		return map[string]any{
			"jobType":           jobType,
			"natsUrlConfigured": strings.TrimSpace(os.Getenv("NATS_URL")) != "",
			"note":              "JetStream / consumer lag is visible on NATS monitoring (e.g. :8222/conz). Replay DLQ via databus/orchestrator policy.",
		}, "completed"

	case "disaster_recovery_coordination":
		return map[string]any{
			"jobType": jobType,
			"note":    "Run Postgres base backups (operator snapshot, WAL archive, or pg_dump CronJob); store off-cluster.",
			"verify":  "Pair with backup_verification_ping restores in lower environments.",
		}, "completed"

	case "backup_verification_ping":
		latency, err := h.repo.DBPingLatency(ctx)
		if err != nil {
			return map[string]any{"jobType": jobType, "error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":                 jobType,
			"dbReachablePingMsApprox": float64(latency.Nanoseconds()) / 1e6,
			"note":                    "Full restore drills are operator-owned; this only checks live DB connectivity.",
		}, "completed"

	default:
		return map[string]any{"error": "unsupported internal jobType", "jobType": jobType}, "failed"
	}
}

func (h *ScheduledJobsHandler) runExternalJob(ctx context.Context, job storage.ScheduledJob) (map[string]any, string) {
	url := strings.TrimSpace(os.Getenv("ORCHESTRATOR_SCHEDULED_JOB_URL"))
	var pl any
	if len(job.Payload) > 0 {
		_ = json.Unmarshal(job.Payload, &pl)
	}
	body := map[string]any{
		"jobType":        job.JobType,
		"targetService":  job.TargetService,
		"targetKind":     job.TargetKind,
		"name":           job.Name,
		"payload":        pl,
		"scheduledJobId": job.ID.String(),
	}
	if url == "" {
		body["note"] = "ORCHESTRATOR_SCHEDULED_JOB_URL not set; dispatch skipped"
		return body, "completed"
	}

	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return map[string]any{"error": err.Error()}, "failed"
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := orchestratorHookHTTPClient.Do(req)
	if err != nil {
		return map[string]any{"error": err.Error()}, "failed"
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]any{
		"httpStatus": resp.StatusCode,
		"response":   json.RawMessage(b),
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return out, "completed"
	}
	out["error"] = "orchestrator returned non-2xx"
	return out, "failed"
}

// HandleListUserCrons GET /api/v1/user_crons
func (h *ScheduledJobsHandler) HandleListUserCrons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}
	if _, err := uuid.Parse(userID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid userId format", nil))
		return
	}

	jobs, err := h.repo.ListUserCrons(r.Context(), userID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to list user crons", err.Error()))
		return
	}

	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		sm := scheduledJobToMap(j)
		out = append(out, sm)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"jobs": out}))
}

// HandleDeleteUserCron DELETE /api/v1/scheduled_jobs/{jobId}
func (h *ScheduledJobsHandler) HandleDeleteUserCron(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID, err := uuid.Parse(strings.TrimSpace(r.PathValue("jobId")))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid job id", nil))
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	if userID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "userId is required", nil))
		return
	}
	if _, err := uuid.Parse(userID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid userId format", nil))
		return
	}

	ok, err := h.repo.DeleteUserCron(r.Context(), jobID, userID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to delete job", err.Error()))
		return
	}
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse("not_found", "job not found or not owned by user", nil))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"deleted": true}))
}

// HandleClaimIdempotency POST /api/v1/idempotency/claim
func (h *ScheduledJobsHandler) HandleClaimIdempotency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req claimIdempotencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid JSON body", nil))
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.Key == "" || req.AgentID == "" || req.TTLSeconds <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "key, agentId, and positive ttlSeconds are required", nil))
		return
	}
	result, err := h.repo.ClaimIdempotency(r.Context(), storage.ClaimIdempotencyParams{
		Key:        req.Key,
		AgentID:    req.AgentID,
		JobID:      strings.TrimSpace(req.JobID),
		RunID:      strings.TrimSpace(req.RunID),
		TTLSeconds: req.TTLSeconds,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to claim idempotency record", err.Error()))
		return
	}

	var resultPayload any = map[string]any{}
	if len(result.Record.Result) > 0 {
		_ = json.Unmarshal(result.Record.Result, &resultPayload)
	}
	body := map[string]any{
		"claimed":   result.Claimed,
		"key":       result.Record.Key,
		"status":    result.Record.Status,
		"agentId":   result.Record.AgentID,
		"jobId":     result.Record.JobID,
		"runId":     result.Record.RunID,
		"result":    resultPayload,
		"claimedAt": result.Record.ClaimedAt.UTC().Format(time.RFC3339Nano),
		"expiresAt": result.Record.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	if result.Record.CompletedAt.Valid {
		body["completedAt"] = result.Record.CompletedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(body))
}

// HandleCompleteIdempotency POST /api/v1/idempotency/complete
func (h *ScheduledJobsHandler) HandleCompleteIdempotency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req completeIdempotencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid JSON body", nil))
		return
	}
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "key is required", nil))
		return
	}
	resultBytes, err := storage.MarshalPayloadMap(req.Result)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "result must be JSON-serializable", nil))
		return
	}
	var jobStateBytes []byte
	if req.JobState != nil {
		jobStateBytes, err = storage.MarshalPayloadMap(req.JobState)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "jobState must be JSON-serializable", nil))
			return
		}
	}
	if err := h.repo.CompleteIdempotency(r.Context(), storage.CompleteIdempotencyParams{
		Key:      req.Key,
		Status:   strings.TrimSpace(req.Status),
		Result:   resultBytes,
		JobID:    strings.TrimSpace(req.JobID),
		RunID:    strings.TrimSpace(req.RunID),
		JobState: jobStateBytes,
	}); err != nil {
		code := "internal"
		status := http.StatusInternalServerError
		msg := "failed to complete idempotency record"
		if errors.Is(err, pgx.ErrNoRows) {
			code = "not_found"
			status = http.StatusNotFound
			msg = "idempotency record not found"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(ErrorResponse(code, msg, err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"ok": true}))
}

// HandleListScheduledJobRuns GET /api/v1/scheduled_jobs/{jobId}/runs
func (h *ScheduledJobsHandler) HandleListScheduledJobRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimSpace(r.PathValue("jobId"))
	jobID, err := uuid.Parse(idStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid job id", nil))
		return
	}

	if _, err := h.repo.GetJob(r.Context(), jobID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(ErrorResponse("not_found", "scheduled job not found", nil))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to load job", err.Error()))
		return
	}

	runs, err := h.repo.ListRunsByJob(r.Context(), jobID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to list runs", err.Error()))
		return
	}

	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, scheduledRunToMap(run))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{"runs": out}))
}

func scheduledJobToMap(j storage.ScheduledJob) map[string]any {
	m := map[string]any{
		"id":             j.ID.String(),
		"jobType":        j.JobType,
		"targetKind":     j.TargetKind,
		"targetService":  j.TargetService,
		"status":         j.Status,
		"scheduleKind":   j.ScheduleKind,
		"name":           j.Name,
		"nextRunAt":      j.NextRunAt.UTC().Format(time.RFC3339Nano),
		"userId":         j.UserID,
		"timeZone":       j.TimeZone,
		"cronExpression": j.CronExpression,
	}
	if j.IntervalSeconds.Valid {
		m["intervalSeconds"] = j.IntervalSeconds.Int32
	}
	var payload any = map[string]any{}
	if len(j.Payload) > 0 {
		_ = json.Unmarshal(j.Payload, &payload)
	}
	m["payload"] = payload
	var state any = map[string]any{}
	if len(j.State) > 0 {
		_ = json.Unmarshal(j.State, &state)
	}
	m["state"] = state
	return m
}

func scheduledRunToMap(run storage.ScheduledJobRun) map[string]any {
	m := map[string]any{
		"id":            run.ID.String(),
		"jobId":         run.JobID.String(),
		"status":        run.Status,
		"targetService": run.TargetService,
		"startedAt":     run.StartedAt.UTC().Format(time.RFC3339Nano),
	}
	if run.FinishedAt.Valid {
		m["finishedAt"] = run.FinishedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	var detail any = map[string]any{}
	if len(run.Detail) > 0 {
		_ = json.Unmarshal(run.Detail, &detail)
	}
	m["detail"] = detail
	return m
}
