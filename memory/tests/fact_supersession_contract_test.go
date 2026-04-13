package tests

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFactSupersessionContract_FutureBlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := generateSeededUserFixture(t)
	originalContent := "My employer is Acme Corp."

	var oldFactID string
	var newFactID string

	t.Run("create_original_fact", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/save", map[string]any{
			"content":      originalContent,
			"sourceType":   "chat",
			"sourceId":     uuid.NewString(),
			"extractFacts": true,
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
	})

	t.Run("find_original_fact_in_active_retrieval", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		oldFactID = assertAndExtractFirstFactID(t, env.Data)
	})

	t.Run("supersede_moves_old_fact_out_of_active_state", func(t *testing.T) {
		if strings.TrimSpace(oldFactID) == "" {
			t.Fatalf("precondition failed: no original fact id captured from active retrieval")
		}
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+oldFactID+"/supersede", map[string]any{
			"category":   "profile",
			"factKey":    "employer",
			"factValue":  "Globex Corp",
			"confidence": 0.95,
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		newFactID = assertAndExtractNonEmptyStringField(t, env.Data, "newFactId")
		assertJSONContainsStringField(t, env.Data, "archiveReason", "superseded")
	})

	t.Run("default_retrieval_only_returns_new_active_fact", func(t *testing.T) {
		if strings.TrimSpace(oldFactID) == "" || strings.TrimSpace(newFactID) == "" {
			t.Fatalf("precondition failed: expected old and new fact ids to be populated")
		}
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertFactsArrayDoesNotContainFactID(t, env.Data, oldFactID)
		assertFactsArrayContainsFactID(t, env.Data, newFactID)
	})

	t.Run("archive_aware_retrieval_shows_superseded_relationship", func(t *testing.T) {
		if strings.TrimSpace(oldFactID) == "" || strings.TrimSpace(newFactID) == "" {
			t.Fatalf("precondition failed: expected old and new fact ids to be populated")
		}
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all?includeArchived=true", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertArchivedFactSupersededBy(t, env.Data, oldFactID, newFactID)
	})
}
