// DEMO: Simulates all 6 components interacting with the Data Bus per EDD.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"aegis-databus/pkg/bus"
	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/security"
	"github.com/nats-io/nats.go"
)

// runIO simulates UI Layer: publishes user actions, subscribes to task updates.
// comp is the ACL component id (e.g. io, orchestrator) per EDD §9.2.
func runIO(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *slog.Logger, comp string) {
	// Subscribe to task updates (FR-DB-001, FR-DB-005 wildcard) — SubscribeWithACL enforces SR-DB-006
	if _, err := bus.SubscribeWithACL(js, comp, bus.SubjectTasks, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		taskID := extractTaskID(m.Data)
		logger.Info("message received", "task_id", taskID, "subject", m.Subject, "size_bytes", len(m.Data), "request_id", corr)
		m.Ack()
	}, nats.Durable("io-tasks"), nats.ManualAck()); err != nil {
		logger.Error("subscribe failed", "subject", bus.SubjectTasks, "error", err)
		return
	}
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
				logger.Error("validation failed", "task_id", taskIDFromEvent(ev), "error", err)
				continue
			}
			if _, err := bus.PublishWithACL(js, comp, bus.SubjectUIAction, ev.MustMarshal()); err != nil {
				logger.Error("publish failed", "task_id", taskIDFromEvent(ev), "subject", bus.SubjectUIAction, "error", err)
				continue
			}
			logger.Info("message published", "task_id", taskIDFromEvent(ev), "subject", bus.SubjectUIAction, "request_id", ev.CorrelationID)
		}
	}
}

// runOrchestrator simulates Task Router + Task Planner + Agent Manager.
func runOrchestrator(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *slog.Logger, comp string) {
	var planCorr string

	// Subscribe to UI actions
	bus.SubscribeWithACL(js, comp, bus.SubjectUIAction, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		taskID := extractTaskID(m.Data)
		logger.Info("ui action received", "task_id", taskID, "request_id", corr)
		m.Ack()
		planCorr = corr

		// FR-DB-002: Request-reply — Task Router calls Personalization before routing
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		reqPayload := []byte(`{"userId":"demo-user"}`)
		reply, err := bus.Request(reqCtx, nc, bus.SubjectPersonalization, reqPayload, 5*time.Second)
		cancel()
		if err != nil {
			logger.Error("personalization request failed", "task_id", taskID, "error", err)
		} else {
			logger.Info("personalization request completed", "task_id", taskID, "reply_bytes", len(reply))
		}
		_ = reply

		// Publish tasks.routed
		ev := envelope.Build("aegis/orchestrator", "aegis.tasks.routed", map[string]string{
			"taskId": envelope.NewID(), "userId": "demo-user", "complexity": "high",
		})
		ev.SetCorrelationID(corr)
		bus.PublishWithACL(js, comp, bus.SubjectTasksRouted, ev.MustMarshal())
		logger.Info("tasks routed", "task_id", taskIDFromEvent(ev), "subject", bus.SubjectTasksRouted, "request_id", corr)
	}, nats.Durable("orch-ui"), nats.ManualAck())

	// Subscribe to plan (self-loop for demo - in real system Task Planner publishes)
	bus.SubscribeWithACL(js, comp, bus.SubjectTasksRouted, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		m.Ack()
		ev := envelope.Build("aegis/orchestrator", "aegis.tasks.plan_created", map[string]interface{}{
			"subtasks": 3, "planId": envelope.NewID(),
		})
		ev.SetCorrelationID(corr)
		bus.PublishWithACL(js, comp, bus.SubjectTasksPlanCreated, ev.MustMarshal())
		logger.Info("plan created", "subject", bus.SubjectTasksPlanCreated, "subtasks", 3, "request_id", corr)
	}, nats.Durable("orch-router"), nats.ManualAck())

	// Subscribe to plan_created, publish agents.created (queue group - FR-DB-004)
	bus.QueueSubscribeWithACL(js, comp, bus.SubjectTasksPlanCreated, "agent-managers", func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		m.Ack()
		for i := 0; i < 3; i++ {
			ev := envelope.Build("aegis/orchestrator", "aegis.agents.created", map[string]string{
				"agentId": envelope.NewID(), "taskId": planCorr,
			})
			ev.SetCorrelationID(corr)
			bus.PublishWithACL(js, comp, bus.SubjectAgentsCreated, ev.MustMarshal())
		}
		logger.Info("agents created", "task_id", planCorr, "subject", bus.SubjectAgentsCreated, "count", 3, "request_id", corr)
	}, nats.Durable("orch-agents"), nats.ManualAck())

	<-ctx.Done()
	_ = planCorr
}

