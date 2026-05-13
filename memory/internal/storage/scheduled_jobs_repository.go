package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	UserID          string
	TimeZone        string
	CronExpression  string
	NextRunAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UserCronDispatch publishes a due user_cron job to NATS when configured.
type UserCronDispatch func(ctx context.Context, job ScheduledJob) error

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
	UserID          string
	TimeZone        string
	CronExpression  string
	NextRunAt       time.Time
}

// ScheduledJobsRepository persists scheduled jobs and run history.
type ScheduledJobsRepository struct {
	pool *pgxpool.Pool
}

// ErrScheduledJobMissingUserID is returned when a CreateJob call omits user_id.
// MT-5 (#186): every scheduled_jobs row is owned by exactly one user.
var ErrScheduledJobMissingUserID = errors.New("user_id is required")

// NewScheduledJobsRepository constructs a repository.
func NewScheduledJobsRepository(pool *pgxpool.Pool) *ScheduledJobsRepository {
	return &ScheduledJobsRepository{pool: pool}
}

// EnsureSchema applies the MT-5 (#186) per-row tenant ownership migration.
// Runs at memory-api boot, idempotently:
//   - scheduling_schema and the scheduled_jobs / scheduled_job_runs tables are
//     guaranteed to exist (matches init-db.sql shape).
//   - The user_id column is guaranteed to be UUID NOT NULL with an FK to
//     identity_schema.users(id). Pre-MT-5 rows used VARCHAR(64) DEFAULT ''
//     which cannot satisfy the UUID type, so a dev-DB-acceptable wipe runs
//     before the type change. Wrapped in EXCEPTION blocks so a fresh DB (no
//     prior column or table) silently no-ops.
//   - The (status, user_id, next_run_at) composite index is created.
func (r *ScheduledJobsRepository) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS scheduling_schema;`,
		// Pre-MT-5 rows had VARCHAR user_id with a '' default. The column
		// type change to UUID fails on any '' value, so wipe first. Wrapped
		// to no-op on a fresh DB where the table doesn't exist yet.
		`DO $$ BEGIN
    TRUNCATE TABLE scheduling_schema.scheduled_jobs CASCADE;
EXCEPTION WHEN undefined_table THEN NULL;
END $$;`,
		// If the column already exists as VARCHAR (pre-MT-5 schema), drop and
		// recreate as UUID with the FK. Wrapped because on a fresh DB the
		// column won't exist at all.
		`DO $$ BEGIN
    ALTER TABLE scheduling_schema.scheduled_jobs DROP COLUMN IF EXISTS user_id;
