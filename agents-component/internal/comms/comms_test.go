package comms_test

import (
	"encoding/json"
	"testing"

	"github.com/aegis/aegis-agents/internal/comms"
)

func TestStubClientPublishSubscribe(t *testing.T) {
	c := comms.NewStubClient()
	defer c.Close()

	type payload struct{ Value string }

	received := make(chan *comms.Message, 1)
	if err := c.Subscribe("test.subject", func(msg *comms.Message) {
		received <- msg
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := c.Publish("test.subject", payload{Value: "hello"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	msg := <-received
	var got payload
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Value != "hello" {
		t.Errorf("got %q, want %q", got.Value, "hello")
	}
}

func TestStubClientNilHandlerError(t *testing.T) {
	c := comms.NewStubClient()
	defer c.Close()

	if err := c.Subscribe("test.subject", nil); err == nil {
		t.Error("expected error for nil handler, got nil")
	}
}
