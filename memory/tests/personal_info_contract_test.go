package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPersonalInfoUserValidation_BlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")

	t.Run("save_with_malformed_user_returns_invalid_argument", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/not-a-uuid/save", map[string]any{
			"content":      "test content",
			"sourceType":   "chat",
			"sourceId":     uuid.NewString(),
			"extractFacts": false,
		}, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("save_with_unknown_user_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+unknownUserID()+"/save", map[string]any{
			"content":      "test content",
			"sourceType":   "chat",
			"sourceId":     uuid.NewString(),
			"extractFacts": false,
		}, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("get_all_with_malformed_user_returns_invalid_argument", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/not-a-uuid/all", nil, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("get_all_with_unknown_user_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+unknownUserID()+"/all", nil, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("query_with_malformed_user_returns_invalid_argument", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/not-a-uuid/query", map[string]any{
			"query": "test query",
			"topK":  3,
		}, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("query_with_unknown_user_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+unknownUserID()+"/query", map[string]any{
			"query": "test query",
			"topK":  3,
		}, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("update_with_malformed_user_returns_invalid_argument", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPut, baseURL+"/api/v1/personal_info/not-a-uuid/facts/"+uuid.NewString(), map[string]any{
			"content": "updated content",
			"version": 1,
		}, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("update_with_unknown_user_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPut, baseURL+"/api/v1/personal_info/"+unknownUserID()+"/facts/"+uuid.NewString(), map[string]any{
			"content": "updated content",
			"version": 1,
		}, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("delete_with_malformed_user_returns_invalid_argument", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodDelete, baseURL+"/api/v1/personal_info/not-a-uuid/facts/"+uuid.NewString(), nil, nil)

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("delete_with_unknown_user_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodDelete, baseURL+"/api/v1/personal_info/"+unknownUserID()+"/facts/"+uuid.NewString(), nil, nil)

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})
}

func TestPersonalInfoContract_BlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")
	userID := generateSeededUserFixture(t)
	content := fmt.Sprintf("Black-box personal info %d: my phone number is 555-1212.", time.Now().UnixNano())

	var factID string
	var category string
	var factKey string
	var version float64

	t.Run("save_returns_success_envelope", func(t *testing.T) {
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

	t.Run("get_all_returns_facts_array_with_versioned_fact_objects", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/personal_info/"+userID+"/all", nil, nil)

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		var payload map[string]any
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal get-all payload: %v", err)
		}

		facts, ok := payload["facts"].([]any)
		if !ok {
			t.Fatalf("facts field missing or not an array: %s", string(env.Data))
		}
		if len(facts) == 0 {
			t.Fatalf("facts array was empty after save: %s", string(env.Data))
		}

		firstFact, ok := facts[0].(map[string]any)
		if !ok {
			t.Fatalf("first fact was not an object: %#v", facts[0])
		}

		factID = asString(firstFact["factId"])
		category = asString(firstFact["category"])
		factKey = asString(firstFact["factKey"])
		version, _ = firstFact["version"].(float64)

		if strings.TrimSpace(factID) == "" {
			t.Fatalf("factId was empty: %#v", firstFact)
		}
		if strings.TrimSpace(category) == "" {
			t.Fatalf("category was empty: %#v", firstFact)
		}
		if strings.TrimSpace(factKey) == "" {
			t.Fatalf("factKey was empty: %#v", firstFact)
		}
		if version <= 0 {
			t.Fatalf("version = %v, want > 0", version)
		}
	})

	t.Run("query_returns_results_array", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/personal_info/"+userID+"/query", map[string]any{
			"query": "phone number",
			"topK":  3,
		}, nil)

		if status != http.StatusOK {
			t.Fatalf("status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		var payload map[string]any
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("unmarshal query payload: %v", err)
		}

		results, ok := payload["results"].([]any)
		if !ok {
			t.Fatalf("results field missing or not an array: %s", string(env.Data))
		}
		if len(results) == 0 {
			t.Fatalf("results array was empty for saved content: %s", string(env.Data))
		}
	})

	t.Run("update_and_delete_follow_contract_and_stale_update_conflicts", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPut, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+factID, map[string]any{
			"category":   category,
			"factKey":    factKey,
			"factValue":  "555-3434",
			"confidence": 0.9,
			"version":    int(version),
		}, nil)

		if status != http.StatusOK {
			t.Fatalf("update status = %d, want %d", status, http.StatusOK)
		}
		assertSuccessEnvelope(t, env)

		conflictStatus, conflictEnv := apiJSONRequest(t, http.MethodPut, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+factID, map[string]any{
			"category":   category,
			"factKey":    factKey,
			"factValue":  "555-0000",
			"confidence": 0.8,
			"version":    int(version),
		}, nil)

		if conflictStatus != http.StatusConflict {
			t.Fatalf("stale update status = %d, want %d", conflictStatus, http.StatusConflict)
		}
		assertErrorCode(t, conflictEnv, "conflict")

		deleteStatus, deleteEnv := apiJSONRequest(t, http.MethodDelete, baseURL+"/api/v1/personal_info/"+userID+"/facts/"+factID, nil, nil)
		if deleteStatus != http.StatusOK {
			t.Fatalf("delete status = %d, want %d", deleteStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, deleteEnv)
		assertJSONContainsBoolField(t, deleteEnv.Data, "deleted", true)
	})
}
