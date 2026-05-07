package tests

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestVaultSecurity(t *testing.T) {
	userID := uuid.New().String()
	seedUser(t, userID)

	t.Run("Unauthorized Access", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"key_name": "api_key",
			"value":    "super_secret_value",
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/vault/%s/secrets", userID), reqBody, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized for missing API key, got %d", resp.StatusCode)
		}

		var errResp map[string]interface{}
		parseResponse(t, resp, &errResp)

		if errResp["error"] == nil {
			t.Errorf("Expected error envelope in response")
		} else {
			errData := errResp["error"].(map[string]interface{})
			if errData["code"] != "invalid_argument" {
				t.Errorf("Expected error code invalid_argument, got %v", errData["code"])
			}
		}
	})

	t.Run("Audit Verification", func(t *testing.T) {
		reqBody := map[string]interface{}{
			"key_name": "api_key",
			"value":    "super_secret_value",
		}

		headers := map[string]string{
			"X-Internal-API-Key": vaultKey,
			"X-Trace-ID": uuid.New().String(),
		}

		resp := doRequest(t, "POST", fmt.Sprintf("/api/v1/vault/%s/secrets", userID), reqBody, headers)
		if resp.StatusCode != http.StatusCreated {
			var body []byte
			if resp.Body != nil {
				body = make([]byte, 1024)
				n, _ := resp.Body.Read(body)
				body = body[:n]
			}
			t.Fatalf("Failed to save secret: expected 201, got %d, body: %s", resp.StatusCode, string(body))
		}

		eventsResp := doRequest(t, "GET", "/api/v1/system/events?serviceName=VaultService", nil, nil)
		if eventsResp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to get system events: expected 200, got %d", eventsResp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, eventsResp, &result)

		data := result["data"].(map[string]interface{})
		events := data["events"].([]interface{})

		found := false
		for _, e := range events {
			event := e.(map[string]interface{})
			if event["message"] == "VAULT_ACCESS" {
				if meta, ok := event["metadata"].(map[string]interface{}); ok {
					if meta["status"] == "granted" && meta["userId"] == userID {
						found = true
						break
					}
				}
			}
		}

		if !found {
			t.Errorf("Could not find VAULT_ACCESS audit log for the authorized request")
		}
	})
}
