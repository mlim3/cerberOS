package natsclient

import (
	"fmt"
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
