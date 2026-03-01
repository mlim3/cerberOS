// Package comms is M1 — the single Communications Interface for all inter-component
// messaging. No other module may call external components directly; all messages
// flow through this package.
package comms

import (
	"encoding/json"
	"fmt"
	"sync"
)

// MessageHandler is the callback signature for subscribed subjects.
type MessageHandler func(msg *Message)

// Message is a received NATS message.
type Message struct {
	Subject string
	Data    []byte
}

// Client is the interface that wraps publish and subscribe operations.
// All inter-component communication uses this interface.
type Client interface {
	// Publish sends a JSON-encoded payload to the given subject.
	Publish(subject string, payload interface{}) error

	// Subscribe registers a handler for the given subject.
	Subscribe(subject string, handler MessageHandler) error

	// Close tears down the underlying connection.
	Close() error
}

// stubClient is an in-process stub used until real NATS integration is wired.
type stubClient struct {
	mu          sync.RWMutex
	subscribers map[string][]MessageHandler
}

// NewStubClient returns a Client backed by in-process channels.
// Replace with a NATS-backed implementation when the Communications Component
// is available.
func NewStubClient() Client {
	return &stubClient{
		subscribers: make(map[string][]MessageHandler),
	}
}

func (c *stubClient) Publish(subject string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("comms: marshal payload for %q: %w", subject, err)
	}

	c.mu.RLock()
	handlers := c.subscribers[subject]
	c.mu.RUnlock()

	msg := &Message{Subject: subject, Data: data}
	for _, h := range handlers {
		h(msg)
	}
	return nil
}

func (c *stubClient) Subscribe(subject string, handler MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("comms: nil handler for subject %q", subject)
	}
	c.mu.Lock()
	c.subscribers[subject] = append(c.subscribers[subject], handler)
	c.mu.Unlock()
	return nil
}

func (c *stubClient) Close() error {
	c.mu.Lock()
	c.subscribers = make(map[string][]MessageHandler)
	c.mu.Unlock()
	return nil
}
