package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockVoyageServer starts an httptest server that mimics the Voyage AI
// embeddings endpoint. It returns fixed 4-dim vectors (index+1 repeated).
func mockVoyageServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}

		var req voyageRequest
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
			vec := []float64{float64(i + 1), float64(i + 1), float64(i + 1), float64(i + 1)}
			data[i] = embEntry{Embedding: vec, Index: i}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	}))
}

func TestVoyageEmbedder_Embed(t *testing.T) {
	srv := mockVoyageServer(t)
	defer srv.Close()

	e := newVoyageEmbedder("test-key", "voyage-3-lite")
	e.client.Transport = localTransport(srv.URL)

	vec, err := e.Embed("fetch a web page")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 4 {
		t.Errorf("expected 4-dim vector, got %d", len(vec))
	}
	// First (and only) input → index 0 → all elements are 1.0.
	for _, v := range vec {
		if v != 1.0 {
			t.Errorf("expected vector element 1.0, got %f", v)
		}
	}
}

func TestVoyageEmbedder_EmbedBatch(t *testing.T) {
	srv := mockVoyageServer(t)
	defer srv.Close()

	e := newVoyageEmbedder("test-key", "voyage-3-lite")
	e.client.Transport = localTransport(srv.URL)

	texts := []string{"text one", "text two", "text three"}
	vecs, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	// Verify ordering: index 0 → 1.0, index 1 → 2.0, index 2 → 3.0.
	for i, vec := range vecs {
		want := float64(i + 1)
		for _, v := range vec {
			if v != want {
				t.Errorf("vecs[%d]: expected element %f, got %f", i, want, v)
			}
		}
	}
}

func TestVoyageEmbedder_EmbedBatch_Empty(t *testing.T) {
	e := newVoyageEmbedder("test-key", "")
	vecs, err := e.EmbedBatch(nil)
	if err != nil {
		t.Fatalf("EmbedBatch(nil): unexpected error: %v", err)
	}
	if vecs != nil {
		t.Errorf("EmbedBatch(nil): expected nil, got %v", vecs)
	}
}

func TestVoyageEmbedder_DefaultModel(t *testing.T) {
	e := newVoyageEmbedder("key", "")
	if e.model != voyageDefaultModel {
		t.Errorf("default model: want %q, got %q", voyageDefaultModel, e.model)
	}
}

func TestVoyageEmbedder_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := newVoyageEmbedder("test-key", "")
	e.client.Transport = localTransport(srv.URL)

	if _, err := e.Embed("anything"); err == nil {
		t.Error("expected error on HTTP 500, got nil")
	}
}

// localTransport returns an http.RoundTripper that rewrites all requests to
// point at the given base URL, allowing voyageEmbedder to be tested against a
// local httptest server without modifying the production URL constant.
func localTransport(baseURL string) http.RoundTripper {
	return &rewriteTransport{base: baseURL}
}

type rewriteTransport struct {
	base string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the host/scheme with the test server's address.
	req2 := req.Clone(req.Context())
	req2.URL.Host = req.URL.Host
	// Parse the base URL and steal its host.
	parsed, err := http.NewRequest(http.MethodGet, rt.base, nil)
	if err != nil {
		return nil, err
	}
	req2.URL.Scheme = parsed.URL.Scheme
	req2.URL.Host = parsed.URL.Host
	return http.DefaultTransport.RoundTrip(req2)
}
