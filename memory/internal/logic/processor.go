package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
	"github.com/sashabaranov/go-openai"

	"github.com/mlim3/cerberOS/memory/internal/storage"
)

// Embedder represents a service that can generate vector embeddings
type Embedder interface {
	Embed(ctx context.Context, text string) (pgvector.Vector, error)
}

// MockEmbedder implements a fake Embedder returning random 1536-dim vectors
type MockEmbedder struct{}

func (m *MockEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	v := make([]float32, 1536)
	for i := range v {
		v[i] = rand.Float32()
	}
	return pgvector.NewVector(v), nil
}

// OpenAIEmbedder implements Embedder using the OpenAI API
type OpenAIEmbedder struct {
	client *openai.Client
}

func NewOpenAIEmbedder(apiKey string) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		client: openai.NewClient(apiKey),
	}
}

func (o *OpenAIEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	req := openai.EmbeddingRequest{
		Input: []string{text},
		Model: openai.SmallEmbedding3,
	}

	resp, err := o.client.CreateEmbeddings(ctx, req)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("failed to create embeddings: %w", err)
	}

	if len(resp.Data) == 0 {
		return pgvector.Vector{}, fmt.Errorf("no embeddings returned")
	}

	return pgvector.NewVector(resp.Data[0].Embedding), nil
}

// Processor coordinates the logic for personal info storage and retrieval
type Processor struct {
	repo     storage.Repository
	embedder Embedder
}

func NewProcessor(repo storage.Repository, embedder Embedder) *Processor {
	return &Processor{
		repo:     repo,
		embedder: embedder,
	}
}

// simpleChunker splits text into chunks of roughly 500 chars with 50 char overlap
func simpleChunker(text string) []string {
	if text == "" {
		return nil
	}

	const chunkSize = 500
	const overlap = 50

	var chunks []string
	runes := []rune(text)

	for i := 0; i < len(runes); {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}

		chunks = append(chunks, string(runes[i:end]))

		if end == len(runes) {
			break
		}

		i += chunkSize - overlap
	}

	return chunks
}

type SaveRequest struct {
	UserID       string
	Content      string
	SourceType   string
	SourceID     string
	ExtractFacts bool
}

type SaveResponse struct {
	ChunkIDs           []string
	FactIDs            []string
	SourceReferenceIDs []string
}

