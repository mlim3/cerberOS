package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository defines the interface for database operations, including transactions
type Repository interface {
	Querier() *Queries
	WithTx(ctx context.Context, fn func(q *Queries) error) error
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
