// DEMO: Simulates all 6 components interacting with the Data Bus per EDD.
package main

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"aegis-databus/pkg/bus"
	"aegis-databus/pkg/envelope"
	"github.com/nats-io/nats.go"
)

// runIO simulates UI Layer: publishes user actions, subscribes to task updates.
func runIO(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *log.Logger) {
	// Subscribe to task updates (FR-DB-001, FR-DB-005 wildcard)
	js.Subscribe(bus.SubjectTasks, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		logger.Printf("[I/O] received subject=%s size=%d correlation=%s", m.Subject, len(m.Data), corr)
		m.Ack()
	}, nats.Durable("io-tasks"), nats.ManualAck())
	time.Sleep(100 * time.Millisecond)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ev := envelope.Build("aegis/io", "aegis.ui.task_submitted", map[string]string{
				"taskId": envelope.NewID(), "userId": "demo-user", "goal": "research task",
			})
			if err := envelope.Validate(ev.MustMarshal()); err != nil {
				logger.Printf("[I/O] validation failed: %v", err)
				continue
			}
			if _, err := js.Publish(bus.SubjectUIAction, ev.MustMarshal()); err != nil {
				logger.Printf("[I/O] publish failed: %v", err)
				continue
			}
			logger.Printf("[I/O] published aegis.ui.action correlation=%s", ev.CorrelationID)
		}
	}
}

// runOrchestrator simulates Task Router + Task Planner + Agent Manager.
func runOrchestrator(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *log.Logger) {
	var planCorr string

	// Subscribe to UI actions
	js.Subscribe(bus.SubjectUIAction, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		logger.Printf("[Orchestrator] received ui.action correlation=%s", corr)
		m.Ack()
		planCorr = corr
		// Publish tasks.routed
		ev := envelope.Build("aegis/orchestrator", "aegis.tasks.routed", map[string]string{
			"taskId": envelope.NewID(), "userId": "demo-user", "complexity": "high",
		})
		ev.SetCorrelationID(corr)
		js.Publish(bus.SubjectTasksRouted, ev.MustMarshal())
		logger.Printf("[Orchestrator] published tasks.routed correlation=%s", corr)
	}, nats.Durable("orch-ui"), nats.ManualAck())

	// Subscribe to plan (self-loop for demo - in real system Task Planner publishes)
	js.Subscribe(bus.SubjectTasksRouted, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		m.Ack()
		ev := envelope.Build("aegis/orchestrator", "aegis.tasks.plan_created", map[string]interface{}{
			"subtasks": 3, "planId": envelope.NewID(),
		})
		ev.SetCorrelationID(corr)
		js.Publish(bus.SubjectTasksPlanCreated, ev.MustMarshal())
		logger.Printf("[Orchestrator] published plan_created subtasks=3 correlation=%s", corr)
	}, nats.Durable("orch-router"), nats.ManualAck())

	// Subscribe to plan_created, publish agents.created (queue group - FR-DB-004)
	js.QueueSubscribe(bus.SubjectTasksPlanCreated, "agent-managers", func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		m.Ack()
		for i := 0; i < 3; i++ {
			ev := envelope.Build("aegis/orchestrator", "aegis.agents.created", map[string]string{
				"agentId": envelope.NewID(), "taskId": planCorr,
			})
			ev.SetCorrelationID(corr)
			js.Publish(bus.SubjectAgentsCreated, ev.MustMarshal())
		}
		logger.Printf("[Orchestrator] published agents.created x3 correlation=%s", corr)
	}, nats.Durable("orch-agents"), nats.ManualAck())

	<-ctx.Done()
	_ = planCorr
}

// runMemory simulates Memory & Context Manager.
func runMemory(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *log.Logger) {
	// Request-reply: respond to personalization (FR-DB-002)
	nc.Subscribe(bus.SubjectPersonalization, func(m *nats.Msg) {
		if m.Reply == "" {
			return
		}
		reply, _ := json.Marshal(map[string]string{"userId": "demo-user", "preferences": "{}"})
		nc.Publish(m.Reply, reply)
	})
	time.Sleep(50 * time.Millisecond)

	// Subscribe to agents.created, publish memory.saved
	js.Subscribe(bus.SubjectAgentsCreated, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		m.Ack()
		ev := envelope.Build("aegis/memory", "aegis.memory.saved", map[string]string{
			"agentId": envelope.NewID(),
		})
		ev.SetCorrelationID(corr)
		js.Publish(bus.SubjectMemorySaved, ev.MustMarshal())
		logger.Printf("[Memory] received agents.created, published memory.saved correlation=%s", corr)
	}, nats.Durable("memory-agents"), nats.ManualAck())

	<-ctx.Done()
}

// runVault simulates Permission Manager / Vault.
func runVault(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *log.Logger) {
	js.Subscribe(bus.SubjectVault, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		logger.Printf("[Vault] received subject=%s size=%d correlation=%s", m.Subject, len(m.Data), corr)
		m.Ack()
	}, nats.Durable("vault"), nats.ManualAck())
	<-ctx.Done()
}

// runAgent simulates Agent runtime (Web Search, Doc Analysis, etc.).
func runAgent(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *log.Logger) {
	js.Subscribe(bus.SubjectAgentsCreated, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		m.Ack()
		ev := envelope.Build("aegis/agent", "aegis.runtime.completed", map[string]string{
			"agentId": envelope.NewID(), "result": "completed", "status": "ok",
		})
		ev.SetCorrelationID(corr)
		js.Publish(bus.SubjectRuntimeCompleted, ev.MustMarshal())
		logger.Printf("[Agent] received agents.created, published runtime.completed correlation=%s", corr)
	}, nats.Durable("agent-runtime"), nats.ManualAck())
	<-ctx.Done()
}

// runMonitoring subscribes to all streams (FR-DB-005 wildcard per domain).
func runMonitoring(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *log.Logger) {
	var total uint64
	subs := []struct {
		subj string
		dura string
	}{
		{bus.SubjectTasks, "mon-tasks"}, {bus.SubjectUI, "mon-ui"}, {bus.SubjectAgents, "mon-agents"},
		{bus.SubjectRuntime, "mon-runtime"}, {bus.SubjectVault, "mon-vault"}, {bus.SubjectMemory, "mon-memory"},
		{bus.SubjectMonitoring, "mon-mon"},
	}
	for _, s := range subs {
		subj, dura := s.subj, s.dura
		js.Subscribe(subj, func(m *nats.Msg) {
			atomic.AddUint64(&total, 1)
			m.Ack()
		}, nats.Durable(dura), nats.ManualAck())
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logger.Printf("[Monitoring] total_messages=%d", atomic.LoadUint64(&total))
		}
	}
}

func extractCorr(data []byte) string {
	var m map[string]interface{}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	s, _ := m["correlationid"].(string)
	return s
}
