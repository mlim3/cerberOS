// Package skills — embed.go provides the Embedder interface and a deterministic
// local fallback used by the in-memory skill search manager (EDD §13.5).
//
// The default hashEmbedder maps text to a fixed-dimension float64 vector using
// FNV-1a feature hashing on unigrams and bigrams. It requires no API calls,
// no training data, and no corpus statistics — making it safe to use inside
// internal/ packages that are prohibited from making network connections.
//
// Production callers inject the shared embedding-api client via WithEmbedder.
// That client lives outside internal/ where network calls are permitted.
package skills

import (
	"math"
	"strings"
	"unicode"
)

const (
	// defaultHashDim is the vector dimension used by the deterministic local embedder.
	// 512 gives a good balance of precision and memory for small skill corpora.
	defaultHashDim = 512
)

// Embedder converts a text string into a float64 embedding vector.
// Implementations must be safe for concurrent use.
// The returned vector should be L2-normalised so cosine similarity is
// equivalent to a dot product — the Manager assumes this property.
type Embedder interface {
	Embed(text string) ([]float64, error)
}

// hashEmbedder is the default Embedder. It uses FNV-1a feature hashing on
// unigrams and bigrams extracted from the input text. The result is L2-normalised.
//
// Properties:
//   - Fixed output dimension (no vocabulary needed).
//   - Deterministic and stateless — identical inputs always produce identical vectors.
//   - O(tokens) time, O(dim) space.
//   - No network calls, no external dependencies.
type hashEmbedder struct {
	dim int
}

// newHashEmbedder returns a hashEmbedder with the given vector dimension.
func newHashEmbedder(dim int) *hashEmbedder {
	return &hashEmbedder{dim: dim}
}

// Embed converts text to a normalised float64 vector via feature hashing.
func (h *hashEmbedder) Embed(text string) ([]float64, error) {
	tokens := tokenizeText(text)
	vec := make([]float64, h.dim)
	for _, tok := range tokens {
		idx := int(fnv1aHash(tok)) % h.dim
		if idx < 0 {
			idx = -idx
		}
		vec[idx]++
	}
	l2Normalize(vec)
	return vec, nil
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
// The returned slice is always the same length as texts; entries where
// embedding failed are nil (non-fatal — those commands are excluded from search
// results but structural queries still work).
func (m *hierarchyManager) embedTexts(texts []string) [][]float64 {
	if len(texts) == 0 {
		return nil
	}
	if be, ok := m.embedder.(BatchEmbedder); ok {
		vecs, err := be.EmbedBatch(texts)
		if err == nil && len(vecs) == len(texts) {
			return vecs
		}
		// Fall through to one-at-a-time on error or unexpected length.
	}
	result := make([][]float64, len(texts))
	for i, t := range texts {
		if vec, err := m.embedder.Embed(t); err == nil {
			result[i] = vec
		}
	}
	return result
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
