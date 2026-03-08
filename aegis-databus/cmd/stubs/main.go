// STUB: DEMO ONLY
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sync/atomic"
	"time"

	"aegis-databus/pkg/bus"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

const (
	defaultNatsURL  = "nats://127.0.0.1:4222"
	defaultInterval = 2 * time.Second
	monitorInterval = 5 * time.Second
)

type CloudEvent struct {
	SpecVersion     string      `json:"specversion"`
	ID              string      `json:"id"`
	Source          string      `json:"source"`
	Type            string      `json:"type"`
	Time            string      `json:"time"`
	CorrelationID   string      `json:"correlationid"`
	DataContentType string      `json:"datacontenttype"`
	Data            interface{} `json:"data"`
}

func main() {
	ctx := context.Background()
	logger := log.New(os.Stdout, "stubs ", log.LstdFlags)

	nc, js, err := connect(ctx, logger)
	if err != nil {
		logger.Fatalf("connect failed: %v", err)
	}
	defer nc.Drain()

	go runTaskRouter(ctx, logger, js)
	go runOrchestrator(ctx, logger, js)
	go runAgentFactory(ctx, logger, js)
	go runVault(ctx, logger, js)
	go runMonitoring(ctx, logger, js)

	select {}
}

func connect(ctx context.Context, logger *log.Logger) (*nats.Conn, nats.JetStreamContext, error) {
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
	if seed := os.Getenv("AEGIS_NKEY_SEED"); seed != "" {
		nkeyOpt, err := nkeyAuthOption(seed)
		if err != nil {
			return nil, nil, err
		}
		options = append([]nats.Option{nkeyOpt}, options...)
	}

	nc, err := nats.Connect(url, options...)
	if err != nil {
		return nil, nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		return nil, nil, err
	}
	logger.Printf("connected nats url=%s", url)
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

func runTaskRouter(ctx context.Context, logger *log.Logger, js nats.JetStreamContext) {
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
				DataContentType: "application/json",
				Data: map[string]string{
					"taskId": newUUID(),
					"userId": "demo-user",
				},
			}

			data, err := json.Marshal(payload)
			if err != nil {
				logger.Printf("task-router marshal failed: %v", err)
				continue
			}

			if _, err := js.Publish(bus.SubjectTasksRouted, data); err != nil {
				logger.Printf("task-router publish failed subject=%s size=%d err=%v", bus.SubjectTasksRouted, len(data), err)
				continue
			}
			logger.Printf("task-router published subject=%s size=%d correlation=%s", bus.SubjectTasksRouted, len(data), payload.CorrelationID)
		}
	}
}

func runOrchestrator(ctx context.Context, logger *log.Logger, js nats.JetStreamContext) {
	sub, err := js.Subscribe(bus.SubjectTasksRouted, func(msg *nats.Msg) {
		correlation := extractCorrelationID(msg.Data)
		logger.Printf("orchestrator received subject=%s size=%d correlation=%s", msg.Subject, len(msg.Data), correlation)
		msg.Ack()

		payload := CloudEvent{
			SpecVersion:     "1.0",
			ID:              newUUID(),
			Source:          "aegis/orchestrator",
			Type:            "aegis.agents.created",
			Time:            time.Now().UTC().Format(time.RFC3339Nano),
			CorrelationID:   correlation,
			DataContentType: "application/json",
			Data: map[string]string{
				"agentId": newUUID(),
				"taskId":  newUUID(),
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			logger.Printf("orchestrator marshal failed: %v", err)
			return
		}
		if _, err := js.Publish(bus.SubjectAgentsCreated, data); err != nil {
			logger.Printf("orchestrator publish failed subject=%s size=%d err=%v", bus.SubjectAgentsCreated, len(data), err)
			return
		}
		logger.Printf("orchestrator published subject=%s size=%d correlation=%s", bus.SubjectAgentsCreated, len(data), payload.CorrelationID)
	}, nats.Durable("orchestrator"), nats.ManualAck())
	if err != nil {
		logger.Printf("orchestrator subscribe failed: %v", err)
		return
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
}

func runAgentFactory(ctx context.Context, logger *log.Logger, js nats.JetStreamContext) {
	sub, err := js.Subscribe(bus.SubjectAgentsCreated, func(msg *nats.Msg) {
		correlation := extractCorrelationID(msg.Data)
		logger.Printf("agent-factory received subject=%s size=%d correlation=%s", msg.Subject, len(msg.Data), correlation)
		msg.Ack()

		payload := CloudEvent{
			SpecVersion:     "1.0",
			ID:              newUUID(),
			Source:          "aegis/agent-factory",
			Type:            "aegis.runtime.completed",
			Time:            time.Now().UTC().Format(time.RFC3339Nano),
			CorrelationID:   correlation,
			DataContentType: "application/json",
			Data: map[string]string{
				"runtimeId": newUUID(),
				"agentId":   newUUID(),
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			logger.Printf("agent-factory marshal failed: %v", err)
			return
		}
		if _, err := js.Publish(bus.SubjectRuntimeCompleted, data); err != nil {
			logger.Printf("agent-factory publish failed subject=%s size=%d err=%v", bus.SubjectRuntimeCompleted, len(data), err)
			return
		}
		logger.Printf("agent-factory published subject=%s size=%d correlation=%s", bus.SubjectRuntimeCompleted, len(data), payload.CorrelationID)
	}, nats.Durable("agent-factory"), nats.ManualAck())
	if err != nil {
		logger.Printf("agent-factory subscribe failed: %v", err)
		return
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
}

func runVault(ctx context.Context, logger *log.Logger, js nats.JetStreamContext) {
	sub, err := js.Subscribe(bus.SubjectVault, func(msg *nats.Msg) {
		correlation := extractCorrelationID(msg.Data)
		logger.Printf("vault received subject=%s size=%d correlation=%s", msg.Subject, len(msg.Data), correlation)
		msg.Ack()
	}, nats.Durable("vault"), nats.ManualAck())
	if err != nil {
		logger.Printf("vault subscribe failed: %v", err)
		return
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
}

func runMonitoring(ctx context.Context, logger *log.Logger, js nats.JetStreamContext) {
	var total uint64
	type subDef struct{ subj, dura string }
	for _, s := range []subDef{
		{bus.SubjectTasks, "mon-tasks"}, {bus.SubjectAgents, "mon-agents"}, {bus.SubjectRuntime, "mon-runtime"},
		{bus.SubjectVault, "mon-vault"}, {bus.SubjectMemory, "mon-mem"}, {bus.SubjectMonitoring, "mon-mon"},
	} {
		sub, err := js.Subscribe(s.subj, func(msg *nats.Msg) {
			atomic.AddUint64(&total, 1)
			msg.Ack()
		}, nats.Durable(s.dura), nats.ManualAck())
		if err != nil {
			logger.Printf("monitoring subscribe %s failed: %v", s.subj, err)
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
			logger.Printf("monitoring summary total_messages=%d", atomic.LoadUint64(&total))
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
