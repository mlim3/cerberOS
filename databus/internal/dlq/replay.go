package dlq

import (
	"context"
	"log/slog"
	"os"
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
	JS        nats.JetStreamContext
	Checker   memory.IdempotencyChecker // optional: if nil, always replay
	Logger    *slog.Logger
	Component string // for ACL, e.g. "aegis-databus"
}

// Start subscribes to aegis.dlq.> and processes messages until ctx is done.
func (h *ReplayHandler) Start(ctx context.Context) {
	if h.JS == nil {
		return
	}
	if h.Logger == nil {
		h.Logger = slog.New(slog.NewJSONHandler(os.Stdout, nil)).
			With("component", "databus", "module", "dlq-replay")
	}
	logger := h.Logger
	component := h.Component
	if component == "" {
		component = "aegis-databus"
	}

	_, err := bus.SubscribeWithACL(h.JS, component, bus.SubjectDLQ, func(m *nats.Msg) {
		originalSubject := m.Header.Get(bus.HeaderOriginalSubject)
		if originalSubject == "" {
			logger.Warn("dlq original subject missing, discarding", "header", bus.HeaderOriginalSubject)
			m.Ack()
			return
		}

		msgID := envelope.ExtractID(m.Data)
		if h.Checker != nil && msgID != "" {
			done, err := h.Checker.WasProcessed(ctx, msgID)
			if err != nil {
				logger.Warn("dlq idempotency check failed, will replay", "message_id", msgID, "error", err)
			} else if done {
				logger.Info("dlq message already processed, skipping replay", "message_id", msgID)
				m.Ack()
				return
			}
		}

		// Republish with MsgId for 120s dedup (same id republished within window is dropped)
		_, err := bus.PublishWithMsgID(h.JS, originalSubject, m.Data, msgID)
		if err != nil {
			logger.Error("dlq republish failed", "subject", originalSubject, "message_id", msgID, "error", err)
			m.Nak()
			return
		}

		if h.Checker != nil && msgID != "" {
			_ = h.Checker.RecordProcessed(ctx, msgID)
		}
		logger.Info("dlq message replayed", "subject", originalSubject, "message_id", msgID)
		m.Ack()
	}, nats.Durable("dlq-replay"), nats.ManualAck())

	if err != nil {
		logger.Error("dlq subscribe failed", "error", err)
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
