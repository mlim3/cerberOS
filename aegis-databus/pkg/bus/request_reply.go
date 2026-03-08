package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

var ErrRequestTimeout = errors.New("request timeout")

// Request sends a request and blocks until reply or timeout.
func Request(ctx context.Context, nc *nats.Conn, subject string, payload []byte, timeout time.Duration) ([]byte, error) {
	inbox := nc.NewInbox()
	var reply []byte
	done := make(chan struct{})
	sub, subErr := nc.Subscribe(inbox, func(m *nats.Msg) {
		reply = m.Data
		close(done)
	})
	if subErr != nil {
		return nil, subErr
	}
	defer sub.Unsubscribe()

	if err := nc.PublishRequest(subject, inbox, payload); err != nil {
		return nil, err
	}

	select {
	case <-done:
		return reply, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, ErrRequestTimeout
	}
}

// Reply sends a reply for a request-reply pair.
func Reply(nc *nats.Conn, replySubject string, payload []byte) error {
	return nc.Publish(replySubject, payload)
}

// RequestReplyHandler is a handler that receives requests and sends replies.
type RequestReplyHandler func(subject string, request []byte) (reply []byte, err error)

// SubscribeRequestReply subscribes to a subject and replies to requests.
func SubscribeRequestReply(ctx context.Context, nc *nats.Conn, subject string, handler RequestReplyHandler) error {
	sub, err := nc.Subscribe(subject, func(m *nats.Msg) {
		if m.Reply == "" {
			return
		}
		reply, err := handler(m.Subject, m.Data)
		if err != nil {
			nc.Publish(m.Reply, []byte(`{"error":"`+err.Error()+`"}`))
			return
		}
		nc.Publish(m.Reply, reply)
	})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sub.Unsubscribe()
	}()
	return nil
}

// RequestJSON sends JSON and returns parsed JSON reply.
func RequestJSON(ctx context.Context, nc *nats.Conn, subject string, req interface{}, timeout time.Duration) (map[string]interface{}, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data, err := Request(ctx, nc, subject, payload, timeout)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return out, nil
}
