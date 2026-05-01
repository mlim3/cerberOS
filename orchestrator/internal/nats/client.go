package natsclient

import (
	"fmt"
	"log/slog"
	"sync"

	nats "github.com/nats-io/nats.go"

	"github.com/mlim3/cerberOS/orchestrator/internal/interfaces"
)

// Client is a thin production implementation of interfaces.NATSClient.
// For the current orchestrator integration flow we only need core publish and
// subscribe semantics; JetStream-published messages from agents are still
// visible to core subscribers.
type Client struct {
	mu   sync.RWMutex
	nc   *nats.Conn
	subs []*nats.Subscription
}

// New connects to NATS using optional credentials. An empty credsPath is valid
// for local integration runs against an unsecured dev broker.
func New(url, nodeID, credsPath string) (*Client, error) {
	opts := []nats.Option{
		nats.Name("aegis-orchestrator-" + nodeID),
	}
	if credsPath != "" {
		opts = append(opts, nats.UserCredentials(credsPath))
	}

	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}

	return &Client{nc: nc}, nil
}

func (c *Client) Publish(subject string, data []byte) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.nc == nil || !c.nc.IsConnected() {
		return fmt.Errorf("nats: not connected")
	}
	if err := c.nc.Publish(subject, data); err != nil {
		return err
	}
	return c.nc.Flush()
}

func (c *Client) Subscribe(subject string, handler interfaces.MessageHandler) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.nc == nil || !c.nc.IsConnected() {
		return fmt.Errorf("nats: not connected")
	}

	sub, err := c.nc.Subscribe(subject, func(msg *nats.Msg) {
		_ = handler(msg.Subject, msg.Data)
	})
	if err != nil {
		return err
	}
	c.subs = append(c.subs, sub)
	return c.nc.Flush()
}

// SubscribeDurable subscribes to subject using a JetStream ephemeral push consumer
// with DeliverAll policy. On every startup this replays ALL messages from the stream
// before delivering new ones, rebuilding in-process state (e.g. agentStore) from
// the full write history. Falls back to core NATS when JetStream is unavailable.
//
// The consumer parameter is accepted for interface compatibility but not used —
// the consumer is ephemeral (no durable name) so it starts fresh every restart.
func (c *Client) SubscribeDurable(subject, consumer string, handler interfaces.MessageHandler) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.nc == nil || !c.nc.IsConnected() {
		return fmt.Errorf("nats: not connected")
	}

	js, err := c.nc.JetStream()
	if err != nil {
		slog.Warn("nats: JetStream unavailable, subscribing without historical replay",
			"subject", subject, "error", err)
		return c.subscribeCoreUnlocked(subject, handler)
	}

	// Ensure the stream covering aegis.orchestrator.> exists. It is normally
	// created by aegis-agents (EnsureStreams), but the orchestrator may start
	// first. AddStream is idempotent — if the stream already exists, it's a no-op.
	if _, err := js.StreamInfo("AEGIS_ORCHESTRATOR"); err != nil {
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:      "AEGIS_ORCHESTRATOR",
			Subjects:  []string{"aegis.orchestrator.>"},
			Storage:   nats.FileStorage,
			Retention: nats.LimitsPolicy,
		}); err != nil {
			slog.Warn("nats: could not ensure AEGIS_ORCHESTRATOR stream, subscribing without replay",
				"subject", subject, "error", err)
			return c.subscribeCoreUnlocked(subject, handler)
		}
	}

	// Ephemeral push consumer with DeliverAll: replays the entire stream on every
	// startup, then continues with new messages. No durable name means a fresh
	// consumer is created on every restart — giving a full agentStore rebuild each time.
	sub, err := js.Subscribe(subject,
		func(msg *nats.Msg) {
			if err := handler(msg.Subject, msg.Data); err != nil {
				_ = msg.Nak()
			} else {
				_ = msg.Ack()
			}
		},
		nats.DeliverAll(),
		nats.AckExplicit(),
	)
	if err != nil {
		slog.Warn("nats: JetStream subscribe failed, falling back to core NATS",
			"subject", subject, "error", err)
		return c.subscribeCoreUnlocked(subject, handler)
	}

	c.subs = append(c.subs, sub)
	return c.nc.Flush()
}

// subscribeCoreUnlocked subscribes via core NATS. Must be called with c.mu held.
func (c *Client) subscribeCoreUnlocked(subject string, handler interfaces.MessageHandler) error {
	sub, err := c.nc.Subscribe(subject, func(msg *nats.Msg) {
		_ = handler(msg.Subject, msg.Data)
	})
	if err != nil {
		return fmt.Errorf("nats: subscribe %q: %w", subject, err)
	}
	c.subs = append(c.subs, sub)
	return c.nc.Flush()
}

func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.nc != nil && c.nc.IsConnected()
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, sub := range c.subs {
		_ = sub.Unsubscribe()
	}
	c.subs = nil
	if c.nc != nil {
		_ = c.nc.Drain()
		c.nc.Close()
		c.nc = nil
	}
}
