package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type VaultRepository struct {
	*BaseRepository
}

func NewVaultRepository(pool *pgxpool.Pool) *VaultRepository {
	return &VaultRepository{
		BaseRepository: &BaseRepository{Pool: pool},
	}
}

func (r *VaultRepository) GetSecretByKey(ctx context.Context, arg GetSecretByKeyParams) (VaultSchemaSecret, error) {
	queries := New(r.Pool)
	return queries.GetSecretByKey(ctx, arg)
}

func (r *VaultRepository) SaveSecret(ctx context.Context, arg SaveSecretParams) error {
	queries := New(r.Pool)
	return queries.SaveSecret(ctx, arg)
}
