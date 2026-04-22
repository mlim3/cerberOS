package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

type ScheduledJobsHandler struct {
	repo *storage.SchedulerRepository
}

type CreateScheduledJobRequest struct {
	JobType         string          `json:"jobType"`
	TargetKind      string          `json:"targetKind"`
	TargetService   string          `json:"targetService"`
	Status          string          `json:"status"`
	ScheduleKind    string          `json:"scheduleKind"`
	IntervalSeconds int32           `json:"intervalSeconds"`
	Name            string          `json:"name"`
	Payload         json.RawMessage `json:"payload" swaggertype:"object"`
	NextRunAt       string          `json:"nextRunAt"`
}

func NewScheduledJobsHandler(repo *storage.SchedulerRepository) *ScheduledJobsHandler {
	return &ScheduledJobsHandler{repo: repo}
}

// HandleCreateScheduledJob creates a scheduled job.
// @Summary Create scheduled job
// @Description Creates a scheduled job for internal maintenance or external dispatch
// @Tags scheduled_jobs
// @Accept json
// @Produce json
// @Param request body CreateScheduledJobRequest true "Scheduled Job Payload"
// @Success 201 {object} map[string]interface{} "Created"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/scheduled_jobs [post]
func (h *ScheduledJobsHandler) HandleCreateScheduledJob(w http.ResponseWriter, r *http.Request) {
	var req CreateScheduledJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid request body", nil))
		return
	}

	if req.JobType == "" || req.TargetKind == "" || req.TargetService == "" || req.Status == "" || req.ScheduleKind == "" || req.Name == "" || req.NextRunAt == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "jobType, targetKind, targetService, status, scheduleKind, name, and nextRunAt are required", nil))
		return
	}

	nextRunAt, err := time.Parse(time.RFC3339, req.NextRunAt)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "nextRunAt must be RFC3339", nil))
		return
	}

	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}

	job, err := h.repo.CreateJob(r.Context(), storage.CreateScheduledJobParams{
		JobType:         req.JobType,
		TargetKind:      req.TargetKind,
		TargetService:   req.TargetService,
		Status:          req.Status,
		ScheduleKind:    req.ScheduleKind,
		IntervalSeconds: req.IntervalSeconds,
		Name:            req.Name,
		Payload:         req.Payload,
		NextRunAt:       nextRunAt,
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to create scheduled job", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"id":            formatUUID(job.ID),
		"jobType":       job.JobType,
		"targetKind":    job.TargetKind,
		"targetService": job.TargetService,
		"status":        job.Status,
		"scheduleKind":  job.ScheduleKind,
		"name":          job.Name,
		"nextRunAt":     job.NextRunAt.Time.Format(time.RFC3339),
	}))
}

// HandleRunDueJobs executes due scheduled jobs.
// @Summary Run due scheduled jobs
// @Description Executes all active scheduled jobs whose next run time is due and records run history
// @Tags scheduled_jobs
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "OK"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/scheduled_jobs/run_due [post]
func (h *ScheduledJobsHandler) HandleRunDueJobs(w http.ResponseWriter, r *http.Request) {
	runs, err := h.repo.RunDueJobs(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to run due jobs", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"runs": formatScheduledJobRuns(runs),
	}))
}

// HandleListScheduledJobRuns lists runs for one scheduled job.
// @Summary List scheduled job runs
// @Description Retrieves run history for a scheduled job
// @Tags scheduled_jobs
// @Produce json
// @Param jobId path string true "Job ID"
// @Success 200 {object} map[string]interface{} "OK"
// @Failure 400 {object} map[string]interface{} "Bad Request"
// @Failure 500 {object} map[string]interface{} "Internal Server Error"
// @Router /api/v1/scheduled_jobs/{jobId}/runs [get]
func (h *ScheduledJobsHandler) HandleListScheduledJobRuns(w http.ResponseWriter, r *http.Request) {
	jobIDRaw := r.PathValue("jobId")
	var jobID pgtype.UUID
	if err := jobID.Scan(jobIDRaw); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse("invalid_argument", "invalid jobId format", nil))
		return
	}

	runs, err := h.repo.ListRuns(r.Context(), jobID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse("internal", "failed to list scheduled job runs", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SuccessResponse(map[string]any{
		"runs": formatScheduledJobRuns(runs),
	}))
}

func formatScheduledJobRuns(runs []storage.ScheduledJobRun) []map[string]any {
	if runs == nil {
		return []map[string]any{}
	}

	formatted := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		item := map[string]any{
			"id":            formatUUID(run.ID),
			"jobId":         formatUUID(run.JobID),
			"status":        run.Status,
			"targetService": run.TargetService,
			"startedAt":     run.StartedAt.Time.Format(time.RFC3339),
		}
		if run.FinishedAt.Valid {
			item["finishedAt"] = run.FinishedAt.Time.Format(time.RFC3339)
		}
		if len(run.Result) > 0 {
			var result any
			if err := json.Unmarshal(run.Result, &result); err == nil {
				item["result"] = result
			}
		}
		formatted = append(formatted, item)
	}
	return formatted
}

func formatUUID(u pgtype.UUID) string {
	b := u.Bytes
	return string([]byte{
		hexChar(b[0] >> 4), hexChar(b[0] & 0x0f),
		hexChar(b[1] >> 4), hexChar(b[1] & 0x0f),
		hexChar(b[2] >> 4), hexChar(b[2] & 0x0f),
		hexChar(b[3] >> 4), hexChar(b[3] & 0x0f),
		'-',
		hexChar(b[4] >> 4), hexChar(b[4] & 0x0f),
		hexChar(b[5] >> 4), hexChar(b[5] & 0x0f),
		'-',
		hexChar(b[6] >> 4), hexChar(b[6] & 0x0f),
		hexChar(b[7] >> 4), hexChar(b[7] & 0x0f),
		'-',
		hexChar(b[8] >> 4), hexChar(b[8] & 0x0f),
		hexChar(b[9] >> 4), hexChar(b[9] & 0x0f),
		'-',
		hexChar(b[10] >> 4), hexChar(b[10] & 0x0f),
		hexChar(b[11] >> 4), hexChar(b[11] & 0x0f),
		hexChar(b[12] >> 4), hexChar(b[12] & 0x0f),
		hexChar(b[13] >> 4), hexChar(b[13] & 0x0f),
		hexChar(b[14] >> 4), hexChar(b[14] & 0x0f),
		hexChar(b[15] >> 4), hexChar(b[15] & 0x0f),
	})
}
