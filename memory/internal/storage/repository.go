package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// Repository defines the interface for database operations, including transactions
type Repository interface {
	Querier() *Queries
	WithTx(ctx context.Context, fn func(q *Queries) error) error
	UserExists(ctx context.Context, userID pgtype.UUID) (bool, error)
	QueryChunksBySimilarity(ctx context.Context, userID pgtype.UUID, embedding pgvector.Vector, limit int32) ([]PersonalInfoChunkMatch, error)
	ListFacts(ctx context.Context, userID pgtype.UUID, includeArchived bool) ([]FactRecord, error)
	ArchiveFact(ctx context.Context, userID, factID pgtype.UUID, reason string) error
	SupersedeFact(ctx context.Context, userID, factID pgtype.UUID, category pgtype.Text, factKey string, factValue []byte, confidence float64) (pgtype.UUID, error)
}

type PersonalInfoChunkMatch struct {
	Chunk    PersonalInfoSchemaPersonalInfoChunk
	Distance float64
}

type FactRecord struct {
	ID                 pgtype.UUID
	UserID             pgtype.UUID
	Category           pgtype.Text
	FactKey            string
	FactValue          []byte
	Confidence         pgtype.Float8
	Version            pgtype.Int4
	UpdatedAt          pgtype.Timestamptz
	ArchiveReason      pgtype.Text
	SupersededByFactID pgtype.UUID
	ArchivedAt         pgtype.Timestamptz
	IsArchived         bool
}

// BaseRepository implements Repository
type BaseRepository struct {
	Pool *pgxpool.Pool
}

func (r *BaseRepository) Querier() *Queries {
	return New(r.Pool)
}

func (r *BaseRepository) WithTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := New(tx)
	if err := fn(q); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BaseRepository) UserExists(ctx context.Context, userID pgtype.UUID) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM identity_schema.users WHERE id = $1)`
	var exists bool
	err := r.Pool.QueryRow(ctx, q, userID).Scan(&exists)
	return exists, err
}

func (r *BaseRepository) QueryChunksBySimilarity(ctx context.Context, userID pgtype.UUID, embedding pgvector.Vector, limit int32) ([]PersonalInfoChunkMatch, error) {
	if limit <= 0 {
		limit = 5
	}

	const q = `
SELECT id, user_id, raw_text, embedding, model_version, created_at, (embedding <=> $2) AS distance
FROM personal_info_schema.personal_info_chunks
WHERE user_id = $1
ORDER BY embedding <=> $2 ASC, created_at DESC
LIMIT $3`

	rows, err := r.Pool.Query(ctx, q, userID, embedding, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	matches := make([]PersonalInfoChunkMatch, 0)
	for rows.Next() {
		var m PersonalInfoChunkMatch
		if err := rows.Scan(
			&m.Chunk.ID,
			&m.Chunk.UserID,
			&m.Chunk.RawText,
			&m.Chunk.Embedding,
			&m.Chunk.ModelVersion,
			&m.Chunk.CreatedAt,
			&m.Distance,
		); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return matches, nil
}

func (r *BaseRepository) ListFacts(ctx context.Context, userID pgtype.UUID, includeArchived bool) ([]FactRecord, error) {
	if err := r.ensureLifecycleTables(ctx); err != nil {
		return nil, err
	}

	const activeFactsQuery = `
SELECT id, user_id, category, fact_key, fact_value, confidence, version, updated_at
FROM personal_info_schema.user_facts
WHERE user_id = $1`

	rows, err := r.Pool.Query(ctx, activeFactsQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	facts := make([]FactRecord, 0)
	for rows.Next() {
		var fact FactRecord
		if err := rows.Scan(
			&fact.ID,
			&fact.UserID,
			&fact.Category,
			&fact.FactKey,
			&fact.FactValue,
			&fact.Confidence,
			&fact.Version,
			&fact.UpdatedAt,
		); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if !includeArchived {
		return facts, nil
	}

	const archivedFactsQuery = `
SELECT fact_id, user_id, category, fact_key, fact_value, confidence, version, original_updated_at,
       archive_reason, superseded_by_fact_id, archived_at
