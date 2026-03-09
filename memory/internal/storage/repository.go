package storage

import (
	"context"

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
}

type PersonalInfoChunkMatch struct {
	Chunk    PersonalInfoSchemaPersonalInfoChunk
	Distance float64
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
