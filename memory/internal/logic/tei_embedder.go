package logic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"
)

const teiRequestTimeout = 15 * time.Second

type teiEmbedder struct {
	apiURL       string
	modelVersion string
	dimensions   int
	client       *http.Client
}

type teiEmbeddingRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model,omitempty"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type teiEmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func NewTEIEmbedder(apiURL, model string, dimensions int) (Embedder, error) {
	apiURL = strings.TrimSpace(apiURL)
	model = strings.TrimSpace(model)
	if apiURL == "" {
		return nil, fmt.Errorf("embedding API URL is required")
	}
	if model == "" {
		return nil, fmt.Errorf("embedding model is required")
	}
	if dimensions <= 0 {
		return nil, fmt.Errorf("embedding dimensions must be positive")
	}

	return &teiEmbedder{
		apiURL:       apiURL,
		modelVersion: model,
		dimensions:   dimensions,
		client: &http.Client{
			Timeout: teiRequestTimeout,
		},
	}, nil
}

func (t *teiEmbedder) ModelVersion() string {
	return t.modelVersion
}

func (t *teiEmbedder) Embed(ctx context.Context, text string) (pgvector.Vector, error) {
	reqBody, err := json.Marshal(teiEmbeddingRequest{
		Input:      []string{text},
		Model:      t.modelVersion,
		Dimensions: t.dimensions,
	})
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("embedding request marshal failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("embedding request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return pgvector.Vector{}, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return pgvector.Vector{}, fmt.Errorf("embedding API returned HTTP %d", resp.StatusCode)
	}

	var payload teiEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return pgvector.Vector{}, fmt.Errorf("embedding response decode failed: %w", err)
	}
	if len(payload.Data) == 0 || len(payload.Data[0].Embedding) == 0 {
		return pgvector.Vector{}, fmt.Errorf("embedding API returned no embedding")
	}

	return pgvector.NewVector(payload.Data[0].Embedding), nil
}