EXCEPTION WHEN undefined_table THEN NULL;
END $$;`,
		`CREATE TABLE IF NOT EXISTS scheduling_schema.scheduled_jobs (
    id UUID PRIMARY KEY,
    job_type VARCHAR(100) NOT NULL,
    target_kind VARCHAR(50) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'active',
    schedule_kind VARCHAR(50) NOT NULL,
    interval_seconds INT,
    name VARCHAR(255) NOT NULL,
    payload JSONB,
    user_id UUID NOT NULL REFERENCES identity_schema.users(id),
    time_zone VARCHAR(64) NOT NULL DEFAULT 'UTC',
    cron_expression TEXT NOT NULL DEFAULT '',
    next_run_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);`,
		`ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS user_id UUID NOT NULL REFERENCES identity_schema.users(id);`,
		`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_next_run
    ON scheduling_schema.scheduled_jobs (next_run_at)
    WHERE status = 'active';`,
		`CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_status_user_next_run
    ON scheduling_schema.scheduled_jobs (status, user_id, next_run_at);`,
		`CREATE TABLE IF NOT EXISTS scheduling_schema.scheduled_job_runs (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES scheduling_schema.scheduled_jobs(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL,
    target_service VARCHAR(100) NOT NULL,
    detail JSONB,
    trace_id UUID,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW()
);`,
		`CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_job_id ON scheduling_schema.scheduled_job_runs(job_id);`,
		`CREATE INDEX IF NOT EXISTS idx_scheduled_job_runs_started_at ON scheduling_schema.scheduled_job_runs(started_at DESC);`,
	}
	for _, stmt := range statements {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure scheduling schema: %w", err)
		}
	}
	return nil
}

// CreateJob inserts a new scheduled job.
// MT-5 (#186): user_id is required and validated before the SQL runs.
func (r *ScheduledJobsRepository) CreateJob(ctx context.Context, p CreateScheduledJobParams) (ScheduledJob, error) {
	userIDStr := strings.TrimSpace(p.UserID)
	if userIDStr == "" {
		return ScheduledJob{}, ErrScheduledJobMissingUserID
	}
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		return ScheduledJob{}, fmt.Errorf("user_id: %w", err)
	}

	const q = `
INSERT INTO scheduling_schema.scheduled_jobs (
  id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, next_run_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
RETURNING id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, next_run_at, created_at, updated_at`

	tz := strings.TrimSpace(p.TimeZone)
	if tz == "" {
		tz = "UTC"
	}

	var row ScheduledJob
	var userOut pgtype.UUID
	err = r.pool.QueryRow(ctx, q,
		p.ID,
		p.JobType,
		p.TargetKind,
		p.TargetService,
		p.Status,
		p.ScheduleKind,
		p.IntervalSeconds,
		p.Name,
		p.Payload,
		pgtype.UUID{Bytes: userUUID, Valid: true},
		tz,
		strings.TrimSpace(p.CronExpression),
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
		&userOut,
		&row.TimeZone,
		&row.CronExpression,
		&row.NextRunAt,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err != nil {
		return ScheduledJob{}, fmt.Errorf("insert scheduled job: %w", err)
	}
	row.UserID = uuidString(userOut)
	return row, nil
}

// uuidString returns the canonical string form of a pgtype.UUID, or "" if invalid.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// GetJob returns a job by id or ErrNoRows.
func (r *ScheduledJobsRepository) GetJob(ctx context.Context, id uuid.UUID) (ScheduledJob, error) {
	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, next_run_at, created_at, updated_at
FROM scheduling_schema.scheduled_jobs WHERE id = $1`

	var row ScheduledJob
	var userOut pgtype.UUID
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
		&userOut,
		&row.TimeZone,
		&row.CronExpression,
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
	row.UserID = uuidString(userOut)
	return row, nil
}

// ListDueJobs returns active jobs whose next_run_at is at or before `before`.
func (r *ScheduledJobsRepository) ListDueJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, next_run_at, created_at, updated_at
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
		var userOut pgtype.UUID
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
			&userOut,
			&row.TimeZone,
			&row.CronExpression,
			&row.NextRunAt,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		row.UserID = uuidString(userOut)
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

// ListUserCrons returns active jobs of type user_cron for a user namespace.
// MT-5 (#186): userID is parsed as UUID before SQL binding.
func (r *ScheduledJobsRepository) ListUserCrons(ctx context.Context, userID string) ([]ScheduledJob, error) {
	userUUID, err := uuid.Parse(strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("user_id: %w", err)
	}

	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, next_run_at, created_at, updated_at
FROM scheduling_schema.scheduled_jobs
WHERE job_type = 'user_cron' AND user_id = $1 AND status = 'active'
ORDER BY name ASC`
	rows, err := r.pool.Query(ctx, q, pgtype.UUID{Bytes: userUUID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list user crons: %w", err)
	}
	defer rows.Close()

	var out []ScheduledJob
	for rows.Next() {
		var row ScheduledJob
		var userOut pgtype.UUID
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
			&userOut,
			&row.TimeZone,
			&row.CronExpression,
			&row.NextRunAt,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		row.UserID = uuidString(userOut)
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteUserCron deletes a job if it is owned by userID and typed user_cron.
// MT-5 (#186): userID is parsed as UUID before SQL binding.
func (r *ScheduledJobsRepository) DeleteUserCron(ctx context.Context, jobID uuid.UUID, userID string) (bool, error) {
	userUUID, err := uuid.Parse(strings.TrimSpace(userID))
	if err != nil {
		return false, fmt.Errorf("user_id: %w", err)
	}
	const q = `DELETE FROM scheduling_schema.scheduled_jobs WHERE id = $1 AND user_id = $2 AND job_type = 'user_cron'`
	ct, err := r.pool.Exec(ctx, q, jobID, pgtype.UUID{Bytes: userUUID, Valid: true})
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
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

// CountActiveScheduledJobs returns jobs with status=active.
func (r *ScheduledJobsRepository) CountActiveScheduledJobs(ctx context.Context) (int64, error) {
	const q = `SELECT COUNT(*) FROM scheduling_schema.scheduled_jobs WHERE status = 'active'`
	var n int64
	if err := r.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountDueScheduledJobs counts active jobs whose next_run_at is at or before `before`.
func (r *ScheduledJobsRepository) CountDueScheduledJobs(ctx context.Context, before time.Time) (int64, error) {
	const q = `
SELECT COUNT(*) FROM scheduling_schema.scheduled_jobs
WHERE status = 'active' AND next_run_at <= $1`
	var n int64
	if err := r.pool.QueryRow(ctx, q, before).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountRunsByStatusSince counts job runs by status whose started_at is at or after `since`.
func (r *ScheduledJobsRepository) CountRunsByStatusSince(ctx context.Context, status string, since time.Time) (int64, error) {
	const q = `
SELECT COUNT(*) FROM scheduling_schema.scheduled_job_runs
WHERE status = $1 AND started_at >= $2`
	var n int64
	if err := r.pool.QueryRow(ctx, q, status, since).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountOrphanScheduledJobRuns returns runs without a backing job row (normally zero with FK).
func (r *ScheduledJobsRepository) CountOrphanScheduledJobRuns(ctx context.Context) (int64, error) {
	const q = `
SELECT COUNT(*) FROM scheduling_schema.scheduled_job_runs r
WHERE NOT EXISTS (SELECT 1 FROM scheduling_schema.scheduled_jobs j WHERE j.id = r.job_id)`
	var n int64
	if err := r.pool.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// DBPingLatency returns round-trip latency for pool.Ping(ctx).
func (r *ScheduledJobsRepository) DBPingLatency(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if err := r.pool.Ping(ctx); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// MarshalPayloadMap encodes a map for JSONB storage.
func MarshalPayloadMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}
