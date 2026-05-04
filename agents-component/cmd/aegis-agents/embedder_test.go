package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mockEmbeddingAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		type embEntry struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		}
		data := make([]embEntry, len(req.Input))
		for i := range req.Input {
			vec := []float64{float64(i + 1), float64(req.Dimensions), float64(len(req.Input[i]))}
			data[i] = embEntry{Embedding: vec, Index: i}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestEmbeddingAPIEmbedder_Embed(t *testing.T) {
	srv := mockEmbeddingAPIServer(t)
	defer srv.Close()

	e, err := newEmbeddingAPIEmbedder(srv.URL, "microsoft/harrier-oss-v1-270m", 640, "harrier")
	if err != nil {
		t.Fatalf("newEmbeddingAPIEmbedder: %v", err)
	}

	vec, err := e.Embed("fetch a web page")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim test vector, got %d", len(vec))
	}
	if vec[0] != 1 || vec[1] != 640 {
		t.Fatalf("unexpected vector contents: %v", vec)
	}
}

func TestEmbeddingAPIEmbedder_EmbedBatch(t *testing.T) {
	srv := mockEmbeddingAPIServer(t)
	defer srv.Close()

	e, err := newEmbeddingAPIEmbedder(srv.URL, "microsoft/harrier-oss-v1-270m", 640, "plain")
	if err != nil {
		t.Fatalf("newEmbeddingAPIEmbedder: %v", err)
	}

	vecs, err := e.EmbedBatch([]string{"text one", "text two", "text three"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, vec := range vecs {
		want := float64(i + 1)
		if vec[0] != want || vec[1] != 640 {
			t.Fatalf("vecs[%d]: unexpected contents %v", i, vec)
		}
	}
}

func TestEmbeddingAPIEmbedder_EmbedBatch_Empty(t *testing.T) {
	e, err := newEmbeddingAPIEmbedder("http://example.invalid", "microsoft/harrier-oss-v1-270m", 640, "plain")
	if err != nil {
		t.Fatalf("newEmbeddingAPIEmbedder: %v", err)
	}
	vecs, err := e.EmbedBatch(nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil): unexpected error: %v", err)
	}
	if vecs != nil {
		t.Fatalf("EmbedBatch(nil): expected nil, got %v", vecs)
	}
}

func TestEmbeddingAPIEmbedder_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	e, err := newEmbeddingAPIEmbedder(srv.URL, "microsoft/harrier-oss-v1-270m", 640, "plain")
	if err != nil {
		t.Fatalf("newEmbeddingAPIEmbedder: %v", err)
	}

	if _, err := e.Embed("anything"); err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}

func TestFormatters(t *testing.T) {
	if got := formatQueryText("embeddinggemma", "hello"); got != "task: search result | query: hello" {
		t.Fatalf("embeddinggemma query formatter mismatch: %q", got)
	}
	if got := formatDocumentText("embeddinggemma", "web.fetch fetches a page"); got != "title: skill command | text: web.fetch fetches a page" {
		t.Fatalf("embeddinggemma document formatter mismatch: %q", got)
	}
	if got := formatQueryText("harrier", "hello"); got != "Instruct: Retrieve semantically similar text\nQuery: hello" {
		t.Fatalf("harrier query formatter mismatch: %q", got)
	}
}
