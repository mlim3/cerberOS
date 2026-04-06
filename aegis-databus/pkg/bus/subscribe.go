package bus

import (
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
	return js.Subscribe(subject, cb, opts...)
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
	return js.QueueSubscribe(subject, queue, cb, opts...)
}

// QueueSubscribeWithACLValidator is QueueSubscribeWithACL with explicit validator.
func QueueSubscribeWithACLValidator(js nats.JetStreamContext, component, subject, queue string, cb nats.MsgHandler, val validation.Validator, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if err := val.ValidateSubscribe(component, subject); err != nil {
		return nil, err
	}
	if err := security.CheckQueueSubscribe(component, subject, queue); err != nil {
		return nil, err
	}
	return js.QueueSubscribe(subject, queue, cb, opts...)
}