// SavePersonalInfo processes and saves new information
func (p *Processor) SavePersonalInfo(ctx context.Context, req SaveRequest) (*SaveResponse, error) {
	var userUUID, sourceUUID pgtype.UUID
	if err := userUUID.Scan(req.UserID); err != nil {
		return nil, err
	}
	if err := sourceUUID.Scan(req.SourceID); err != nil {
		return nil, err
	}

	chunks := simpleChunker(req.Content)
	resp := &SaveResponse{
		ChunkIDs:           make([]string, 0),
		FactIDs:            make([]string, 0),
		SourceReferenceIDs: make([]string, 0),
	}

	err := p.repo.WithTx(ctx, func(q *storage.Queries) error {
		// 1. Process Chunks
		for _, text := range chunks {
			embedText := fmt.Sprintf("Type: %s | Content: %s", req.SourceType, text)
			embedding, err := p.embedder.Embed(ctx, embedText)
			if err != nil {
				return err
			}

			var chunkID pgtype.UUID
			newChunkID, _ := uuid.NewV7()
			chunkID.Scan(newChunkID.String())

			chunk, err := q.InsertChunk(ctx, storage.InsertChunkParams{
				ID:           chunkID,
				UserID:       userUUID,
				RawText:      text,
				Embedding:    embedding,
				ModelVersion: "text-embedding-3-small",
			})
			if err != nil {
				return err
			}

			// Format pgtype.UUID back to string
			cid := formatUUID(chunk.ID)
			resp.ChunkIDs = append(resp.ChunkIDs, cid)

			// Source Reference for Chunk
			var refID pgtype.UUID
			newRefID, _ := uuid.NewV7()
			refID.Scan(newRefID.String())

			ref, err := q.CreateSourceReference(ctx, storage.CreateSourceReferenceParams{
				ID:         refID,
				UserID:     userUUID,
				TargetID:   chunk.ID,
				TargetType: "chunk",
				SourceID:   sourceUUID,
				SourceType: req.SourceType,
			})
			if err != nil {
				return err
			}
			resp.SourceReferenceIDs = append(resp.SourceReferenceIDs, formatUUID(ref.ID))
		}

		// 2. Process Facts if requested
		if req.ExtractFacts {
			// Mock fact extraction
			var factID pgtype.UUID
			newFactID, _ := uuid.NewV7()
			factID.Scan(newFactID.String())

			factVal, _ := json.Marshal(map[string]string{"extracted_from": "mock"})

			var cat pgtype.Text
			cat.Scan("General")

			fact, err := q.UpsertFact(ctx, storage.UpsertFactParams{
				ID:         factID,
				UserID:     userUUID,
				Category:   cat,
				FactKey:    "auto_extracted_" + uuid.NewString()[:8], // Make it somewhat unique
				FactValue:  factVal,
				Confidence: pgtype.Float8{Float64: 0.9, Valid: true},
				Version:    pgtype.Int4{Int32: 1, Valid: true},
			})
			if err != nil {
				return err
			}
			resp.FactIDs = append(resp.FactIDs, formatUUID(fact.ID))

			// Source Reference for Fact
			var refID pgtype.UUID
			newFactRefID, _ := uuid.NewV7()
			refID.Scan(newFactRefID.String())

			ref, err := q.CreateSourceReference(ctx, storage.CreateSourceReferenceParams{
				ID:         refID,
				UserID:     userUUID,
				TargetID:   fact.ID,
				TargetType: "fact",
				SourceID:   sourceUUID,
				SourceType: req.SourceType,
			})
			if err != nil {
				return err
			}
			resp.SourceReferenceIDs = append(resp.SourceReferenceIDs, formatUUID(ref.ID))
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return resp, nil
}

type QueryResult struct {
	ChunkID          string
	Text             string
	SimilarityScore  float64
	SourceReferences []storage.PersonalInfoSchemaSourceReference
}

func (p *Processor) SemanticQuery(ctx context.Context, userID, query string, topK int) ([]QueryResult, error) {
	var userUUID pgtype.UUID
	if err := userUUID.Scan(userID); err != nil {
		return nil, err
	}

	// 1. Embed Query
	embedding, err := p.embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	// 2. Search (ordered by distance ASC, then created_at DESC)
	chunks, err := p.repo.QueryChunksBySimilarity(ctx, userUUID, embedding, int32(topK))
	if err != nil {
		return nil, err
	}

	// 3. Populate Results
	var results []QueryResult
	for _, c := range chunks {
		sim := 1.0 - c.Distance
		if sim < 0 {
			sim = 0
		}
		if sim > 1 {
			sim = 1
		}

		refs, err := p.repo.Querier().GetSourceReferencesByTarget(ctx, storage.GetSourceReferencesByTargetParams{
			UserID:   userUUID,
			TargetID: c.Chunk.ID,
		})
		if err != nil {
			return nil, err
		}

		results = append(results, QueryResult{
			ChunkID:          formatUUID(c.Chunk.ID),
			Text:             c.Chunk.RawText,
			SimilarityScore:  sim,
			SourceReferences: refs,
		})
	}

	return results, nil
}

func formatUUID(u pgtype.UUID) string {
	b := u.Bytes
	// 8-4-4-4-12
	return string([]byte{
		hexChar(b[0] >> 4), hexChar(b[0] & 0x0f),
		hexChar(b[1] >> 4), hexChar(b[1] & 0x0f),
		hexChar(b[2] >> 4), hexChar(b[2] & 0x0f),
		hexChar(b[3] >> 4), hexChar(b[3] & 0x0f),
		'-',
		hexChar(b[4] >> 4), hexChar(b[4] & 0x0f),
		hexChar(b[5] >> 4), hexChar(b[5] & 0x0f),
		'-',
		hexChar(b[6] >> 4), hexChar(b[6] & 0x0f),
		hexChar(b[7] >> 4), hexChar(b[7] & 0x0f),
		'-',
		hexChar(b[8] >> 4), hexChar(b[8] & 0x0f),
		hexChar(b[9] >> 4), hexChar(b[9] & 0x0f),
		'-',
		hexChar(b[10] >> 4), hexChar(b[10] & 0x0f),
		hexChar(b[11] >> 4), hexChar(b[11] & 0x0f),
		hexChar(b[12] >> 4), hexChar(b[12] & 0x0f),
		hexChar(b[13] >> 4), hexChar(b[13] & 0x0f),
		hexChar(b[14] >> 4), hexChar(b[14] & 0x0f),
		hexChar(b[15] >> 4), hexChar(b[15] & 0x0f),
	})
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}
