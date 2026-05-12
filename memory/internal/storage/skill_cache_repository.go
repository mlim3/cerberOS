package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type SkillCacheRepository struct {
	pool *pgxpool.Pool
}

func NewSkillCacheRepository(pool *pgxpool.Pool) *SkillCacheRepository {
	return &SkillCacheRepository{pool: pool}
}

// EnsureSchema creates the agents_schema and skill_cache table when they do
// not already exist. dim is the embedding vector dimension (e.g. 640).
// Called once at server startup so the table is available even on clusters
// whose postgres init-db.sql predates the agents_schema addition.
func (r *SkillCacheRepository) EnsureSchema(ctx context.Context, dim int) error {
	if dim <= 0 {
		dim = 640
	}
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS agents_schema;`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS agents_schema.skill_cache (
    id          UUID PRIMARY KEY,
    domain      TEXT NOT NULL,
    name        TEXT NOT NULL,
    origin      TEXT NOT NULL,
    description TEXT NOT NULL,
    payload     JSONB NOT NULL,
    embedding   VECTOR(%d),
    seed_hash   TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (domain, name)
);`, dim),
		`CREATE INDEX IF NOT EXISTS idx_skill_cache_domain ON agents_schema.skill_cache(domain);`,
		`CREATE INDEX IF NOT EXISTS idx_skill_cache_embedding ON agents_schema.skill_cache USING hnsw (embedding vector_cosine_ops);`,
	}
	for _, stmt := range statements {
		if _, err := r.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("skill_cache EnsureSchema: %w", err)
		}
	}
	return nil
}

func (r *SkillCacheRepository) Upsert(ctx context.Context, arg UpsertSkillParams) (AgentsSchemaSkillCache, error) {
	return New(r.pool).UpsertSkill(ctx, arg)
}

func (r *SkillCacheRepository) Search(ctx context.Context, embedding pgvector.Vector, limit int32) ([]AgentsSchemaSkillCacheRow, error) {
	return New(r.pool).SearchSkills(ctx, embedding, limit)
}

func (r *SkillCacheRepository) SearchByDomain(ctx context.Context, embedding pgvector.Vector, domain string, limit int32) ([]AgentsSchemaSkillCacheRow, error) {
	return New(r.pool).SearchSkillsByDomain(ctx, embedding, domain, limit)
}

func (r *SkillCacheRepository) GetBySeedHash(ctx context.Context, seedHash string) ([]AgentsSchemaSkillCacheRow, error) {
	return New(r.pool).GetSkillBySeedHash(ctx, seedHash)
}

func (r *SkillCacheRepository) GetByDomain(ctx context.Context, domain string) ([]AgentsSchemaSkillCacheRow, error) {
	return New(r.pool).GetSkillsByDomain(ctx, domain)
}

func (r *SkillCacheRepository) Delete(ctx context.Context, domain, name string) error {
	return New(r.pool).DeleteSkill(ctx, domain, name)
}
