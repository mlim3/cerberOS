package tests

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestChatAndIdempotency(t *testing.T) {
	sessionID := uuid.New().String()
	userID := uuid.New().String()
	idempotencyKey := uuid.New().String()
	seedUser(t, userID)

	t.Run("Happy Path - Save a message", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "user",
			"content":        "Hello, this is a test message.",
			"idempotencyKey": idempotencyKey,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), reqBody, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Expected status 201, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		data, ok := result["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("Missing data field in response")
		}

		msg, ok := data["message"].(map[string]interface{})
		if !ok {
			t.Fatalf("Missing message field in data")
		}

		if msg["content"] != reqBody["content"] {
			t.Errorf("Expected content %s, got %v", reqBody["content"], msg["content"])
		}

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), nil, nil)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", getResp.StatusCode)
		}

		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)

		getData, ok := getResult["data"].(map[string]interface{})
		if !ok {
			t.Fatalf("Missing data field in get response")
		}

		messages, ok := getData["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Fatalf("Expected messages array in get response")
		}
	})

	t.Run("Idempotency Check", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "user",
			"content":        "Hello, this is a test message.",
			"idempotencyKey": idempotencyKey,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), reqBody, nil)
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 201 or 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), nil, nil)
		var getResult map[string]interface{}
		parseResponse(t, getResp, &getResult)

		getData := getResult["data"].(map[string]interface{})
		messages := getData["messages"].([]interface{})

		if len(messages) != 1 {
			t.Errorf("Expected 1 message (idempotency should prevent duplicate), got %d", len(messages))
		}
	})

	t.Run("Idempotency Conflict On Different Payload", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "assistant",
			"content":        "Changed content",
			"idempotencyKey": idempotencyKey,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", sessionID), reqBody, nil)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("Expected status 409, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		errObj := result["error"].(map[string]interface{})
		if errObj["code"] != "conflict" {
			t.Fatalf("Expected conflict error code, got %v", errObj["code"])
		}
	})
}
