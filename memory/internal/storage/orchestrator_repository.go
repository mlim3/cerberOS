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

var (
	ErrUnknownOrchestratorDataType = errors.New("unknown orchestrator data_type")
	ErrMissingPlanID               = errors.New("plan_id is required for this data_type")
	ErrMissingSubtaskID            = errors.New("subtask_id is required for this data_type")
	ErrOrchestratorRecordNotFound  = errors.New("orchestrator record not found")
)

var (
	orchestratorUpsertTypes = map[string]struct{}{
		"task_state":    {},
		"plan_state":    {},
		"subtask_state": {},
	}
	orchestratorAppendOnlyTypes = map[string]struct{}{
		"audit_log":      {},
		"recovery_event": {},
		"policy_event":   {},
	}
	terminalTaskStates = []string{
		"COMPLETED",
		"FAILED",
		"DELIVERY_FAILED",
		"TIMED_OUT",
		"POLICY_VIOLATION",
		"DECOMPOSITION_FAILED",
		"PARTIAL_COMPLETE",
	}
)

type OrchestratorRepository struct {
	pool *pgxpool.Pool
}

type OrchestratorRecord struct {
	ID                  pgtype.UUID        `json:"id"`
	OrchestratorTaskRef string             `json:"orchestrator_task_ref"`
	TaskID              string             `json:"task_id"`
	PlanID              pgtype.Text        `json:"plan_id"`
	SubtaskID           pgtype.Text        `json:"subtask_id"`
	TraceID             pgtype.Text        `json:"trace_id"`
	DataType            string             `json:"data_type"`
	Timestamp           pgtype.Timestamptz `json:"timestamp"`
	Payload             []byte             `json:"payload"`
	TTLSeconds          int32              `json:"ttl_seconds"`
	CreatedAt           pgtype.Timestamptz `json:"created_at"`
}

type WriteOrchestratorRecordParams struct {
	OrchestratorTaskRef string
	TaskID              string
	PlanID              string
	SubtaskID           string
	TraceID             string
	DataType            string
	Timestamp           time.Time
	Payload             []byte
	TTLSeconds          int32
}

type QueryOrchestratorRecordsParams struct {
	DataType            string
	TaskID              string
	OrchestratorTaskRef string
	FromTimestamp       *time.Time
	ToTimestamp         *time.Time
	StateFilter         string
}

func NewOrchestratorRepository(pool *pgxpool.Pool) *OrchestratorRepository {
	return &OrchestratorRepository{pool: pool}
}

func ValidOrchestratorDataType(dataType string) bool {
	_, ok := orchestratorUpsertTypes[dataType]
	if ok {
		return true
	}
	_, ok = orchestratorAppendOnlyTypes[dataType]
	return ok
}

