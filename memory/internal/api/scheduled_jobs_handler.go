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
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

// HTTP client for optional orchestrator webhook (bounded latency).
var orchestratorHookHTTPClient = &http.Client{Timeout: 15 * time.Second}

// ScheduledJobsHandler implements /api/v1/scheduled_jobs* endpoints.
type ScheduledJobsHandler struct {
	repo *storage.ScheduledJobsRepository
}

// NewScheduledJobsHandler constructs the handler.
func NewScheduledJobsHandler(repo *storage.ScheduledJobsRepository) *ScheduledJobsHandler {
	return &ScheduledJobsHandler{repo: repo}
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

	if req.JobType == "" || req.TargetKind == "" || req.TargetService == "" || req.Name == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "jobType, targetKind, targetService, and name are required", nil))
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
}

// HandleRunDueJobs POST /api/v1/scheduled_jobs/run_due
func (h *ScheduledJobsHandler) HandleRunDueJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()
	jobs, err := h.repo.ListDueJobs(ctx, now)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to list due jobs", err.Error()))
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
		trace = pgtype.UUID{Bytes: tid, Valid: true}
	}

	detail, status := h.runJobBody(ctx, job)

	detailBytes, err := json.Marshal(detail)
	if err != nil {
		detailBytes = []byte(`{}`)
	}

	finished := time.Now().UTC()

	run := storage.ScheduledJobRun{
		ID:            runID,
		JobID:         job.ID,
		Status:        status,
		TargetService: job.TargetService,
		Detail:        detailBytes,
		TraceID:       trace,
		StartedAt:     started,
		FinishedAt:    pgtype.Timestamptz{Time: finished, Valid: true},
	}
	if err := h.repo.InsertRun(ctx, run); err != nil {
		return nil, err
	}

	// Advance next_run_at for interval schedules on success paths.
	if job.ScheduleKind == "interval" && job.IntervalSeconds.Valid && job.IntervalSeconds.Int32 > 0 {
		next := finished.Add(time.Duration(job.IntervalSeconds.Int32) * time.Second)
		if err := h.repo.UpdateJobNextRun(ctx, job.ID, next); err != nil {
			return nil, err
		}
	}

	return scheduledRunToMap(run), nil
}

func (h *ScheduledJobsHandler) runJobBody(ctx context.Context, job storage.ScheduledJob) (map[string]any, string) {
	switch job.TargetKind {
	case "internal":
		return h.runInternalJob(ctx, job)
	case "external":
		return h.runExternalJob(ctx, job)
	default:
		return map[string]any{"error": "unknown targetKind"}, "failed"
	}
}

func (h *ScheduledJobsHandler) runInternalJob(ctx context.Context, job storage.ScheduledJob) (map[string]any, string) {
	switch job.JobType {
	case "fact_decay_scan":
		n, err := h.repo.FactCountForDecayScan(ctx)
		if err != nil {
			return map[string]any{"error": err.Error()}, "failed"
		}
		return map[string]any{
			"jobType":       job.JobType,
			"factsObserved": n,
			"note":          "fact_decay_scan completed (stub scan)",
		}, "completed"
	default:
		return map[string]any{"error": "unsupported internal jobType", "jobType": job.JobType}, "failed"
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
		"id":            j.ID.String(),
		"jobType":       j.JobType,
		"targetKind":    j.TargetKind,
		"targetService": j.TargetService,
		"status":        j.Status,
		"scheduleKind":  j.ScheduleKind,
		"name":          j.Name,
		"nextRunAt":     j.NextRunAt.UTC().Format(time.RFC3339Nano),
	}
	if j.IntervalSeconds.Valid {
		m["intervalSeconds"] = j.IntervalSeconds.Int32
	}
	var payload any = map[string]any{}
	if len(j.Payload) > 0 {
		_ = json.Unmarshal(j.Payload, &payload)
	}
	m["payload"] = payload
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