// runMemory simulates Memory & Context Manager.
func runMemory(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *slog.Logger, comp string) {
	// FR-DB-002: Request-reply — Personalization responder (ACL enforced)
	bus.SubscribeRequestReplyWithACL(ctx, nc, comp, bus.SubjectPersonalization, func(subject string, request []byte) ([]byte, error) {
		_ = subject
		_ = request
		logger.Info("personalization request received")
		return json.Marshal(map[string]string{"userId": "demo-user", "preferences": "{}"})
	})
	time.Sleep(50 * time.Millisecond)

	// Subscribe to agents.created, publish memory.saved
	bus.SubscribeWithACL(js, comp, bus.SubjectAgentsCreated, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		taskID := extractTaskID(m.Data)
		m.Ack()
		ev := envelope.Build("aegis/memory", "aegis.memory.saved", map[string]string{
			"agentId": envelope.NewID(),
		})
		ev.SetCorrelationID(corr)
		bus.PublishWithACL(js, comp, bus.SubjectMemorySaved, ev.MustMarshal())
		logger.Info("memory saved", "task_id", taskID, "subject", bus.SubjectMemorySaved, "request_id", corr)
	}, nats.Durable("memory-agents"), nats.ManualAck())

	<-ctx.Done()
}

// runVault simulates Permission Manager / Vault.
func runVault(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *slog.Logger, comp string) {
	bus.SubscribeWithACL(js, comp, bus.SubjectVault, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		taskID := extractTaskID(m.Data)
		logger.Info("vault message received", "task_id", taskID, "subject", m.Subject, "size_bytes", len(m.Data), "request_id", corr)
		m.Ack()
	}, nats.Durable("vault"), nats.ManualAck())
	<-ctx.Done()
}

// runAgent simulates Agent runtime (Web Search, Doc Analysis, etc.).
func runAgent(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *slog.Logger, comp string) {
	// EDD §9.2 / M3: sensitive subjects — Agents consumer group only (queue aegis-agents).
	if _, err := bus.QueueSubscribeWithACL(js, comp, bus.SubjectAgentsVaultExecuteResult, security.QueueAgentsComponent, func(m *nats.Msg) {
		m.Ack()
		logger.Info("vault execute result received", "subject", m.Subject)
	}, nats.Durable("agent-m3-vault-exec"), nats.ManualAck()); err != nil {
		logger.Error("vault result queue subscribe failed", "error", err)
	}
	if _, err := bus.QueueSubscribeWithACL(js, comp, bus.SubjectAgentsCredentialResponse, security.QueueAgentsComponent, func(m *nats.Msg) {
		m.Ack()
		logger.Info("credential response received", "subject", m.Subject)
	}, nats.Durable("agent-m3-cred"), nats.ManualAck()); err != nil {
		logger.Error("credential response queue subscribe failed", "error", err)
	}

	bus.SubscribeWithACL(js, comp, bus.SubjectAgentsCreated, func(m *nats.Msg) {
		corr := extractCorr(m.Data)
		taskID := extractTaskID(m.Data)
		m.Ack()
		ev := envelope.Build("aegis/agent", "aegis.runtime.completed", map[string]string{
			"agentId": envelope.NewID(), "result": "completed", "status": "ok",
		})
		ev.SetCorrelationID(corr)
		bus.PublishWithACL(js, comp, bus.SubjectRuntimeCompleted, ev.MustMarshal())
		logger.Info("runtime completed", "task_id", taskID, "subject", bus.SubjectRuntimeCompleted, "request_id", corr)
	}, nats.Durable("agent-runtime"), nats.ManualAck())
	<-ctx.Done()
}

// runMonitoring subscribes to all streams (FR-DB-005 wildcard per domain).
// Uses SubjectAgentsLeafWildcard (aegis.agents.*) so M3 nested subjects are not observed here.
func runMonitoring(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext, logger *slog.Logger, comp string) {
	var total uint64
	subs := []struct {
		subj string
		dura string
	}{
		{bus.SubjectTasks, "mon-tasks"}, {bus.SubjectUI, "mon-ui"}, {bus.SubjectAgentsLeafWildcard, "mon-agents"},
		{bus.SubjectRuntime, "mon-runtime"}, {bus.SubjectVault, "mon-vault"}, {bus.SubjectMemory, "mon-memory"},
		{bus.SubjectMonitoring, "mon-mon"},
	}
	for _, s := range subs {
		subj, dura := s.subj, s.dura
		bus.SubscribeWithACL(js, comp, subj, func(m *nats.Msg) {
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
			logger.Info("monitoring summary", "total_messages", atomic.LoadUint64(&total))
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

func extractTaskID(data []byte) string {
	var m map[string]interface{}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	payload, _ := m["data"].(map[string]interface{})
	if payload == nil {
		return ""
	}
	taskID, _ := payload["taskId"].(string)
	return taskID
}

func taskIDFromEvent(ev envelope.CloudEvent) string {
	payload, _ := ev.Data.(map[string]string)
	if payload == nil {
		return ""
	}
	return payload["taskId"]
}
