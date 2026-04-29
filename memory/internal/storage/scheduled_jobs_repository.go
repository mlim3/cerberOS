package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScheduledJob is a persisted scheduled job definition.
type ScheduledJob struct {
	ID              uuid.UUID
	JobType         string
	TargetKind      string
	TargetService   string
	Status          string
	ScheduleKind    string
	IntervalSeconds pgtype.Int4
	Name            string
	Payload         []byte
	NextRunAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ScheduledJobRun is one execution attempt of a scheduled job.
type ScheduledJobRun struct {
	ID            uuid.UUID
	JobID         uuid.UUID
	Status        string
	TargetService string
	Detail        []byte
	TraceID       pgtype.UUID
	StartedAt     time.Time
	FinishedAt    pgtype.Timestamptz
	CreatedAt     time.Time
}

// CreateScheduledJobParams holds fields for inserting a job.
type CreateScheduledJobParams struct {
	ID              uuid.UUID
	JobType         string
	TargetKind      string
	TargetService   string
	Status          string
	ScheduleKind    string
	IntervalSeconds pgtype.Int4
	Name            string
	Payload         []byte
	NextRunAt       time.Time
}

// ScheduledJobsRepository persists scheduled jobs and run history.
type ScheduledJobsRepository struct {
	pool *pgxpool.Pool
}

// NewScheduledJobsRepository constructs a repository.
func NewScheduledJobsRepository(pool *pgxpool.Pool) *ScheduledJobsRepository {
	return &ScheduledJobsRepository{pool: pool}
}

// CreateJob inserts a new scheduled job.
func (r *ScheduledJobsRepository) CreateJob(ctx context.Context, p CreateScheduledJobParams) (ScheduledJob, error) {
	const q = `
INSERT INTO scheduling_schema.scheduled_jobs (
  id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, next_run_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
RETURNING id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, next_run_at, created_at, updated_at`

	var row ScheduledJob
	err := r.pool.QueryRow(ctx, q,
		p.ID,
		p.JobType,
		p.TargetKind,
		p.TargetService,
		p.Status,
		p.ScheduleKind,
		p.IntervalSeconds,
		p.Name,
		p.Payload,
		p.NextRunAt,
	).Scan(
		&row.ID,
		&row.JobType,
		&row.TargetKind,
		&row.TargetService,
		&row.Status,
		&row.ScheduleKind,
		&row.IntervalSeconds,
		&row.Name,
		&row.Payload,
		&row.NextRunAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err != nil {
		return ScheduledJob{}, fmt.Errorf("insert scheduled job: %w", err)
	}
	return row, nil
}

// GetJob returns a job by id or ErrNoRows.
func (r *ScheduledJobsRepository) GetJob(ctx context.Context, id uuid.UUID) (ScheduledJob, error) {
	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, next_run_at, created_at, updated_at
FROM scheduling_schema.scheduled_jobs WHERE id = $1`

	var row ScheduledJob
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&row.ID,
		&row.JobType,
		&row.TargetKind,
		&row.TargetService,
		&row.Status,
		&row.ScheduleKind,
		&row.IntervalSeconds,
		&row.Name,
		&row.Payload,
		&row.NextRunAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduledJob{}, err
	}
	if err != nil {
		return ScheduledJob{}, fmt.Errorf("get scheduled job: %w", err)
	}
	return row, nil
}

// ListDueJobs returns active jobs whose next_run_at is at or before `before`.
func (r *ScheduledJobsRepository) ListDueJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, next_run_at, created_at, updated_at
FROM scheduling_schema.scheduled_jobs
WHERE status = 'active' AND next_run_at <= $1
ORDER BY next_run_at ASC`

	rows, err := r.pool.Query(ctx, q, before)
	if err != nil {
		return nil, fmt.Errorf("list due jobs: %w", err)
	}
	defer rows.Close()

	var out []ScheduledJob
	for rows.Next() {
		var row ScheduledJob
		if err := rows.Scan(
			&row.ID,
			&row.JobType,
			&row.TargetKind,
			&row.TargetService,
			&row.Status,
			&row.ScheduleKind,
			&row.IntervalSeconds,
			&row.Name,
			&row.Payload,
			&row.NextRunAt,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UpdateJobNextRun sets next_run_at and bumps updated_at.
func (r *ScheduledJobsRepository) UpdateJobNextRun(ctx context.Context, jobID uuid.UUID, next time.Time) error {
	const q = `
UPDATE scheduling_schema.scheduled_jobs
SET next_run_at = $2, updated_at = NOW()
WHERE id = $1`
	ct, err := r.pool.Exec(ctx, q, jobID, next)
	if err != nil {
		return fmt.Errorf("update job next_run: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// InsertRun creates a run row.
func (r *ScheduledJobsRepository) InsertRun(ctx context.Context, run ScheduledJobRun) error {
	const q = `
INSERT INTO scheduling_schema.scheduled_job_runs (
  id, job_id, status, target_service, detail, trace_id, started_at, finished_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`
	_, err := r.pool.Exec(ctx, q,
		run.ID,
		run.JobID,
		run.Status,
		run.TargetService,
		run.Detail,
		run.TraceID,
		run.StartedAt,
		run.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("insert scheduled job run: %w", err)
	}
	return nil
}

// ListRunsByJob returns runs for a job, newest first.
func (r *ScheduledJobsRepository) ListRunsByJob(ctx context.Context, jobID uuid.UUID) ([]ScheduledJobRun, error) {
	const q = `
SELECT id, job_id, status, target_service, detail, trace_id, started_at, finished_at, created_at
FROM scheduling_schema.scheduled_job_runs
WHERE job_id = $1
ORDER BY started_at DESC`

	rows, err := r.pool.Query(ctx, q, jobID)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []ScheduledJobRun
	for rows.Next() {
		var row ScheduledJobRun
		if err := rows.Scan(
			&row.ID,
			&row.JobID,
			&row.Status,
			&row.TargetService,
			&row.Detail,
			&row.TraceID,
			&row.StartedAt,
			&row.FinishedAt,
			&row.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// FactCountForDecayScan returns how many user_facts rows exist (lightweight internal job signal).
func (r *ScheduledJobsRepository) FactCountForDecayScan(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM personal_info_schema.user_facts`
	var n int64
	if err := r.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// MarshalPayloadMap encodes a map for JSONB storage.
func MarshalPayloadMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}
