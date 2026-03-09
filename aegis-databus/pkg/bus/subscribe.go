package bus

import (
	"aegis-databus/pkg/security"
	"github.com/nats-io/nats.go"
)

// SubscribeWithACL subscribes only if component is allowed (SR-DB-006 DLQ enforcement).
// Components must use this for all subscriptions to enforce subject-level ACL.
// For aegis.dlq.>, only databus, aegis-databus, admin, dlq-admin may subscribe.
func SubscribeWithACL(js nats.JetStreamContext, component, subject string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if err := security.CheckSubscribe(component, subject); err != nil {
		return nil, err
	}
	return js.Subscribe(subject, cb, opts...)
}

// QueueSubscribeWithACL is QueueSubscribe with ACL check (SR-DB-006).
func QueueSubscribeWithACL(js nats.JetStreamContext, component, subject, queue string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if err := security.CheckSubscribe(component, subject); err != nil {
		return nil, err
	}
	return js.QueueSubscribe(subject, queue, cb, opts...)
}
