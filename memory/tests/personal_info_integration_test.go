package tests

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mlim3/cerberOS/memory/internal/storage"
)

func TestPersonalInfoAndConcurrency(t *testing.T) {
	userID := uuid.New().String()
	seedUser(t, userID)

	t.Run("Semantic Search", func(t *testing.T) {
		chunks := []string{
			"My favorite color is blue.",
			"I live in San Francisco.",
			"I work as a software engineer.",
		}

		for _, chunk := range chunks {
			reqBody := map[string]interface{}{
				"content":      chunk,
				"sourceType":   "chat",
				"sourceId":     uuid.New().String(),
				"extractFacts": true,
			}
			resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/save", userID), reqBody, nil)
			if resp.StatusCode != http.StatusOK {
				var body []byte
				if resp.Body != nil {
					body = make([]byte, 1024)
					n, _ := resp.Body.Read(body)
					body = body[:n]
				}
				t.Fatalf("Failed to save chunk: expected 200, got %d, body: %s", resp.StatusCode, string(body))
			}
		}

		queryReq := map[string]interface{}{
			"query": "Where do I live?",
			"topK":  1,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/query", userID), queryReq, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to query chunks: expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		data := result["data"].(map[string]interface{})
		resultChunks := data["results"].([]interface{})

		if len(resultChunks) > 0 {
			firstChunk := resultChunks[0].(map[string]interface{})
			if _, ok := firstChunk["similarityScore"]; !ok {
				t.Log("Warning: similarityScore not present in response")
			}
		}
	})

	t.Run("Optimistic Concurrency (The Race)", func(t *testing.T) {
		saveReq := map[string]interface{}{
			"content":      "My phone number is 555-1234",
			"sourceType":   "chat",
			"sourceId":     uuid.New().String(),
			"extractFacts": true,
		}
		saveResp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/save", userID), saveReq, nil)
		if saveResp.StatusCode != http.StatusOK {
			var body []byte
			if saveResp.Body != nil {
				body = make([]byte, 1024)
				n, _ := saveResp.Body.Read(body)
				body = body[:n]
			}
			t.Fatalf("Failed to save fact: expected 200, got %d, body: %s", saveResp.StatusCode, string(body))
		}

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/personal_info/%s/all", userID), nil, nil)
		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)

		data := getResult["data"].(map[string]interface{})
		facts := data["facts"].([]interface{})
		if len(facts) == 0 {
			t.Fatalf("No facts found after save")
		}

		fact := facts[0].(map[string]interface{})
		factID := fact["factId"].(string)
		versionFloat, ok := fact["version"].(float64)
		if !ok {
			t.Fatalf("Version missing or not float64")
		}
		version1 := int32(versionFloat)

		updateReqA := map[string]interface{}{
			"category":   fact["category"].(string),
			"factKey":    fact["factKey"].(string),
			"factValue":  "555-9999",
			"confidence": 0.9,
			"version":    version1,
		}

		updateRespA := doRequest(t, "PUT", fmt.Sprintf("/api/v1/personal_info/%s/facts/%s", userID, factID), updateReqA, nil)
		if updateRespA.StatusCode != http.StatusOK {
			t.Fatalf("Agent A failed to update fact: expected 200, got %d", updateRespA.StatusCode)
		}

		updateReqB := map[string]interface{}{
			"category":   fact["category"].(string),
			"factKey":    fact["factKey"].(string),
			"factValue":  "555-0000",
			"confidence": 0.8,
			"version":    version1,
		}

		updateRespB := doRequest(t, "PUT", fmt.Sprintf("/api/v1/personal_info/%s/facts/%s", userID, factID), updateReqB, nil)
		if updateRespB.StatusCode != http.StatusConflict {
			t.Errorf("Expected 409 Conflict for stale update, got %d", updateRespB.StatusCode)
		}
	})

	t.Run("Delete Fact", func(t *testing.T) {
		saveReq := map[string]interface{}{
			"content":      "I prefer keyboard shortcuts.",
			"sourceType":   "chat",
			"sourceId":     uuid.New().String(),
			"extractFacts": true,
		}
		saveResp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/save", userID), saveReq, nil)
		if saveResp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to save fact for delete test: got %d", saveResp.StatusCode)
		}

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/personal_info/%s/all", userID), nil, nil)
		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)
		facts := getResult["data"].(map[string]interface{})["facts"].([]interface{})
		if len(facts) == 0 {
			t.Fatalf("No facts found for delete test")
		}
		factID := facts[len(facts)-1].(map[string]interface{})["factId"].(string)

		delResp := doRequest(t, "DELETE", fmt.Sprintf("/api/v1/personal_info/%s/facts/%s", userID, factID), nil, nil)
		if delResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200 on delete, got %d", delResp.StatusCode)
		}
		var delResult map[string]interface{}
		parseResponse(t, delResp, &delResult)
		data := delResult["data"].(map[string]interface{})
		if data["deleted"] != true {
			t.Fatalf("Expected deleted=true, got %v", data["deleted"])
		}
	})
}

