package tests

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestSystemAndTracing(t *testing.T) {
	t.Run("TraceID Propagation", func(t *testing.T) {
		customTraceID := uuid.New().String()

		reqBody := map[string]interface{}{
			"message":     "Test tracing message",
			"severity":    "info",
			"serviceName": "test-suite",
			"traceId":     customTraceID,
		}

		headers := map[string]string{
			"X-Trace-ID": customTraceID,
		}

		resp := doRequest(t, "POST", "/api/v1/system/events", reqBody, headers)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Failed to create system event: expected 201, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		parseResponse(t, resp, &result)
		_ = result["data"].(map[string]interface{})

		eventsResp := doRequest(t, "GET", "/api/v1/system/events?serviceName=test-suite", nil, nil)
		var eventsResult map[string]interface{}
		parseResponse(t, eventsResp, &eventsResult)

		eventsData := eventsResult["data"].(map[string]interface{})
		events := eventsData["events"].([]interface{})

		found := false
		for _, e := range events {
			event := e.(map[string]interface{})
			if event["message"] == "Test tracing message" && event["traceId"] == customTraceID {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("Could not find system event with custom TraceID in database")
		}
	})
}
