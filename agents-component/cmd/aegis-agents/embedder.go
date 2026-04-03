// Package main — embedder.go implements a Voyage AI embedding client for
// production-quality semantic skill search.
//
// The voyageEmbedder implements both skills.Embedder (single text) and
// skills.BatchEmbedder (multi-text), allowing the Skill Hierarchy Manager to
// embed an entire domain's commands in a single HTTP round-trip rather than
// one call per command.
//
// Network calls are permitted in cmd/ binaries; this file MUST NOT be moved
// to internal/ where the no-outbound-calls rule applies.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

const (
	voyageAPIURL         = "https://api.voyageai.com/v1/embeddings"
	voyageDefaultModel   = "voyage-3-lite"
	voyageRequestTimeout = 15 * time.Second
)

// voyageEmbedder calls the Voyage AI embeddings API.
// It satisfies skills.Embedder (via Embed) and skills.BatchEmbedder (via EmbedBatch).
type voyageEmbedder struct {
	apiKey string
	model  string
	client *http.Client
}

// newVoyageEmbedder constructs a voyageEmbedder. model defaults to
// voyageDefaultModel when empty.
func newVoyageEmbedder(apiKey, model string) *voyageEmbedder {
	if model == "" {
		model = voyageDefaultModel
	}
	return &voyageEmbedder{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: voyageRequestTimeout},
	}
}

// voyageRequest is the JSON body sent to the Voyage AI embeddings endpoint.
type voyageRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// voyageResponse is the JSON body returned by the Voyage AI embeddings endpoint.
type voyageResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed returns the embedding vector for a single text. It delegates to
// EmbedBatch internally.
func (v *voyageEmbedder) Embed(text string) ([]float64, error) {
	vecs, err := v.EmbedBatch([]string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || vecs[0] == nil {
		return nil, fmt.Errorf("voyage: no embedding returned for input text")
	}
	return vecs[0], nil
}

// EmbedBatch returns embedding vectors for all texts in a single API call.
// The returned slice has the same length as texts; entries are ordered by the
// index field in the API response so the caller can correlate by position.
func (v *voyageEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(voyageRequest{
		Input: texts,
		Model: v.model,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, voyageAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("voyage: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voyage: unexpected HTTP status %d", resp.StatusCode)
	}

	var vr voyageResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return nil, fmt.Errorf("voyage: decode response: %w", err)
	}

	if len(vr.Data) != len(texts) {
		return nil, fmt.Errorf("voyage: expected %d embeddings, got %d", len(texts), len(vr.Data))
	}

	// Sort by index so results align with the input slice regardless of the
	// order in which the API returns them.
	sort.Slice(vr.Data, func(i, j int) bool {
		return vr.Data[i].Index < vr.Data[j].Index
	})

	result := make([][]float64, len(texts))
	for i, d := range vr.Data {
		result[i] = d.Embedding
	}
	return result, nil
}
