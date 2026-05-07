package tests

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestChatAndIdempotency(t *testing.T) {
	conversationID := uuid.New().String()
	userID := uuid.New().String()
	otherUserID := uuid.New().String()
	idempotencyKey := uuid.New().String()
	seedUser(t, userID)
	seedUser(t, otherUserID)

	t.Run("Happy Path - Save a message", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"role":           "user",
			"content":        "Hello, this is a test message.",
			"idempotencyKey": idempotencyKey,
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", conversationID), reqBody, nil)
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

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages?userId=%s", conversationID, userID), nil, nil)
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

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", conversationID), reqBody, nil)
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 201 or 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages?userId=%s", conversationID, userID), nil, nil)
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

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/chat/%s/messages", conversationID), reqBody, nil)
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

	t.Run("Read Requires Ownership", func(t *testing.T) {
		resp := doRequest(t, "GET", fmt.Sprintf("/api/v1/chat/%s/messages?userId=%s", conversationID, otherUserID), nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("Expected status 404, got %d", resp.StatusCode)
		}
	})

	t.Run("List Conversations For User", func(t *testing.T) {
		resp := doRequest(t, "GET", fmt.Sprintf("/api/v1/conversations?userId=%s", userID), nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		data := result["data"].(map[string]interface{})
		conversations, ok := data["conversations"].([]interface{})
		if !ok || len(conversations) == 0 {
			t.Fatalf("Expected conversations array in response")
		}
		first := conversations[0].(map[string]interface{})
		if first["conversationId"] != conversationID {
			t.Fatalf("Expected conversationId %s, got %v", conversationID, first["conversationId"])
		}
	})

	t.Run("Create Task For Conversation", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"userId":         userID,
			"conversationId": conversationID,
			"title":          "Follow up on the prior request",
			"inputSummary":   "Follow up on the prior request",
			"status":         "awaiting_feedback",
		}
		resp := doRequest(t, "POST", "/api/v1/tasks", reqBody, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Expected status 201, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		data := result["data"].(map[string]interface{})
		task := data["task"].(map[string]interface{})
		taskID, ok := task["taskId"].(string)
		if !ok || taskID == "" {
			t.Fatalf("Expected taskId in response")
		}

		getResp := doRequest(t, "GET", fmt.Sprintf("/api/v1/tasks/%s?userId=%s", taskID, userID), nil, nil)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", getResp.StatusCode)
		}
	})

	t.Run("List Conversations Returns Updated Title", func(t *testing.T) {
		resp := doRequest(t, "GET", fmt.Sprintf("/api/v1/conversations?userId=%s", userID), nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)

		data := result["data"].(map[string]interface{})
		conversations, ok := data["conversations"].([]interface{})
		if !ok || len(conversations) == 0 {
			t.Fatalf("Expected conversations array in response")
		}

		first := conversations[0].(map[string]interface{})
		if first["title"] != "Follow up on the prior request" {
			t.Fatalf("Expected persisted title, got %v", first["title"])
		}
	})
}
