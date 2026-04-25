// STUB: DEMO ONLY
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"aegis-databus/pkg/bus"
	"aegis-databus/pkg/security"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

const (
	defaultNatsURL    = "nats://127.0.0.1:4222"
	defaultInterval   = 2 * time.Second
	monitorInterval   = 5 * time.Second
	stubComponentName = "aegis-stubs"
)

type CloudEvent struct {
	SpecVersion     string      `json:"specversion"`
	ID              string      `json:"id"`
	Source          string      `json:"source"`
	Type            string      `json:"type"`
	Time            string      `json:"time"`
	CorrelationID   string      `json:"correlationid"`
	TraceID         string      `json:"traceid,omitempty"` // W3C trace_id (32 hex), aligned with HTTP / IO
	DataContentType string      `json:"datacontenttype"`
	Data            interface{} `json:"data"`
}

func main() {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("component", "databus", "module", "stubs")

	nc, js, err := connect(ctx, logger)
	if err != nil {
		logger.Error("connect failed", "error", err, "exit_code", 1)
		os.Exit(1)
	}
	defer nc.Drain()

	go runTaskRouter(ctx, logger, js)
	go runOrchestrator(ctx, logger, js)
	go runAgentFactory(ctx, logger, js)
	go runVault(ctx, logger, js)
	go runMonitoring(ctx, logger, js)

	select {}
}

func connect(ctx context.Context, logger *slog.Logger) (*nats.Conn, nats.JetStreamContext, error) {
	_ = ctx
	url := os.Getenv("AEGIS_NATS_URL")
	if url == "" {
		url = defaultNatsURL
	}

	options := []nats.Option{
		nats.Name("aegis-stubs"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500 * time.Millisecond),
	}
	if seed, _ := security.GetNKeyFromOpenBao(ctx, "aegis-stubs"); seed != "" {
		nkeyOpt, err := nkeyAuthOption(seed)
		if err != nil {
			return nil, nil, err
		}
		options = append([]nats.Option{nkeyOpt}, options...)
	}
	if cfg, err := security.TLSConfigFromEnv(); err == nil && cfg != nil {
		options = append(options, nats.Secure(cfg))
	}

	nc, err := nats.Connect(url, options...)
	if err != nil {
		return nil, nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		return nil, nil, err
	}
	logger.Info("connected to NATS", "url", url)
	return nc, js, nil
}

func nkeyAuthOption(seed string) (nats.Option, error) {
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return nil, err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	sign := func(nonce []byte) ([]byte, error) {
		return kp.Sign(nonce)
	}
	return nats.Nkey(pub, sign), nil
}

func runTaskRouter(ctx context.Context, logger *slog.Logger, js nats.JetStreamContext) {
	ticker := time.NewTicker(defaultInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			payload := CloudEvent{
				SpecVersion:     "1.0",
				ID:              newUUID(),
				Source:          "aegis/task-router",
				Type:            "aegis.tasks.routed",
				Time:            time.Now().UTC().Format(time.RFC3339Nano),
				CorrelationID:   newUUID(),
				TraceID:         traceID32Hex(),
				DataContentType: "application/json",
				Data: map[string]string{
					"taskId": newUUID(),
					"userId": "demo-user",
				},
			}

			data, err := json.Marshal(payload)
			if err != nil {
				logger.Error("task router marshal failed", "task_id", taskIDFromPayload(payload), "trace_id", payload.TraceID, "error", err)
				continue
			}

			if _, err := bus.PublishWithACL(js, stubComponentName, bus.SubjectTasksRouted, data); err != nil {
				logger.Error("task router publish failed", "task_id", taskIDFromPayload(payload), "trace_id", payload.TraceID, "subject", bus.SubjectTasksRouted, "size_bytes", len(data), "error", err)
				continue
			}
			logger.Info("task router published", "task_id", taskIDFromPayload(payload), "trace_id", payload.TraceID, "subject", bus.SubjectTasksRouted, "size_bytes", len(data), "request_id", payload.CorrelationID)
		}
	}
}

