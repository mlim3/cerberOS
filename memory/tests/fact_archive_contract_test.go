package tests

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFactArchiveVisibilityContract_FutureBlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := generateSeededUserFixture(t)
	content := "Future archive test: I temporarily live in Seattle."

	var factID string

	t.Run("save_creates_active_fact", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/save", map[string]any{
			"content":      content,
			"sourceType":   "chat",
			"sourceId":     uuid.NewString(),
			"extractFacts": true,
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
	})

	t.Run("active_fact_is_visible_in_default_retrieval", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		factID = assertAndExtractFirstFactID(t, env.Data)
	})

	t.Run("manual_archive_hides_fact_from_default_retrieval", func(t *testing.T) {
		if strings.TrimSpace(factID) == "" {
			t.Fatalf("precondition failed: no fact id captured from active retrieval")
		}
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+factID+"/archive", map[string]any{
			"reason": "manually_archived",
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertJSONContainsStringField(t, env.Data, "archiveReason", "manually_archived")
	})

	t.Run("default_retrieval_excludes_archived_fact", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertFactsArrayDoesNotContainFactID(t, env.Data, factID)
	})

	t.Run("archive_aware_retrieval_includes_archived_fact", func(t *testing.T) {
		if strings.TrimSpace(factID) == "" {
			t.Fatalf("precondition failed: no fact id captured from active retrieval")
		}
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all?includeArchived=true", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)
		assertArchivedFactHasReason(t, env.Data, factID, "manually_archived")
	})

	t.Run("invalid_archive_reason_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+uuid.NewString()+"/archive", map[string]any{
			"reason": "totally_invalid_reason",
		}, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("invalid_include_archived_query_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all?includeArchived=definitely-not-bool", nil, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("archiving_already_archived_fact_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+factID+"/archive", map[string]any{
			"reason": "manually_archived",
		}, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})
}
