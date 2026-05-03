package bus

import (
	"log/slog"
	"strings"
	"time"

	"aegis-databus/pkg/security"
	"aegis-databus/pkg/validation"
	"github.com/nats-io/nats.go"
)

func defaultValidator() validation.Validator { return validation.Strict }

// SubscribeWithACL subscribes only if component is allowed (SR-DB-006 DLQ enforcement).
// Uses validation.Strict. For tests, use SubscribeWithACLValidator with validation.NoOp.
func SubscribeWithACL(js nats.JetStreamContext, component, subject string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	return subscribeWithACLImpl(js, component, subject, cb, defaultValidator(), opts...)
}

// SubscribeWithACLValidator is SubscribeWithACL with explicit validator (e.g. validation.NoOp for tests).
func SubscribeWithACLValidator(js nats.JetStreamContext, component, subject string, cb nats.MsgHandler, val validation.Validator, opts ...nats.SubOpt) (*nats.Subscription, error) {
	return subscribeWithACLImpl(js, component, subject, cb, val, opts...)
}

func subscribeWithACLImpl(js nats.JetStreamContext, component, subject string, cb nats.MsgHandler, val validation.Validator, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if err := val.ValidateSubscribe(component, subject); err != nil {
		return nil, err
	}
	if err := security.CheckSubscribePlainForbidden(subject); err != nil {
		return nil, err
	}
	return retrySubscribe(func() (*nats.Subscription, error) {
		return js.Subscribe(subject, cb, opts...)
	}, subject)
}

// QueueSubscribeWithACL is QueueSubscribe with ACL check (SR-DB-006). Uses validation.Strict.
// EDD §9.2: sensitive agent subjects require queue group security.QueueAgentsComponent.
func QueueSubscribeWithACL(js nats.JetStreamContext, component, subject, queue string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if err := defaultValidator().ValidateSubscribe(component, subject); err != nil {
		return nil, err
	}
	if err := security.CheckQueueSubscribe(component, subject, queue); err != nil {
		return nil, err
	}
	return retrySubscribe(func() (*nats.Subscription, error) {
		return js.QueueSubscribe(subject, queue, cb, opts...)
	}, subject)
}

// QueueSubscribeWithACLValidator is QueueSubscribeWithACL with explicit validator.
func QueueSubscribeWithACLValidator(js nats.JetStreamContext, component, subject, queue string, cb nats.MsgHandler, val validation.Validator, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if err := val.ValidateSubscribe(component, subject); err != nil {
		return nil, err
	}
	if err := security.CheckQueueSubscribe(component, subject, queue); err != nil {
		return nil, err
	}
	return retrySubscribe(func() (*nats.Subscription, error) {
		return js.QueueSubscribe(subject, queue, cb, opts...)
	}, subject)
}

// retrySubscribe retries fn with exponential backoff when a durable push consumer
// is already bound to another subscriber (rolling update scenario). The old pod
// releases the binding when its NATS connection closes, typically within seconds.
func retrySubscribe(fn func() (*nats.Subscription, error), subject string) (*nats.Subscription, error) {
	const maxAttempts = 20
	backoff := 2 * time.Second
	var sub *nats.Subscription
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		sub, err = fn()
		if err == nil {
			return sub, nil
		}
		if attempt == maxAttempts || !strings.Contains(err.Error(), "already bound") {
			return nil, err
		}
		slog.Default().With("module", "subscribe-retry").Warn(
			"durable consumer is already bound to another subscriber; retrying with backoff (rolling restart in progress)",
			"subject", subject,
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"backoff", backoff,
		)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil, err
}
