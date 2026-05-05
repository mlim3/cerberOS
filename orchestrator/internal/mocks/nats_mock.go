package mocks

import (
	"errors"
	"sync"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
)

// NATSMock is a controllable in-memory implementation of interfaces.NATSClient.
// Messages published are captured in Published for test inspection.
// Subscriptions are stored and can be triggered manually via Deliver().
//
// Usage:
//
//	nats := mocks.NewNATSMock()
//	nats.ShouldFailPublish = true         // simulate publish failure
//	nats.Deliver("aegis.agents.status.events", data)  // inject a fake inbound message
//	nats.Published["aegis.agents.tasks.inbound"]       // inspect outbound messages
type NATSMock struct {
	mu sync.RWMutex

	// Control flags
	ShouldFailPublish    bool // Publish() returns error
	ShouldBeDisconnected bool // IsConnected() returns false; Publish() returns error

	// Captured outbound messages — map[subject][]payloads
	Published map[string][][]byte

	// Registered subscriptions — map[subject]handler
	subscriptions map[string]interfaces.MessageHandler

	// Inspection
	PublishCallCount   int
	SubscribeCallCount int
}

func NewNATSMock() *NATSMock {
	return &NATSMock{
		Published:     make(map[string][][]byte),
		subscriptions: make(map[string]interfaces.MessageHandler),
	}
}

func (m *NATSMock) Publish(subject string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PublishCallCount++

	if m.ShouldBeDisconnected {
		return errors.New("nats: not connected")
	}
	if m.ShouldFailPublish {
		return errors.New("nats: publish failed")
	}

	m.Published[subject] = append(m.Published[subject], data)
	return nil
}

func (m *NATSMock) Subscribe(subject string, handler interfaces.MessageHandler) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.SubscribeCallCount++
	m.subscriptions[subject] = handler
	return nil
}

// SubscribeDurable delegates to Subscribe in the mock — no historical replay in tests.
func (m *NATSMock) SubscribeDurable(subject, consumer string, handler interfaces.MessageHandler) error {
	return m.Subscribe(subject, handler)
}

func (m *NATSMock) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return !m.ShouldBeDisconnected
}

func (m *NATSMock) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ShouldBeDisconnected = true
}

// Deliver simulates an inbound NATS message arriving on the given subject.
// Triggers the registered subscription handler if one exists.
// Use this in tests to inject fake messages from other components.
func (m *NATSMock) Deliver(subject string, data []byte) error {
	m.mu.RLock()
	handler, ok := m.subscriptions[subject]
	m.mu.RUnlock()

	if !ok {
		return errors.New("no subscription registered for subject: " + subject)
	}
	return handler(subject, data)
}

// LastPublished returns the most recent payload published to a given subject.
// Returns nil if nothing has been published to that subject.
func (m *NATSMock) LastPublished(subject string) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msgs := m.Published[subject]
	if len(msgs) == 0 {
		return nil
	}
	return msgs[len(msgs)-1]
}

// Reset clears all captured messages, subscriptions, and control flags.
func (m *NATSMock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Published = make(map[string][][]byte)
	m.subscriptions = make(map[string]interfaces.MessageHandler)
	m.ShouldFailPublish = false
	m.ShouldBeDisconnected = false
	m.PublishCallCount = 0
	m.SubscribeCallCount = 0
}