func (r *OrchestratorRepository) EnsureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS orchestrator_schema;`,
		`CREATE TABLE IF NOT EXISTS orchestrator_schema.orchestrator_records (
    id UUID PRIMARY KEY,
    orchestrator_task_ref TEXT NOT NULL,
    task_id TEXT NOT NULL,
    plan_id TEXT,
    subtask_id TEXT,
    trace_id VARCHAR(64),
    data_type TEXT NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    ttl_seconds INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`,
		`CREATE INDEX IF NOT EXISTS idx_orch_records_task_id_type
    ON orchestrator_schema.orchestrator_records (task_id, data_type, timestamp DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_orch_records_orch_ref_type
    ON orchestrator_schema.orchestrator_records (orchestrator_task_ref, data_type, timestamp DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_orch_records_type_timestamp
    ON orchestrator_schema.orchestrator_records (data_type, timestamp DESC);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_orch_records_task_state_upsert
    ON orchestrator_schema.orchestrator_records (task_id, data_type)
    WHERE data_type = 'task_state';`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_orch_records_plan_state_upsert
    ON orchestrator_schema.orchestrator_records (task_id, plan_id, data_type)
    WHERE data_type = 'plan_state';`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_orch_records_subtask_state_upsert
    ON orchestrator_schema.orchestrator_records (task_id, subtask_id, data_type)
    WHERE data_type = 'subtask_state';`,
		`CREATE OR REPLACE FUNCTION orchestrator_schema.reject_append_only_mutation()
RETURNS trigger AS $$
BEGIN
    IF OLD.data_type IN ('audit_log', 'recovery_event', 'policy_event') THEN
        RAISE EXCEPTION 'append-only orchestrator record type cannot be mutated: %', OLD.data_type;
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;`,
		`DROP TRIGGER IF EXISTS trg_orch_records_no_update_append_only ON orchestrator_schema.orchestrator_records;`,
		`CREATE TRIGGER trg_orch_records_no_update_append_only
    BEFORE UPDATE ON orchestrator_schema.orchestrator_records
    FOR EACH ROW
    EXECUTE FUNCTION orchestrator_schema.reject_append_only_mutation();`,
		`DROP TRIGGER IF EXISTS trg_orch_records_no_delete_append_only ON orchestrator_schema.orchestrator_records;`,
		`CREATE TRIGGER trg_orch_records_no_delete_append_only
    BEFORE DELETE ON orchestrator_schema.orchestrator_records
    FOR EACH ROW
    EXECUTE FUNCTION orchestrator_schema.reject_append_only_mutation();`,
	}

	for _, stmt := range statements {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure orchestrator schema: %w", err)
		}
	}
	return nil
}

func (r *OrchestratorRepository) WriteRecord(ctx context.Context, params WriteOrchestratorRecordParams) (OrchestratorRecord, error) {
	if !ValidOrchestratorDataType(params.DataType) {
		return OrchestratorRecord{}, ErrUnknownOrchestratorDataType
	}
	if params.DataType == "plan_state" && params.PlanID == "" {
		return OrchestratorRecord{}, ErrMissingPlanID
	}
	if params.DataType == "subtask_state" {
		if params.PlanID == "" {
			return OrchestratorRecord{}, ErrMissingPlanID
		}
		if params.SubtaskID == "" {
			return OrchestratorRecord{}, ErrMissingSubtaskID
		}
	}

	if _, ok := orchestratorAppendOnlyTypes[params.DataType]; ok {
		return r.insertRecord(ctx, params)
	}
	return r.upsertRecord(ctx, params)
}

func (r *OrchestratorRepository) insertRecord(ctx context.Context, params WriteOrchestratorRecordParams) (OrchestratorRecord, error) {
	recordID, err := uuid.NewV7()
	if err != nil {
		return OrchestratorRecord{}, err
	}
	row := r.pool.QueryRow(ctx, `
INSERT INTO orchestrator_schema.orchestrator_records (
    id, orchestrator_task_ref, task_id, plan_id, subtask_id, trace_id, data_type, timestamp, payload, ttl_seconds, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
RETURNING id, orchestrator_task_ref, task_id, plan_id, subtask_id, trace_id, data_type, timestamp, payload, ttl_seconds, created_at`,
		pgtype.UUID{Bytes: recordID, Valid: true},
		params.OrchestratorTaskRef,
		params.TaskID,
		textValue(params.PlanID),
		textValue(params.SubtaskID),
		textValue(params.TraceID),
		params.DataType,
		pgtype.Timestamptz{Time: params.Timestamp.UTC(), Valid: true},
		params.Payload,
		params.TTLSeconds,
	)
	return scanOrchestratorRecord(row)
}

func (r *OrchestratorRepository) upsertRecord(ctx context.Context, params WriteOrchestratorRecordParams) (OrchestratorRecord, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return OrchestratorRecord{}, err
	}
	defer tx.Rollback(ctx)

	var deleteSQL string
	var deleteArgs []any
	switch params.DataType {
	case "task_state":
		deleteSQL = `
DELETE FROM orchestrator_schema.orchestrator_records
WHERE task_id = $1 AND data_type = $2`
		deleteArgs = []any{params.TaskID, params.DataType}
	case "plan_state":
		deleteSQL = `
DELETE FROM orchestrator_schema.orchestrator_records
WHERE task_id = $1 AND plan_id = $2 AND data_type = $3`
		deleteArgs = []any{params.TaskID, params.PlanID, params.DataType}
	case "subtask_state":
		deleteSQL = `
DELETE FROM orchestrator_schema.orchestrator_records
WHERE task_id = $1 AND plan_id = $2 AND subtask_id = $3 AND data_type = $4`
		deleteArgs = []any{params.TaskID, params.PlanID, params.SubtaskID, params.DataType}
	default:
		return OrchestratorRecord{}, ErrUnknownOrchestratorDataType
	}

	if _, err := tx.Exec(ctx, deleteSQL, deleteArgs...); err != nil {
		return OrchestratorRecord{}, err
	}

	recordID, err := uuid.NewV7()
	if err != nil {
		return OrchestratorRecord{}, err
	}
	insertRow := tx.QueryRow(ctx, `
INSERT INTO orchestrator_schema.orchestrator_records (
    id, orchestrator_task_ref, task_id, plan_id, subtask_id, trace_id, data_type, timestamp, payload, ttl_seconds, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
RETURNING id, orchestrator_task_ref, task_id, plan_id, subtask_id, trace_id, data_type, timestamp, payload, ttl_seconds, created_at`,
		pgtype.UUID{Bytes: recordID, Valid: true},
		params.OrchestratorTaskRef,
		params.TaskID,
		textValue(params.PlanID),
		textValue(params.SubtaskID),
		textValue(params.TraceID),
		params.DataType,
		pgtype.Timestamptz{Time: params.Timestamp.UTC(), Valid: true},
		params.Payload,
		params.TTLSeconds,
	)
	record, err := scanOrchestratorRecord(insertRow)
	if err != nil {
		return OrchestratorRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OrchestratorRecord{}, err
	}
	return record, nil
}

func (r *OrchestratorRepository) QueryRecords(ctx context.Context, params QueryOrchestratorRecordsParams) ([]OrchestratorRecord, error) {
	if !ValidOrchestratorDataType(params.DataType) {
		return nil, ErrUnknownOrchestratorDataType
	}

	args := []any{params.DataType}
	q := `
SELECT id, orchestrator_task_ref, task_id, plan_id, subtask_id, trace_id, data_type, timestamp, payload, ttl_seconds, created_at
FROM orchestrator_schema.orchestrator_records
WHERE data_type = $1`

	if params.TaskID != "" {
		args = append(args, params.TaskID)
		q += fmt.Sprintf(" AND task_id = $%d", len(args))
	}
	if params.OrchestratorTaskRef != "" {
		args = append(args, params.OrchestratorTaskRef)
		q += fmt.Sprintf(" AND orchestrator_task_ref = $%d", len(args))
	}
	if params.FromTimestamp != nil {
		args = append(args, pgtype.Timestamptz{Time: params.FromTimestamp.UTC(), Valid: true})
		q += fmt.Sprintf(" AND timestamp >= $%d", len(args))
	}
	if params.ToTimestamp != nil {
		args = append(args, pgtype.Timestamptz{Time: params.ToTimestamp.UTC(), Valid: true})
		q += fmt.Sprintf(" AND timestamp <= $%d", len(args))
	}
	if params.StateFilter == "not_terminal" {
		q += ` AND COALESCE(payload->>'state', '') NOT IN ('COMPLETED','FAILED','DELIVERY_FAILED','TIMED_OUT','POLICY_VIOLATION','DECOMPOSITION_FAILED','PARTIAL_COMPLETE')`
	}
	q += ` ORDER BY timestamp ASC`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]OrchestratorRecord, 0)
	for rows.Next() {
		rec, err := scanOrchestratorRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *OrchestratorRepository) ReadLatest(ctx context.Context, taskID, dataType string) (OrchestratorRecord, error) {
	if !ValidOrchestratorDataType(dataType) {
		return OrchestratorRecord{}, ErrUnknownOrchestratorDataType
	}
	row := r.pool.QueryRow(ctx, `
SELECT id, orchestrator_task_ref, task_id, plan_id, subtask_id, trace_id, data_type, timestamp, payload, ttl_seconds, created_at
FROM orchestrator_schema.orchestrator_records
WHERE task_id = $1 AND data_type = $2
ORDER BY timestamp DESC
LIMIT 1`, taskID, dataType)
	rec, err := scanOrchestratorRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrchestratorRecord{}, ErrOrchestratorRecordNotFound
	}
	return rec, err
}

func scanOrchestratorRecord(scanner interface {
	Scan(dest ...any) error
}) (OrchestratorRecord, error) {
	var rec OrchestratorRecord
	err := scanner.Scan(
		&rec.ID,
		&rec.OrchestratorTaskRef,
		&rec.TaskID,
		&rec.PlanID,
		&rec.SubtaskID,
		&rec.TraceID,
		&rec.DataType,
		&rec.Timestamp,
		&rec.Payload,
		&rec.TTLSeconds,
		&rec.CreatedAt,
	)
	return rec, err
}

func textValue(v string) pgtype.Text {
	if v == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: v, Valid: true}
}

func (r OrchestratorRecord) PayloadJSON() any {
	var payload any
	if len(r.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Payload, &payload); err != nil {
		return string(r.Payload)
	}
	return payload
}
