package comms_test

import (
	"encoding/json"
	"testing"

	"github.com/cerberOS/agents-component/internal/comms"
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

	if err := c.Publish("test.subject", comms.PublishOptions{MessageType: "test.event"}, payload{Value: "hello"}); err != nil {
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

func TestStubClientSubscribeDurable(t *testing.T) {
	c := comms.NewStubClient()
	defer c.Close()

	type payload struct{ Value string }

	received := make(chan *comms.Message, 1)
	if err := c.SubscribeDurable("test.durable", "consumer-name", func(msg *comms.Message) {
		received <- msg
	}); err != nil {
		t.Fatalf("SubscribeDurable: %v", err)
	}

	if err := c.Publish("test.durable", comms.PublishOptions{MessageType: "test.event", CorrelationID: "task-1"}, payload{Value: "world"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	msg := <-received
	var got payload
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Value != "world" {
		t.Errorf("got %q, want %q", got.Value, "world")
	}
	// Ack and Nak must be callable without error (no-ops in stub).
	if err := msg.Ack(); err != nil {
		t.Errorf("Ack: %v", err)
	}
	if err := msg.Nak(); err != nil {
		t.Errorf("Nak: %v", err)
	}
}

func TestStubClientNilHandlerError(t *testing.T) {
	c := comms.NewStubClient()
	defer c.Close()

	if err := c.Subscribe("test.subject", nil); err == nil {
		t.Error("expected error for nil handler on Subscribe, got nil")
	}
	if err := c.SubscribeDurable("test.subject", "d", nil); err == nil {
		t.Error("expected error for nil handler on SubscribeDurable, got nil")
	}
}

func TestStubClientMultipleSubscribers(t *testing.T) {
	c := comms.NewStubClient()
	defer c.Close()

	var count int
	for i := 0; i < 3; i++ {
		if err := c.Subscribe("multi.subject", func(msg *comms.Message) {
			count++
		}); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
	}

	if err := c.Publish("multi.subject", comms.PublishOptions{}, struct{}{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if count != 3 {
		t.Errorf("got %d deliveries, want 3", count)
	}
}

func TestStubClientCloseRemovesSubscribers(t *testing.T) {
	c := comms.NewStubClient()

	delivered := false
	_ = c.Subscribe("close.test", func(msg *comms.Message) { delivered = true })
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_ = c.Publish("close.test", comms.PublishOptions{}, struct{}{})
	if delivered {
		t.Error("expected no delivery after Close")
	}
}
