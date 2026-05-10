package logic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mlim3/cerberOS/memory/internal/storage"
)

// SkillSearchResult is a single hit returned by SemanticSearchSkills.
type SkillSearchResult struct {
	Domain      string          `json:"domain"`
	Name        string          `json:"name"`
	Origin      string          `json:"origin"`
	Description string          `json:"description"`
	Payload     json.RawMessage `json:"payload"`
}

// SkillCacheProcessor coordinates embedding-on-write and pgvector search for
// the agents_schema.skill_cache table.
type SkillCacheProcessor struct {
	repo     *storage.SkillCacheRepository
	embedder Embedder
}

func NewSkillCacheProcessor(repo *storage.SkillCacheRepository, embedder Embedder) *SkillCacheProcessor {
	return &SkillCacheProcessor{repo: repo, embedder: embedder}
}

// UpsertSkillRequest is the write payload sent by the seed job or skill synthesis.
type UpsertSkillRequest struct {
	Domain      string          `json:"domain"`
	Name        string          `json:"name"`
	Origin      string          `json:"origin"`
	Description string          `json:"description"`
	Payload     json.RawMessage `json:"payload"`
	SeedHash    string          `json:"seed_hash"`
}

// Upsert embeds the description and writes the skill to the database.
// Conflicts on (domain, name) are resolved by overwriting all fields.
func (p *SkillCacheProcessor) Upsert(ctx context.Context, req UpsertSkillRequest) error {
	if req.Domain == "" || req.Name == "" {
		return fmt.Errorf("skill_cache: domain and name are required")
	}

	text := req.Name + ": " + req.Description
	embedding, err := p.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("skill_cache: embed description: %w", err)
	}

	payloadBytes := []byte(req.Payload)
	if len(payloadBytes) == 0 {
		payloadBytes = []byte("{}")
	}

	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("skill_cache: generate id: %w", err)
	}

	_, err = p.repo.Upsert(ctx, storage.UpsertSkillParams{
		ID:          pgtype.UUID{Bytes: id, Valid: true},
		Domain:      req.Domain,
		Name:        req.Name,
		Origin:      req.Origin,
		Description: req.Description,
		Payload:     payloadBytes,
		Embedding:   embedding,
		SeedHash:    req.SeedHash,
	})
	return err
}

// SemanticSearch embeds the query and returns the top-K most similar skills.
func (p *SkillCacheProcessor) SemanticSearch(ctx context.Context, query string, domain string, topK int) ([]SkillSearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("skill_cache: query must not be empty")
	}
	if topK <= 0 {
		topK = 3
	}

	embedding, err := p.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("skill_cache: embed query: %w", err)
	}

	var rows []storage.AgentsSchemaSkillCacheRow
	if domain != "" {
		rows, err = p.repo.SearchByDomain(ctx, embedding, domain, int32(topK))
	} else {
		rows, err = p.repo.Search(ctx, embedding, int32(topK))
	}
	if err != nil {
		return nil, fmt.Errorf("skill_cache: search: %w", err)
	}

	results := make([]SkillSearchResult, 0, len(rows))
	for _, r := range rows {
		results = append(results, SkillSearchResult{
			Domain:      r.Domain,
			Name:        r.Name,
			Origin:      r.Origin,
			Description: r.Description,
			Payload:     json.RawMessage(r.Payload),
		})
	}
	return results, nil
}

// IsSeedHashPresent returns true when any skill record with the given hash exists.
// Used by the Factory to skip re-seeding when the YAML hasn't changed.
func (p *SkillCacheProcessor) IsSeedHashPresent(ctx context.Context, hash string) (bool, error) {
	if hash == "" {
		return false, nil
	}
	rows, err := p.repo.GetBySeedHash(ctx, hash)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// ListByDomain returns all skills stored in the given domain, ordered by name.
func (p *SkillCacheProcessor) ListByDomain(ctx context.Context, domain string) ([]SkillSearchResult, error) {
	if domain == "" {
		return nil, fmt.Errorf("skill_cache: domain must not be empty")
	}
	rows, err := p.repo.GetByDomain(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("skill_cache: list by domain: %w", err)
	}
	results := make([]SkillSearchResult, 0, len(rows))
	for _, r := range rows {
		results = append(results, SkillSearchResult{
			Domain:      r.Domain,
			Name:        r.Name,
			Origin:      r.Origin,
			Description: r.Description,
			Payload:     json.RawMessage(r.Payload),
		})
	}
	return results, nil
}

// Delete removes a skill identified by domain and name. Returns nil if the
// skill did not exist (idempotent).
func (p *SkillCacheProcessor) Delete(ctx context.Context, domain, name string) error {
	if domain == "" || name == "" {
		return fmt.Errorf("skill_cache: domain and name are required")
	}
	return p.repo.Delete(ctx, domain, name)
}
