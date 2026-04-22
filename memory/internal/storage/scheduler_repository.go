package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SchedulerRepository struct {
	pool *pgxpool.Pool
}

type ScheduledJob struct {
	ID              pgtype.UUID
	JobType         string
	TargetKind      string
	TargetService   string
	Status          string
	ScheduleKind    string
	IntervalSeconds pgtype.Int4
	Name            string
	Payload         []byte
	NextRunAt       pgtype.Timestamptz
	LastRunAt       pgtype.Timestamptz
	LastSuccessAt   pgtype.Timestamptz
}

type ScheduledJobRun struct {
	ID            pgtype.UUID
	JobID         pgtype.UUID
	Status        string
	TargetService string
	Result        []byte
	StartedAt     pgtype.Timestamptz
	FinishedAt    pgtype.Timestamptz
}

type CreateScheduledJobParams struct {
	JobType         string
	TargetKind      string
	TargetService   string
	Status          string
	ScheduleKind    string
	IntervalSeconds int32
	Name            string
	Payload         []byte
	NextRunAt       time.Time
}

func NewSchedulerRepository(pool *pgxpool.Pool) *SchedulerRepository {
	return &SchedulerRepository{pool: pool}
}

func (r *SchedulerRepository) CreateJob(ctx context.Context, params CreateScheduledJobParams) (ScheduledJob, error) {
	if err := r.ensureSchedulerTables(ctx); err != nil {
		return ScheduledJob{}, err
	}

	jobID, err := uuid.NewV7()
	if err != nil {
		return ScheduledJob{}, err
	}

	row := r.pool.QueryRow(ctx, `
INSERT INTO scheduler_schema.scheduled_jobs (
	id, job_type, target_kind, target_service, status, schedule_kind,
	interval_seconds, name, payload, next_run_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
RETURNING id, job_type, target_kind, target_service, status, schedule_kind,
          interval_seconds, name, payload, next_run_at, last_run_at, last_success_at`,
		pgtype.UUID{Bytes: jobID, Valid: true},
		params.JobType,
		params.TargetKind,
		params.TargetService,
		params.Status,
		params.ScheduleKind,
		pgtype.Int4{Int32: params.IntervalSeconds, Valid: params.IntervalSeconds > 0},
		params.Name,
		params.Payload,
		pgtype.Timestamptz{Time: params.NextRunAt.UTC(), Valid: true},
	)

	var job ScheduledJob
	err = row.Scan(
		&job.ID,
		&job.JobType,
		&job.TargetKind,
		&job.TargetService,
		&job.Status,
		&job.ScheduleKind,
		&job.IntervalSeconds,
		&job.Name,
		&job.Payload,
		&job.NextRunAt,
		&job.LastRunAt,
		&job.LastSuccessAt,
	)
	return job, err
}

func (r *SchedulerRepository) RunDueJobs(ctx context.Context) ([]ScheduledJobRun, error) {
	if err := r.ensureSchedulerTables(ctx); err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
       interval_seconds, name, payload, next_run_at, last_run_at, last_success_at
FROM scheduler_schema.scheduled_jobs
WHERE status = 'active' AND next_run_at <= NOW()
ORDER BY next_run_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := make([]ScheduledJob, 0)
	for rows.Next() {
		var job ScheduledJob
		if err := rows.Scan(
			&job.ID,
			&job.JobType,
			&job.TargetKind,
			&job.TargetService,
			&job.Status,
			&job.ScheduleKind,
			&job.IntervalSeconds,
			&job.Name,
			&job.Payload,
			&job.NextRunAt,
			&job.LastRunAt,
			&job.LastSuccessAt,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	runs := make([]ScheduledJobRun, 0, len(jobs))
	for _, job := range jobs {
		run, err := r.recordJobRun(ctx, job)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	return runs, nil
}

func (r *SchedulerRepository) ListRuns(ctx context.Context, jobID pgtype.UUID) ([]ScheduledJobRun, error) {
	if err := r.ensureSchedulerTables(ctx); err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
SELECT id, job_id, status, target_service, result, started_at, finished_at
FROM scheduler_schema.scheduled_job_runs
WHERE job_id = $1
ORDER BY started_at DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]ScheduledJobRun, 0)
	for rows.Next() {
		var run ScheduledJobRun
		if err := rows.Scan(
			&run.ID,
			&run.JobID,
			&run.Status,
			&run.TargetService,
			&run.Result,
			&run.StartedAt,
			&run.FinishedAt,
		); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (r *SchedulerRepository) recordJobRun(ctx context.Context, job ScheduledJob) (ScheduledJobRun, error) {
	runID, err := uuid.NewV7()
	if err != nil {
		return ScheduledJobRun{}, err
	}

	startedAt := time.Now().UTC()
	resultPayload, err := json.Marshal(map[string]any{
		"jobType":       job.JobType,
		"targetKind":    job.TargetKind,
		"targetService": job.TargetService,
		"dispatched":    true,
	})
	if err != nil {
		return ScheduledJobRun{}, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ScheduledJobRun{}, err
	}
	defer tx.Rollback(ctx)

	finishedAt := time.Now().UTC()
	row := tx.QueryRow(ctx, `
INSERT INTO scheduler_schema.scheduled_job_runs (
	id, job_id, status, target_service, result, started_at, finished_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, job_id, status, target_service, result, started_at, finished_at`,
		pgtype.UUID{Bytes: runID, Valid: true},
		job.ID,
		"success",
		job.TargetService,
		resultPayload,
		pgtype.Timestamptz{Time: startedAt, Valid: true},
		pgtype.Timestamptz{Time: finishedAt, Valid: true},
	)

	var run ScheduledJobRun
	if err := row.Scan(
		&run.ID,
		&run.JobID,
		&run.Status,
		&run.TargetService,
		&run.Result,
		&run.StartedAt,
		&run.FinishedAt,
	); err != nil {
		return ScheduledJobRun{}, err
	}

	nextRunAt := startedAt
	if job.IntervalSeconds.Valid && job.IntervalSeconds.Int32 > 0 {
		nextRunAt = startedAt.Add(time.Duration(job.IntervalSeconds.Int32) * time.Second)
	}

	if _, err := tx.Exec(ctx, `
UPDATE scheduler_schema.scheduled_jobs
SET last_run_at = $2, last_success_at = $2, next_run_at = $3, updated_at = $2
WHERE id = $1`,
		job.ID,
		pgtype.Timestamptz{Time: finishedAt, Valid: true},
		pgtype.Timestamptz{Time: nextRunAt, Valid: true},
	); err != nil {
		return ScheduledJobRun{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ScheduledJobRun{}, err
	}

	return run, nil
}

func (r *SchedulerRepository) ensureSchedulerTables(ctx context.Context) error {
	const createSchema = `CREATE SCHEMA IF NOT EXISTS scheduler_schema`
	const createJobsTable = `
CREATE TABLE IF NOT EXISTS scheduler_schema.scheduled_jobs (
    id UUID PRIMARY KEY,
    job_type VARCHAR(100) NOT NULL,
    target_kind VARCHAR(20) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    status VARCHAR(20) NOT NULL,
    schedule_kind VARCHAR(20) NOT NULL,
    interval_seconds INT,
    name VARCHAR(255) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`
	const createRunsTable = `
CREATE TABLE IF NOT EXISTS scheduler_schema.scheduled_job_runs (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES scheduler_schema.scheduled_jobs(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    result JSONB,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
)`

	for _, stmt := range []string{createSchema, createJobsTable, createRunsTable} {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure scheduler schema: %w", err)
		}
	}
	return nil
}