FROM personal_info_schema.user_facts_archive
WHERE user_id = $1
ORDER BY archived_at DESC`

	archivedRows, err := r.Pool.Query(ctx, archivedFactsQuery, userID)
	if err != nil {
		return nil, err
	}
	defer archivedRows.Close()

	for archivedRows.Next() {
		var fact FactRecord
		fact.IsArchived = true
		if err := archivedRows.Scan(
			&fact.ID,
			&fact.UserID,
			&fact.Category,
			&fact.FactKey,
			&fact.FactValue,
			&fact.Confidence,
			&fact.Version,
			&fact.UpdatedAt,
			&fact.ArchiveReason,
			&fact.SupersededByFactID,
			&fact.ArchivedAt,
		); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	if err := archivedRows.Err(); err != nil {
		return nil, err
	}

	return facts, nil
}

func (r *BaseRepository) ArchiveFact(ctx context.Context, userID, factID pgtype.UUID, reason string) error {
	if err := r.ensureLifecycleTables(ctx); err != nil {
		return err
	}

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	fact, err := getFactForUpdate(ctx, tx, userID, factID)
	if err != nil {
		return err
	}

	if err := insertArchivedFact(ctx, tx, fact, reason, pgtype.UUID{}); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
DELETE FROM personal_info_schema.user_facts
WHERE user_id = $1 AND id = $2`, userID, factID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *BaseRepository) SupersedeFact(ctx context.Context, userID, factID pgtype.UUID, category pgtype.Text, factKey string, factValue []byte, confidence float64) (pgtype.UUID, error) {
	if err := r.ensureLifecycleTables(ctx); err != nil {
		return pgtype.UUID{}, err
	}

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, err
	}
	defer tx.Rollback(ctx)

	oldFact, err := getFactForUpdate(ctx, tx, userID, factID)
	if err != nil {
		return pgtype.UUID{}, err
	}

	newFactIDUUID, err := uuid.NewV7()
	if err != nil {
		return pgtype.UUID{}, err
	}
	newFactID := pgtype.UUID{Bytes: newFactIDUUID, Valid: true}
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}

	if !category.Valid {
		category = oldFact.Category
	}
	if factKey == "" {
		factKey = oldFact.FactKey
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO personal_info_schema.user_facts (
	id, user_id, category, fact_key, fact_value, confidence, version, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		newFactID,
		userID,
		category,
		factKey,
		factValue,
		pgtype.Float8{Float64: confidence, Valid: true},
		pgtype.Int4{Int32: 1, Valid: true},
		now,
	); err != nil {
		return pgtype.UUID{}, err
	}

	if err := insertArchivedFact(ctx, tx, oldFact, "superseded", newFactID); err != nil {
		return pgtype.UUID{}, err
	}

	if _, err := tx.Exec(ctx, `
DELETE FROM personal_info_schema.user_facts
WHERE user_id = $1 AND id = $2`, userID, factID); err != nil {
		return pgtype.UUID{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return pgtype.UUID{}, err
	}

	return newFactID, nil
}

func (r *BaseRepository) ensureLifecycleTables(ctx context.Context) error {
	const createArchiveTable = `
CREATE TABLE IF NOT EXISTS personal_info_schema.user_facts_archive (
    archive_id UUID PRIMARY KEY,
    fact_id UUID NOT NULL,
    user_id UUID NOT NULL,
    category VARCHAR(50),
    fact_key VARCHAR(100) NOT NULL,
    fact_value JSONB NOT NULL,
    confidence FLOAT,
    version INT,
    archive_reason VARCHAR(50) NOT NULL,
    superseded_by_fact_id UUID,
    archived_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    original_updated_at TIMESTAMPTZ
)`

	if _, err := r.Pool.Exec(ctx, createArchiveTable); err != nil {
		return fmt.Errorf("ensure archive table: %w", err)
	}
	return nil
}

func getFactForUpdate(ctx context.Context, tx pgx.Tx, userID, factID pgtype.UUID) (FactRecord, error) {
	const q = `
SELECT id, user_id, category, fact_key, fact_value, confidence, version, updated_at
FROM personal_info_schema.user_facts
WHERE user_id = $1 AND id = $2
FOR UPDATE`

	var fact FactRecord
	err := tx.QueryRow(ctx, q, userID, factID).Scan(
		&fact.ID,
		&fact.UserID,
		&fact.Category,
		&fact.FactKey,
		&fact.FactValue,
		&fact.Confidence,
		&fact.Version,
		&fact.UpdatedAt,
	)
	return fact, err
}

func insertArchivedFact(ctx context.Context, tx pgx.Tx, fact FactRecord, reason string, supersededBy pgtype.UUID) error {
	archiveID, err := uuid.NewV7()
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
INSERT INTO personal_info_schema.user_facts_archive (
	archive_id, fact_id, user_id, category, fact_key, fact_value, confidence, version,
	archive_reason, superseded_by_fact_id, archived_at, original_updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		pgtype.UUID{Bytes: archiveID, Valid: true},
		fact.ID,
		fact.UserID,
		fact.Category,
		fact.FactKey,
		fact.FactValue,
		fact.Confidence,
		fact.Version,
		reason,
		supersededBy,
		pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		fact.UpdatedAt,
	)
	return err
}
