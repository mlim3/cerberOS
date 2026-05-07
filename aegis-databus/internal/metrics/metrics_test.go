package metrics

import (
	"testing"
)

func TestMetricsInitialized(t *testing.T) {
	if MessagesPublished == nil {
		t.Error("MessagesPublished not initialized")
	}
	if PublishErrors == nil {
		t.Error("PublishErrors not initialized")
	}
	if ValidationErrors == nil {
		t.Error("ValidationErrors not initialized")
	}
	if HeartbeatsPublished == nil {
		t.Error("HeartbeatsPublished not initialized")
	}
	if DegradedMode == nil {
		t.Error("DegradedMode not initialized")
	}
	if OutboxRelayProcessed == nil {
		t.Error("OutboxRelayProcessed not initialized")
	}
}
