package logic

import (
	"context"
	"hash/fnv"

	"github.com/pgvector/pgvector-go"
)

// LocalEmbedder generates deterministic local vectors without external APIs.
// This is intended for local development and demo usage.
type LocalEmbedder struct{}

func (l *LocalEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum64()

	v := make([]float32, 1536)
	for i := range v {
		v[i] = float32((seed+uint64(i*97))%1000) / 1000.0
	}
	return pgvector.NewVector(v), nil
}
