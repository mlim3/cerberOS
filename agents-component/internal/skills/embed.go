// Package skills — embed.go provides embedding utilities used by the in-memory
// skill search manager (EDD §13.5).
package skills

import (
	"fmt"
	"math"
	"strings"
	"unicode"
)

// Embedder converts a text string into a float64 embedding vector.
// Implementations must be safe for concurrent use.
// The returned vector should be L2-normalised so cosine similarity is
// equivalent to a dot product — the Manager assumes this property.
type Embedder interface {
	Embed(text string) ([]float64, error)
}

// tokenizeText lowercases text, splits on non-alphanumeric/underscore boundaries,
// drops tokens shorter than 2 characters and common English stopwords, then
// appends adjacent bigrams. Bigrams improve precision for technical phrase matching
// (e.g. "web_fetch" stays linked to "web" and "fetch").
func tokenizeText(text string) []string {
	text = strings.ToLower(text)
	var unigrams []string
	var cur strings.Builder

	flush := func() {
		if cur.Len() >= 2 {
			tok := cur.String()
			if !isStopword(tok) {
				unigrams = append(unigrams, tok)
			}
		}
		cur.Reset()
	}

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	tokens := make([]string, 0, len(unigrams)*2)
	tokens = append(tokens, unigrams...)
	for i := 0; i < len(unigrams)-1; i++ {
		tokens = append(tokens, unigrams[i]+"_"+unigrams[i+1])
	}
	return tokens
}

// isStopword returns true for common English words that carry little semantic
// signal in technical tool descriptions.
func isStopword(w string) bool {
	switch w {
	case "the", "an", "is", "are", "be", "to", "for", "of", "in", "on",
		"and", "or", "not", "do", "use", "this", "that", "it", "its",
		"with", "from", "by", "at", "as", "if", "only", "when", "any",
		"all", "no", "so", "but", "also", "via", "per", "can":
		return true
	}
	return false
}

// fnv1aHash computes a 32-bit FNV-1a hash of s.
func fnv1aHash(s string) uint32 {
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// l2Normalize divides each element of vec by the L2 norm in-place.
// No-ops on zero vectors to avoid division by zero.
func l2Normalize(vec []float64) {
	sum := 0.0
	for _, v := range vec {
		sum += v * v
	}
	if sum == 0 {
		return
	}
	norm := math.Sqrt(sum)
	for i := range vec {
		vec[i] /= norm
	}
}

// BatchEmbedder is an optional extension of Embedder that enables efficient
// multi-text embedding in a single API call (e.g. a shared embedding-api
// request). Implementations must be safe for concurrent use.
//
// Callers detect support via type assertion:
//
//	if be, ok := embedder.(skills.BatchEmbedder); ok { ... }
type BatchEmbedder interface {
	Embedder
	EmbedBatch(texts []string) ([][]float64, error)
}

// embedTexts embeds all texts in a single call when the configured embedder
// implements BatchEmbedder, and falls back to sequential Embed calls otherwise.
// When no embedder is configured it returns nil vectors without error so
// structural manager operations can still work in lightweight tests.
func (m *hierarchyManager) embedTexts(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if m.embedder == nil {
		return nil, nil
	}
	if be, ok := m.embedder.(BatchEmbedder); ok {
		vecs, err := be.EmbedBatch(texts)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(texts) {
			return nil, fmt.Errorf("skills: batch embed returned %d vectors for %d texts", len(vecs), len(texts))
		}
		for i, vec := range vecs {
			if vec == nil {
				return nil, fmt.Errorf("skills: batch embed returned nil vector at index %d", i)
			}
		}
		return vecs, nil
	}
	result := make([][]float64, len(texts))
	for i, t := range texts {
		vec, err := m.embedder.Embed(t)
		if err != nil {
			return nil, err
		}
		if vec == nil {
			return nil, fmt.Errorf("skills: embed returned nil vector for text index %d", i)
		}
		result[i] = vec
	}
	return result, nil
}

// cosineSimilarity computes the dot product of two L2-normalised vectors.
// Returns 0 when the vectors have different lengths.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	dot := 0.0
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}
