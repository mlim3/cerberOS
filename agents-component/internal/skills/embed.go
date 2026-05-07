// Package skills — embed.go implements a lightweight feature-hash embedder for
// the semantic search index (EDD §13.5). No external ML dependencies are required:
// tokens are hashed into a fixed-dimension float vector and L2-normalised so that
// cosine similarity reduces to a dot product.
package skills

import (
	"hash/fnv"
	"math"
	"strings"
)

const (
	embDim            = 128 // feature-hash vector dimension
	defaultSearchTopK = 3   // default topK for Manager.Search
)

// commandEntry is one indexed record in the in-memory embedding search index.
type commandEntry struct {
	domain      string
	name        string
	description string
	vector      []float64
}

// hashEmbed computes a feature-hash embedding for text.
// Tokens are space-separated words and character bigrams from the lowercased input.
// Each token is hashed with FNV64a into a bucket; a second hash determines the sign
// (sign-randomisation reduces variance). The resulting vector is L2-normalised so
// cosineSimilarity can be computed as a plain dot product.
func hashEmbed(text string) []float64 {
	vec := make([]float64, embDim)
	lower := strings.ToLower(text)

	for _, word := range strings.Fields(lower) {
		addToVec(vec, word)
	}

	runes := []rune(lower)
	for i := 0; i+1 < len(runes); i++ {
		addToVec(vec, string(runes[i:i+2]))
	}

	l2Normalise(vec)
	return vec
}

func addToVec(vec []float64, token string) {
	h1 := fnv.New64a()
	h1.Write([]byte(token))
	bucket := h1.Sum64() % uint64(len(vec))

	h2 := fnv.New64a()
	h2.Write([]byte("~" + token))
	if h2.Sum64()%2 == 0 {
		vec[bucket] += 1.0
	} else {
		vec[bucket] -= 1.0
	}
}

func l2Normalise(vec []float64) {
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
}

// cosineSimilarity returns the cosine similarity of two L2-normalised vectors.
// Because both inputs are unit vectors this is equivalent to their dot product.
func cosineSimilarity(a, b []float64) float64 {
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}