func runOrchestrator(ctx context.Context, logger *slog.Logger, js nats.JetStreamContext) {
	sub, err := bus.SubscribeWithACL(js, stubComponentName, bus.SubjectTasksRouted, func(msg *nats.Msg) {
		correlation := extractCorrelationID(msg.Data)
		traceID := extractTraceID(msg.Data)
		taskID := extractTaskID(msg.Data)
		if traceID == "" {
			traceID = correlation
		}
		logger.Info("orchestrator received", "task_id", taskID, "trace_id", traceID, "subject", msg.Subject, "size_bytes", len(msg.Data), "request_id", correlation)
		msg.Ack()

		payload := CloudEvent{
			SpecVersion:     "1.0",
			ID:              newUUID(),
			Source:          "aegis/orchestrator",
			Type:            "aegis.agents.created",
			Time:            time.Now().UTC().Format(time.RFC3339Nano),
			CorrelationID:   correlation,
			TraceID:         traceID,
			DataContentType: "application/json",
			Data: map[string]string{
				"agentId": newUUID(),
				"taskId":  newUUID(),
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			logger.Error("orchestrator marshal failed", "task_id", taskIDFromPayload(payload), "trace_id", traceID, "error", err)
			return
		}
		if _, err := bus.PublishWithACL(js, stubComponentName, bus.SubjectAgentsCreated, data); err != nil {
			logger.Error("orchestrator publish failed", "task_id", taskIDFromPayload(payload), "trace_id", traceID, "subject", bus.SubjectAgentsCreated, "size_bytes", len(data), "error", err)
			return
		}
		logger.Info("orchestrator published", "task_id", taskIDFromPayload(payload), "trace_id", traceID, "subject", bus.SubjectAgentsCreated, "size_bytes", len(data), "request_id", payload.CorrelationID)
	}, nats.Durable("orchestrator"), nats.ManualAck())
	if err != nil {
		logger.Error("orchestrator subscribe failed", "error", err)
		return
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
}

func runAgentFactory(ctx context.Context, logger *slog.Logger, js nats.JetStreamContext) {
	sub, err := bus.SubscribeWithACL(js, stubComponentName, bus.SubjectAgentsCreated, func(msg *nats.Msg) {
		correlation := extractCorrelationID(msg.Data)
		traceID := extractTraceID(msg.Data)
		taskID := extractTaskID(msg.Data)
		if traceID == "" {
			traceID = correlation
		}
		logger.Info("agent factory received", "task_id", taskID, "trace_id", traceID, "subject", msg.Subject, "size_bytes", len(msg.Data), "request_id", correlation)
		msg.Ack()

		payload := CloudEvent{
			SpecVersion:     "1.0",
			ID:              newUUID(),
			Source:          "aegis/agent-factory",
			Type:            "aegis.runtime.completed",
			Time:            time.Now().UTC().Format(time.RFC3339Nano),
			CorrelationID:   correlation,
			TraceID:         traceID,
			DataContentType: "application/json",
			Data: map[string]string{
				"runtimeId": newUUID(),
				"agentId":   newUUID(),
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			logger.Error("agent factory marshal failed", "trace_id", traceID, "error", err)
			return
		}
		if _, err := bus.PublishWithACL(js, stubComponentName, bus.SubjectRuntimeCompleted, data); err != nil {
			logger.Error("agent factory publish failed", "trace_id", traceID, "subject", bus.SubjectRuntimeCompleted, "size_bytes", len(data), "error", err)
			return
		}
		logger.Info("agent factory published", "trace_id", traceID, "subject", bus.SubjectRuntimeCompleted, "size_bytes", len(data), "request_id", payload.CorrelationID)
	}, nats.Durable("agent-factory"), nats.ManualAck())
	if err != nil {
		logger.Error("agent factory subscribe failed", "error", err)
		return
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
}

func runVault(ctx context.Context, logger *slog.Logger, js nats.JetStreamContext) {
	sub, err := bus.SubscribeWithACL(js, stubComponentName, bus.SubjectVault, func(msg *nats.Msg) {
		correlation := extractCorrelationID(msg.Data)
		traceID := extractTraceID(msg.Data)
		taskID := extractTaskID(msg.Data)
		if traceID == "" {
			traceID = correlation
		}
		logger.Info("vault received", "task_id", taskID, "trace_id", traceID, "subject", msg.Subject, "size_bytes", len(msg.Data), "request_id", correlation)
		msg.Ack()
	}, nats.Durable("vault"), nats.ManualAck())
	if err != nil {
		logger.Error("vault subscribe failed", "error", err)
		return
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
}

func runMonitoring(ctx context.Context, logger *slog.Logger, js nats.JetStreamContext) {
	var total uint64
	type subDef struct{ subj, dura string }
	for _, s := range []subDef{
		{bus.SubjectTasks, "mon-tasks"}, {bus.SubjectAgentsLeafWildcard, "mon-agents"}, {bus.SubjectRuntime, "mon-runtime"},
		{bus.SubjectVault, "mon-vault"}, {bus.SubjectMemory, "mon-mem"}, {bus.SubjectMonitoring, "mon-mon"},
	} {
		sub, err := bus.SubscribeWithACL(js, stubComponentName, s.subj, func(msg *nats.Msg) {
			atomic.AddUint64(&total, 1)
			msg.Ack()
		}, nats.Durable(s.dura), nats.ManualAck())
		if err != nil {
			logger.Error("monitoring subscribe failed", "subject", s.subj, "error", err)
			continue
		}
		defer sub.Unsubscribe()
	}

	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logger.Info("monitoring summary", "total_messages", atomic.LoadUint64(&total))
		}
	}
}

func extractCorrelationID(payload []byte) string {
	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return ""
	}
	raw, _ := msg["correlationid"].(string)
	return raw
}

func extractTraceID(payload []byte) string {
	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return ""
	}
	raw, _ := msg["traceid"].(string)
	return raw
}

func extractTaskID(payload []byte) string {
	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return ""
	}
	data, _ := msg["data"].(map[string]interface{})
	if data == nil {
		return ""
	}
	raw, _ := data["taskId"].(string)
	return raw
}

func taskIDFromPayload(payload CloudEvent) string {
	data, _ := payload.Data.(map[string]string)
	if data == nil {
		return ""
	}
	return data["taskId"]
}

// traceID32Hex returns 32 lowercase hex chars (W3C trace_id length).
func traceID32Hex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}
