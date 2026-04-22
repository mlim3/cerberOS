package tests

import (
	"net/http"
	"strings"
	"testing"
)

func TestVaultContract_BlackBox(t *testing.T) {
	baseURL := strings.TrimRight(blackboxBaseURL(), "/")

	t.Run("missing_api_key_uses_standard_error_envelope", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/vault/"+validUserFixture(t)+"/secrets", map[string]string{
			"key_name": vaultKeyName(),
			"value":    "secret-value",
		}, nil)

		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
		}
		assertErrorEnvelope(t, env, "missing API key should fail")
	})

	t.Run("invalid_api_key_uses_standard_error_envelope", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/vault/"+validUserFixture(t)+"/secrets", map[string]string{
			"key_name": vaultKeyName(),
			"value":    "secret-value",
		}, map[string]string{
			"X-Internal-API-Key": "definitely-invalid",
		})

		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
		}
		assertErrorEnvelope(t, env, "invalid API key should fail")
	})

	apiKey := requiredEnv(t, "INTERNAL_VAULT_API_KEY")

	t.Run("malformed_user_id_returns_invalid_argument", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/vault/not-a-uuid/secrets", map[string]string{
			"key_name": vaultKeyName(),
			"value":    "secret-value",
		}, map[string]string{
			"X-Internal-API-Key": apiKey,
		})

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("unknown_user_returns_not_found", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/vault/"+unknownUserID()+"/secrets", map[string]string{
			"key_name": vaultKeyName(),
			"value":    "secret-value",
		}, map[string]string{
			"X-Internal-API-Key": apiKey,
		})

		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
		}
		assertErrorCode(t, env, "not_found")
	})

	t.Run("missing_required_fields_return_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/vault/"+validUserFixture(t)+"/secrets", map[string]string{}, map[string]string{
			"X-Internal-API-Key": apiKey,
		})

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("create_get_update_delete_follow_contract", func(t *testing.T) {
		userID := validUserFixture(t)
		keyName := vaultKeyName()

		createStatus, createEnv := apiJSONRequest(t, http.MethodPost, baseURL+"/api/v1/vault/"+userID+"/secrets", map[string]string{
			"key_name": keyName,
			"value":    "secret-value",
		}, map[string]string{
			"X-Internal-API-Key": apiKey,
		})
		if createStatus != http.StatusCreated {
			t.Fatalf("create status = %d, want %d", createStatus, http.StatusCreated)
		}
		assertSuccessEnvelope(t, createEnv)

		getStatus, getEnv := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/vault/"+userID+"/secrets?key_name="+keyName, nil, map[string]string{
			"X-Internal-API-Key": apiKey,
		})
		if getStatus != http.StatusOK {
			t.Fatalf("get status = %d, want %d", getStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, getEnv)
		assertJSONContainsStringField(t, getEnv.Data, "key_name", keyName)
		assertJSONContainsStringField(t, getEnv.Data, "value", "secret-value")

		updateStatus, updateEnv := apiJSONRequest(t, http.MethodPut, baseURL+"/api/v1/vault/"+userID+"/secrets/"+keyName, map[string]string{
			"value": "new-secret-value",
		}, map[string]string{
			"X-Internal-API-Key": apiKey,
		})
		if updateStatus != http.StatusOK {
			t.Fatalf("update status = %d, want %d", updateStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, updateEnv)

		getUpdatedStatus, getUpdatedEnv := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/vault/"+userID+"/secrets?key_name="+keyName, nil, map[string]string{
			"X-Internal-API-Key": apiKey,
		})
		if getUpdatedStatus != http.StatusOK {
			t.Fatalf("get-updated status = %d, want %d", getUpdatedStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, getUpdatedEnv)
		assertJSONContainsStringField(t, getUpdatedEnv.Data, "key_name", keyName)
		assertJSONContainsStringField(t, getUpdatedEnv.Data, "value", "new-secret-value")

		deleteStatus, deleteEnv := apiJSONRequest(t, http.MethodDelete, baseURL+"/api/v1/vault/"+userID+"/secrets/"+keyName, nil, map[string]string{
			"X-Internal-API-Key": apiKey,
		})
		if deleteStatus != http.StatusOK {
			t.Fatalf("delete status = %d, want %d", deleteStatus, http.StatusOK)
		}
		assertSuccessEnvelope(t, deleteEnv)
	})

	t.Run("get_secret_missing_query_parameter_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodGet, baseURL+"/api/v1/vault/"+validUserFixture(t)+"/secrets", nil, map[string]string{
			"X-Internal-API-Key": apiKey,
		})

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})

	t.Run("update_secret_missing_value_returns_bad_request", func(t *testing.T) {
		status, env := apiJSONRequest(t, http.MethodPut, baseURL+"/api/v1/vault/"+validUserFixture(t)+"/secrets/"+vaultKeyName(), map[string]string{}, map[string]string{
			"X-Internal-API-Key": apiKey,
		})

		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
		}
		assertErrorCode(t, env, "invalid_argument")
	})
}
