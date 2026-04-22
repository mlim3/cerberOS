package tests

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChatOwnership_BlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	ownerID := validUserFixture(t)
	otherUserID := generateSeededUserFixture(t)
	conversationID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), "ownership-conversation")
	conversationID = "aaaaaaaa-aaaa-4aaa-8aaa-" + fmt.Sprintf("%012d", time.Now().UnixNano()%1_000_000_000_000)

	t.Run("owner_can_write_to_new_session", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/chat/"+conversationID+"/messages", map[string]any{
			"userId":  ownerID,
			"role":    "user",
			"content": "Owner message",
		}, nil)

		if status != http.StatusCreated {
			t.Fatalf("status = %d, want %d", status, http.StatusCreated)
		}
		assertSuccessEnvelope(t, env)
	})

	t.Run("different_user_cannot_write_to_owned_session", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/chat/"+conversationID+"/messages", map[string]any{
			"userId":  otherUserID,
			"role":    "user",
			"content": "Intruding message",
		}, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("owner_can_read_owned_conversation", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/chat/"+conversationID+"/messages?userId="+ownerID, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
	})

	t.Run("different_user_cannot_read_owned_conversation", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/chat/"+conversationID+"/messages?userId="+otherUserID, nil, nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("owner_can_list_conversations", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/conversations?userId="+ownerID, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
	})
}
