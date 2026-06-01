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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlim3/cerberOS/memory/internal/scheduleutil"
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
	State           []byte
	NextRunAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UserCronDispatch publishes a due user_cron job to NATS when configured.
type UserCronDispatch func(ctx context.Context, job ScheduledJob, runID uuid.UUID) error

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
	State           []byte
	NextRunAt       time.Time
}

// IdempotencyRecord tracks whether a cross-run side effect has already been claimed.
type IdempotencyRecord struct {
	Key         string
	Status      string
	AgentID     string
	JobID       string
	RunID       string
	Result      []byte
	ClaimedAt   time.Time
	CompletedAt pgtype.Timestamptz
	ExpiresAt   time.Time
}

type ClaimIdempotencyParams struct {
	Key        string
	AgentID    string
	JobID      string
	RunID      string
	TTLSeconds int
}

type ClaimIdempotencyResult struct {
	Claimed bool
	Record  IdempotencyRecord
}

type CompleteIdempotencyParams struct {
	Key      string
	Status   string
	Result   []byte
	JobID    string
	RunID    string
	JobState []byte
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
//     identity_schema.users(id). Pre-MT-5 rows used VARCHAR(64) DEFAULT ”
//     which cannot satisfy the UUID type, so a dev-DB-acceptable wipe runs
//     before the type change. Wrapped in EXCEPTION blocks so a fresh DB (no
//     prior column or table) silently no-ops.
//   - The (status, user_id, next_run_at) composite index is created.
func (r *ScheduledJobsRepository) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS scheduling_schema;`,
		// Only legacy schemas used VARCHAR user_id. Detect that specific shape
		// before taking the destructive migration path, so normal restarts on
		// an already-migrated DB do not wipe scheduled jobs or run history.
		`DO $$ BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'scheduling_schema'
          AND table_name = 'scheduled_jobs'
          AND column_name = 'user_id'
          AND data_type = 'character varying'
    ) THEN
        TRUNCATE TABLE scheduling_schema.scheduled_jobs CASCADE;
        ALTER TABLE scheduling_schema.scheduled_jobs DROP COLUMN user_id;
    END IF;
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
    state JSONB NOT NULL DEFAULT '{}'::jsonb,
    next_run_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);`,
		// Safe no-op on freshly initialized or already-migrated DBs. The
		// legacy VARCHAR case above clears rows before the NOT NULL add.
		`ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS user_id UUID NOT NULL REFERENCES identity_schema.users(id);`,
		`ALTER TABLE scheduling_schema.scheduled_jobs ADD COLUMN IF NOT EXISTS state JSONB NOT NULL DEFAULT '{}'::jsonb;`,
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
		`CREATE TABLE IF NOT EXISTS scheduling_schema.idempotency_records (
    key TEXT PRIMARY KEY,
    status VARCHAR(50) NOT NULL DEFAULT 'claimed',
    agent_id TEXT NOT NULL,
    job_id UUID,
    run_id UUID,
    result JSONB,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL
);`,
		`CREATE INDEX IF NOT EXISTS idx_idempotency_records_expires_at ON scheduling_schema.idempotency_records(expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_idempotency_records_claimed_at ON scheduling_schema.idempotency_records(status, claimed_at);`,
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
  interval_seconds, name, payload, user_id, time_zone, cron_expression, state, next_run_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
RETURNING id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, state, next_run_at, created_at, updated_at`

	tz := strings.TrimSpace(p.TimeZone)
	if tz == "" {
		tz = "UTC"
	}
	state := p.State
	if len(state) == 0 {
		state = []byte(`{}`)
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
		state,
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
		&row.State,
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
  interval_seconds, name, payload, user_id, time_zone, cron_expression, state, next_run_at, created_at, updated_at
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
		&row.State,
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
// This is retained for contract tests and diagnostics; production dispatch uses ClaimDueJobs.
func (r *ScheduledJobsRepository) ListDueJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, state, next_run_at, created_at, updated_at
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
		row, err := scanScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ClaimDueJobs locks due jobs, advances next_run_at before dispatch, and returns the claimed rows.
func (r *ScheduledJobsRepository) ClaimDueJobs(ctx context.Context, before time.Time) ([]ScheduledJob, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("claim due jobs: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
SELECT id, job_type, target_kind, target_service, status, schedule_kind,
  interval_seconds, name, payload, user_id, time_zone, cron_expression, state, next_run_at, created_at, updated_at
FROM scheduling_schema.scheduled_jobs
WHERE status = 'active' AND next_run_at <= $1
ORDER BY next_run_at ASC
FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(ctx, q, before)
	if err != nil {
		return nil, fmt.Errorf("claim due jobs: query: %w", err)
	}
	defer rows.Close()

	var out []ScheduledJob
	for rows.Next() {
		row, err := scanScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for i := range out {
		next := computeClaimedNextRun(out[i], before)
		if err := updateJobNextRunTx(ctx, tx, out[i].ID, next); err != nil {
			return nil, err
		}
		out[i].NextRunAt = next
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim due jobs: commit: %w", err)
	}
	return out, nil
}

// UpdateJobNextRun sets next_run_at and bumps updated_at.
func (r *ScheduledJobsRepository) UpdateJobNextRun(ctx context.Context, jobID uuid.UUID, next time.Time) error {
	return updateJobNextRunExec(ctx, r.pool, jobID, next)
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

// UpdateRunStatus updates a run row after dispatch while preserving a later
// complete_action update if one already finalized the same run.
func (r *ScheduledJobsRepository) UpdateRunStatus(ctx context.Context, runID uuid.UUID, status string, detail []byte, finishedAt pgtype.Timestamptz) error {
	if len(detail) == 0 {
		detail = []byte(`{}`)
	}
	const q = `
UPDATE scheduling_schema.scheduled_job_runs
SET status = $2,
    detail = $3,
    finished_at = CASE WHEN $4::timestamptz IS NULL THEN finished_at ELSE $4 END
WHERE id = $1 AND finished_at IS NULL`
	_, err := r.pool.Exec(ctx, q, runID, status, detail, finishedAt)
	if err != nil {
		return fmt.Errorf("update scheduled job run status: %w", err)
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
  interval_seconds, name, payload, user_id, time_zone, cron_expression, state, next_run_at, created_at, updated_at
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
		row, err := scanScheduledJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// NextDueTime returns the next active job after `after`.
func (r *ScheduledJobsRepository) NextDueTime(ctx context.Context, after time.Time) (time.Time, bool, error) {
	const q = `
SELECT MIN(next_run_at)
FROM scheduling_schema.scheduled_jobs
WHERE status = 'active' AND next_run_at > $1`
	var next pgtype.Timestamptz
	if err := r.pool.QueryRow(ctx, q, after).Scan(&next); err != nil {
		return time.Time{}, false, err
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return next.Time.UTC(), true, nil
}

// UpdateJobState persists the latest durable state for a scheduled job.
func (r *ScheduledJobsRepository) UpdateJobState(ctx context.Context, jobID uuid.UUID, state []byte) error {
	if len(state) == 0 {
		state = []byte(`{}`)
	}
	const q = `
UPDATE scheduling_schema.scheduled_jobs
SET state = $2, updated_at = NOW()
WHERE id = $1`
	ct, err := r.pool.Exec(ctx, q, jobID, state)
	if err != nil {
		return fmt.Errorf("update job state: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *ScheduledJobsRepository) ClaimIdempotency(ctx context.Context, p ClaimIdempotencyParams) (ClaimIdempotencyResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClaimIdempotencyResult{}, fmt.Errorf("claim idempotency: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `DELETE FROM scheduling_schema.idempotency_records WHERE key = $1 AND expires_at <= $2`, p.Key, now); err != nil {
		return ClaimIdempotencyResult{}, fmt.Errorf("claim idempotency: prune expired: %w", err)
	}

	expiresAt := now.Add(time.Duration(p.TTLSeconds) * time.Second)
	var jobUUID pgtype.UUID
	if id, err := parseUUIDOptional(p.JobID); err == nil && id != uuid.Nil {
		jobUUID = pgtype.UUID{Bytes: id, Valid: true}
	}
	var runUUID pgtype.UUID
	if id, err := parseUUIDOptional(p.RunID); err == nil && id != uuid.Nil {
		runUUID = pgtype.UUID{Bytes: id, Valid: true}
	}

	const insertQ = `
INSERT INTO scheduling_schema.idempotency_records (
  key, status, agent_id, job_id, run_id, expires_at
) VALUES ($1, 'claimed', $2, $3, $4, $5)
ON CONFLICT (key) DO NOTHING
RETURNING key, status, agent_id, job_id, run_id, result, claimed_at, completed_at, expires_at`
	var rec IdempotencyRecord
	if err := tx.QueryRow(ctx, insertQ, p.Key, p.AgentID, jobUUID, runUUID, expiresAt).Scan(
		&rec.Key,
		&rec.Status,
		&rec.AgentID,
		&jobUUID,
		&runUUID,
		&rec.Result,
		&rec.ClaimedAt,
		&rec.CompletedAt,
		&rec.ExpiresAt,
	); err == nil {
		rec.JobID = uuidString(jobUUID)
		rec.RunID = uuidString(runUUID)
		if err := tx.Commit(ctx); err != nil {
			return ClaimIdempotencyResult{}, fmt.Errorf("claim idempotency: commit inserted: %w", err)
		}
		return ClaimIdempotencyResult{Claimed: true, Record: rec}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ClaimIdempotencyResult{}, fmt.Errorf("claim idempotency: insert: %w", err)
	}

	const selectQ = `
SELECT key, status, agent_id, job_id, run_id, result, claimed_at, completed_at, expires_at
FROM scheduling_schema.idempotency_records
WHERE key = $1`
	if err := tx.QueryRow(ctx, selectQ, p.Key).Scan(
		&rec.Key,
		&rec.Status,
		&rec.AgentID,
		&jobUUID,
		&runUUID,
		&rec.Result,
		&rec.ClaimedAt,
		&rec.CompletedAt,
		&rec.ExpiresAt,
	); err != nil {
		return ClaimIdempotencyResult{}, fmt.Errorf("claim idempotency: read existing: %w", err)
	}
	rec.JobID = uuidString(jobUUID)
	rec.RunID = uuidString(runUUID)
	if err := tx.Commit(ctx); err != nil {
		return ClaimIdempotencyResult{}, fmt.Errorf("claim idempotency: commit existing: %w", err)
	}
	return ClaimIdempotencyResult{Claimed: false, Record: rec}, nil
}

func (r *ScheduledJobsRepository) CompleteIdempotency(ctx context.Context, p CompleteIdempotencyParams) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("complete idempotency: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	status := strings.TrimSpace(p.Status)
	if status == "" {
		status = "completed"
	}
	result := p.Result
	if len(result) == 0 {
		result = []byte(`{}`)
	}
	const updateClaimQ = `
UPDATE scheduling_schema.idempotency_records
SET status = $2, result = $3, completed_at = NOW()
WHERE key = $1`
	if ct, err := tx.Exec(ctx, updateClaimQ, p.Key, status, result); err != nil {
		return fmt.Errorf("complete idempotency: update claim: %w", err)
	} else if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	if id, err := parseUUIDOptional(p.JobID); err == nil && id != uuid.Nil && len(p.JobState) > 0 {
		const updateStateQ = `
UPDATE scheduling_schema.scheduled_jobs
SET state = $2, updated_at = NOW()
WHERE id = $1`
		if ct, err := tx.Exec(ctx, updateStateQ, id, p.JobState); err != nil {
			return fmt.Errorf("complete idempotency: update job state: %w", err)
		} else if ct.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
	}

	if id, err := parseUUIDOptional(p.RunID); err == nil && id != uuid.Nil {
		const updateRunQ = `
UPDATE scheduling_schema.scheduled_job_runs
SET status = $2, detail = $3, finished_at = NOW()
WHERE id = $1`
		if _, err := tx.Exec(ctx, updateRunQ, id, status, result); err != nil {
			return fmt.Errorf("complete idempotency: update run detail: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("complete idempotency: commit: %w", err)
	}
	return nil
}

func (r *ScheduledJobsRepository) CleanupIdempotency(ctx context.Context, now, staleClaimBefore time.Time) (int64, int64, error) {
	expiredCT, err := r.pool.Exec(ctx,
		`DELETE FROM scheduling_schema.idempotency_records WHERE expires_at <= $1`,
		now.UTC(),
	)
	if err != nil {
		return 0, 0, fmt.Errorf("cleanup idempotency expired: %w", err)
	}
	staleCT, err := r.pool.Exec(ctx,
		`DELETE FROM scheduling_schema.idempotency_records WHERE status = 'claimed' AND claimed_at < $1`,
		staleClaimBefore.UTC(),
	)
	if err != nil {
		return expiredCT.RowsAffected(), 0, fmt.Errorf("cleanup idempotency stale: %w", err)
	}
	return expiredCT.RowsAffected(), staleCT.RowsAffected(), nil
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

func updateJobNextRunExec(ctx context.Context, exec interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, jobID uuid.UUID, next time.Time) error {
	const q = `
UPDATE scheduling_schema.scheduled_jobs
SET next_run_at = $2, updated_at = NOW()
WHERE id = $1`
	ct, err := exec.Exec(ctx, q, jobID, next)
	if err != nil {
		return fmt.Errorf("update job next_run: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func updateJobNextRunTx(ctx context.Context, tx pgx.Tx, jobID uuid.UUID, next time.Time) error {
	return updateJobNextRunExec(ctx, tx, jobID, next)
}

func computeClaimedNextRun(job ScheduledJob, from time.Time) time.Time {
	switch job.ScheduleKind {
	case "cron":
		interval := int32(0)
		if job.IntervalSeconds.Valid {
			interval = job.IntervalSeconds.Int32
		}
		next := scheduleutil.NextRunTime("cron", job.CronExpression, job.TimeZone, interval, from)
		if !next.After(from) {
			return from.Add(time.Minute).UTC()
		}
		return next.UTC()
	case "interval":
		if job.IntervalSeconds.Valid && job.IntervalSeconds.Int32 > 0 {
			return from.Add(time.Duration(job.IntervalSeconds.Int32) * time.Second).UTC()
		}
	}
	return from.Add(time.Minute).UTC()
}

func scanScheduledJob(row pgx.Row) (ScheduledJob, error) {
	var out ScheduledJob
	var userOut pgtype.UUID
	if err := row.Scan(
		&out.ID,
		&out.JobType,
		&out.TargetKind,
		&out.TargetService,
		&out.Status,
		&out.ScheduleKind,
		&out.IntervalSeconds,
		&out.Name,
		&out.Payload,
		&userOut,
		&out.TimeZone,
		&out.CronExpression,
		&out.State,
		&out.NextRunAt,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return ScheduledJob{}, err
	}
	out.UserID = uuidString(userOut)
	if len(out.State) == 0 {
		out.State = []byte(`{}`)
	}
	return out, nil
}

func parseUUIDOptional(raw string) (uuid.UUID, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(s)
}