func TestPersonalInfoRetrievalOrdering(t *testing.T) {
	userID := uuid.New().String()
	seedUser(t, userID)

	ctx := context.Background()
	var userUUID pgtype.UUID
	if err := userUUID.Scan(userID); err != nil {
		t.Fatalf("failed to scan user id: %v", err)
	}

	embedder := &deterministicTestEmbedder{}

	t.Run("More Relevant Chunk Ranks Ahead", func(t *testing.T) {
		queryText := "ranking exact match query"
		matchingEmbedding, err := embedder.Embed(ctx, queryText)
		if err != nil {
			t.Fatalf("failed to create matching embedding: %v", err)
		}
		otherEmbedding, err := embedder.Embed(ctx, "completely different query text")
		if err != nil {
			t.Fatalf("failed to create comparison embedding: %v", err)
		}

		matchID := uuid.Must(uuid.NewV7())
		otherID := uuid.Must(uuid.NewV7())

		if _, err := storage.New(dbPool).InsertChunk(ctx, storage.InsertChunkParams{
			ID:           pgtype.UUID{Bytes: matchID, Valid: true},
			UserID:       userUUID,
			RawText:      "matching chunk",
			Embedding:    matchingEmbedding,
			ModelVersion: "test-model",
		}); err != nil {
			t.Fatalf("failed to insert matching chunk: %v", err)
		}

		if _, err := storage.New(dbPool).InsertChunk(ctx, storage.InsertChunkParams{
			ID:           pgtype.UUID{Bytes: otherID, Valid: true},
			UserID:       userUUID,
			RawText:      "other chunk",
			Embedding:    otherEmbedding,
			ModelVersion: "test-model",
		}); err != nil {
			t.Fatalf("failed to insert other chunk: %v", err)
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/query", userID), map[string]interface{}{
			"query": queryText,
			"topK":  2,
		}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		results := result["data"].(map[string]interface{})["results"].([]interface{})
		if len(results) < 2 {
			t.Fatalf("expected at least 2 results, got %d", len(results))
		}

		first := results[0].(map[string]interface{})
		if first["text"] != "matching chunk" {
			t.Fatalf("expected matching chunk to rank first, got %v", first["text"])
		}
	})

	t.Run("Tie Break Uses Most Recent Chunk", func(t *testing.T) {
		queryText := "tie break ranking query"
		tiedEmbedding, err := embedder.Embed(ctx, queryText)
		if err != nil {
			t.Fatalf("failed to create tied embedding: %v", err)
		}

		olderID := uuid.Must(uuid.NewV7())
		newerID := uuid.Must(uuid.NewV7())

		if _, err := storage.New(dbPool).InsertChunk(ctx, storage.InsertChunkParams{
			ID:           pgtype.UUID{Bytes: olderID, Valid: true},
			UserID:       userUUID,
			RawText:      "older tied chunk",
			Embedding:    tiedEmbedding,
			ModelVersion: "test-model",
		}); err != nil {
			t.Fatalf("failed to insert older tied chunk: %v", err)
		}

		if _, err := storage.New(dbPool).InsertChunk(ctx, storage.InsertChunkParams{
			ID:           pgtype.UUID{Bytes: newerID, Valid: true},
			UserID:       userUUID,
			RawText:      "newer tied chunk",
			Embedding:    tiedEmbedding,
			ModelVersion: "test-model",
		}); err != nil {
			t.Fatalf("failed to insert newer tied chunk: %v", err)
		}

		if _, err := dbPool.Exec(ctx,
			`UPDATE personal_info_schema.personal_info_chunks SET created_at = $1 WHERE id = $2`,
			time.Now().Add(-2*time.Hour),
			pgtype.UUID{Bytes: olderID, Valid: true},
		); err != nil {
			t.Fatalf("failed to update older chunk timestamp: %v", err)
		}
		if _, err := dbPool.Exec(ctx,
			`UPDATE personal_info_schema.personal_info_chunks SET created_at = $1 WHERE id = $2`,
			time.Now().Add(-1*time.Hour),
			pgtype.UUID{Bytes: newerID, Valid: true},
		); err != nil {
			t.Fatalf("failed to update newer chunk timestamp: %v", err)
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/personal_info/%s/query", userID), map[string]interface{}{
			"query": queryText,
			"topK":  2,
		}, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		results := result["data"].(map[string]interface{})["results"].([]interface{})
		if len(results) < 2 {
			t.Fatalf("expected at least 2 results, got %d", len(results))
		}

		first := results[0].(map[string]interface{})
		second := results[1].(map[string]interface{})
		if first["text"] != "newer tied chunk" {
			t.Fatalf("expected newer tied chunk first, got %v", first["text"])
		}
		if second["text"] != "older tied chunk" {
			t.Fatalf("expected older tied chunk second, got %v", second["text"])
		}
	})
}
