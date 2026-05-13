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
	// Exclude synthesized (user-created) skills from global search. Synthesized
	// skills are already pre-loaded into the creating user's spawn context via the
	// SynthesizedSkills field — returning them here would expose one user's skills
	// to other users' search sessions and cause spurious embedding matches (the
	// skill name is embedded alongside the description, so "e2e_url_summarizer"
	// scores unexpectedly high for queries like "e2e connectivity probe").
	// Only static skills (origin = 'static' or '') are globally discoverable.
	const q = `
SELECT id, domain, name, origin, description, payload, seed_hash, created_at, updated_at
FROM agents_schema.skill_cache
WHERE origin != 'synthesized'
ORDER BY embedding <=> $1
LIMIT $2`
	rows, err := r.pool.Query(ctx, q, embedding, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AgentsSchemaSkillCacheRow
	for rows.Next() {
		var i AgentsSchemaSkillCacheRow
		if err := rows.Scan(&i.ID, &i.Domain, &i.Name, &i.Origin, &i.Description,
			&i.Payload, &i.SeedHash, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func (r *SkillCacheRepository) SearchByDomain(ctx context.Context, embedding pgvector.Vector, domain string, limit int32) ([]AgentsSchemaSkillCacheRow, error) {
	// Same synthesized-skill exclusion as Search — see comment there.
	const q = `
SELECT id, domain, name, origin, description, payload, seed_hash, created_at, updated_at
FROM agents_schema.skill_cache
WHERE origin != 'synthesized' AND domain = $2
ORDER BY embedding <=> $1
LIMIT $3`
	rows, err := r.pool.Query(ctx, q, embedding, domain, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AgentsSchemaSkillCacheRow
	for rows.Next() {
		var i AgentsSchemaSkillCacheRow
		if err := rows.Scan(&i.ID, &i.Domain, &i.Name, &i.Origin, &i.Description,
			&i.Payload, &i.SeedHash, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
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
