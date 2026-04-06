package dlq

import (
	"context"
	"log"
	"strings"

	"aegis-databus/pkg/bus"
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/memory"

	"github.com/nats-io/nats.go"
)

// ReplayHandler subscribes to the DLQ and conditionally replays messages.
// If IdempotencyChecker is provided and WasProcessed(messageID) is true, the message is acked
// without replay (avoids duplicate processing when upstream already retried and succeeded).
// Otherwise, republishes to the original subject with Nats-Msg-Id for deduplication.
//
// Consumers should use bus.ForwardToDLQ when sending to DLQ so X-Aegis-Original-Subject is set.
type ReplayHandler struct {
	JS       nats.JetStreamContext
	Checker  memory.IdempotencyChecker // optional: if nil, always replay
	Logger   *log.Logger
	Component string // for ACL, e.g. "aegis-databus"
}

// Start subscribes to aegis.dlq.> and processes messages until ctx is done.
func (h *ReplayHandler) Start(ctx context.Context) {
	if h.JS == nil {
		return
	}
	if h.Logger == nil {
		h.Logger = log.Default()
	}
	component := h.Component
	if component == "" {
		component = "aegis-databus"
	}

	_, err := bus.SubscribeWithACL(h.JS, component, bus.SubjectDLQ, func(m *nats.Msg) {
		originalSubject := m.Header.Get(bus.HeaderOriginalSubject)
		if originalSubject == "" {
			h.Logger.Printf("dlq-replay: missing %s, discarding (ack)", bus.HeaderOriginalSubject)
			m.Ack()
			return
		}

		msgID := envelope.ExtractID(m.Data)
		if h.Checker != nil && msgID != "" {
			done, err := h.Checker.WasProcessed(ctx, msgID)
			if err != nil {
				h.Logger.Printf("dlq-replay: WasProcessed id=%s err=%v, will replay", msgID, err)
			} else if done {
				h.Logger.Printf("dlq-replay: id=%s already processed, skipping replay", msgID)
				m.Ack()
				return
			}
		}

		// Republish with MsgId for 120s dedup (same id republished within window is dropped)
		_, err := bus.PublishWithMsgID(h.JS, originalSubject, m.Data, msgID)
		if err != nil {
			h.Logger.Printf("dlq-replay: republish subject=%s id=%s err=%v", originalSubject, msgID, err)
			m.Nak()
			return
		}

		if h.Checker != nil && msgID != "" {
			_ = h.Checker.RecordProcessed(ctx, msgID)
		}
		h.Logger.Printf("dlq-replay: replayed subject=%s id=%s", originalSubject, msgID)
		m.Ack()
	}, nats.Durable("dlq-replay"), nats.ManualAck())

	if err != nil {
		h.Logger.Printf("dlq-replay: subscribe failed: %v", err)
		return
	}

	<-ctx.Done()
}

// SubjectToDLQ returns a DLQ subject that encodes the original, for streams that route by subject.
// Example: aegis.dlq.failed.aegis_tasks_routed (dots replaced with underscores).
func SubjectToDLQ(original string) string {
	s := strings.ReplaceAll(original, ".", "_")
	return bus.SubjectDLQ + ".failed." + s
}
