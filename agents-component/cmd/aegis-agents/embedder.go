// Package main — embedder.go implements the shared embedding-api client used
// for production skill search.
//
// Network calls are permitted in cmd/ binaries; this file MUST NOT be moved to
// internal/ where outbound calls are prohibited.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const embeddingRequestTimeout = 15 * time.Second

type embeddingAPIEmbedder struct {
	apiURL      string
	model       string
	dimensions  int
	promptStyle string
	client      *http.Client
}

type embeddingRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model,omitempty"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func newEmbeddingAPIEmbedder(apiURL, model string, dimensions int, promptStyle string) (*embeddingAPIEmbedder, error) {
	apiURL = strings.TrimSpace(apiURL)
	model = strings.TrimSpace(model)
	promptStyle = strings.TrimSpace(promptStyle)

	if apiURL == "" {
		return nil, fmt.Errorf("embedding API URL is required")
	}
	if model == "" {
		return nil, fmt.Errorf("embedding model is required")
	}
	if dimensions <= 0 {
		return nil, fmt.Errorf("embedding dimensions must be positive")
	}
	if promptStyle == "" {
		promptStyle = "plain"
	}

	return &embeddingAPIEmbedder{
		apiURL:      apiURL,
		model:       model,
		dimensions:  dimensions,
		promptStyle: promptStyle,
		client: &http.Client{
			Timeout: embeddingRequestTimeout,
		},
	}, nil
}

func (e *embeddingAPIEmbedder) Embed(text string) ([]float64, error) {
	vecs, err := e.embedTexts([]string{formatQueryText(e.promptStyle, text)})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || vecs[0] == nil {
		return nil, fmt.Errorf("embedding API returned no embedding for input text")
	}
	return vecs[0], nil
}

func (e *embeddingAPIEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	formatted := make([]string, len(texts))
	for i, text := range texts {
		formatted[i] = formatDocumentText(e.promptStyle, text)
	}
	return e.embedTexts(formatted)
}

func (e *embeddingAPIEmbedder) embedTexts(texts []string) ([][]float64, error) {
	reqBody, err := json.Marshal(embeddingRequest{
		Input:      texts,
		Model:      e.model,
		Dimensions: e.dimensions,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding request marshal failed: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, e.apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("embedding request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API returned HTTP %d", resp.StatusCode)
	}

	var payload embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("embedding response decode failed: %w", err)
	}
	if len(payload.Data) != len(texts) {
		return nil, fmt.Errorf("embedding API returned %d embeddings, expected %d", len(payload.Data), len(texts))
	}

	result := make([][]float64, len(texts))
	for _, datum := range payload.Data {
		if datum.Index < 0 || datum.Index >= len(texts) {
			return nil, fmt.Errorf("embedding API returned out-of-range index %d", datum.Index)
		}
		result[datum.Index] = datum.Embedding
	}
	for i, vec := range result {
		if len(vec) == 0 {
			return nil, fmt.Errorf("embedding API returned no embedding at index %d", i)
		}
	}
	return result, nil
}

func formatDocumentText(promptStyle, text string) string {
	switch promptStyle {
	case "embeddinggemma":
		return "title: skill command | text: " + text
	case "harrier":
		return text
	default:
		return text
	}
}

func formatQueryText(promptStyle, query string) string {
	switch promptStyle {
	case "embeddinggemma":
		return "task: search result | query: " + query
	case "harrier":
		return "Instruct: Retrieve semantically similar text\nQuery: " + query
	default:
		return query
	}
}
