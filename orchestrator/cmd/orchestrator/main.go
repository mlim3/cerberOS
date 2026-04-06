// main is the Orchestrator entrypoint.
//
// Startup sequence:
//  1. Load configuration from environment variables
//  2. Connect Memory Interface (M6) + run schema migrations
//  3. Connect Policy Enforcer (M3)
//  4. Connect Communications Gateway (M1)
//  5. Wire Recovery Manager (M5)
//  6. Wire Task Monitor (M4) + rehydrate active tasks
//  7. Wire Task Dispatcher (M2)
//  8. Start health HTTP server
//  9. Start metrics emitter goroutine
//
// 10. Begin accepting inbound NATS messages
// 11. Block until OS signal (SIGINT/SIGTERM)
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
	"github.com/mlim3/cerberOS/orchestrator/internal/dispatcher"
	"github.com/mlim3/cerberOS/orchestrator/internal/executor"
	"github.com/mlim3/cerberOS/orchestrator/internal/gateway"
	"github.com/mlim3/cerberOS/orchestrator/internal/health"
	ioclient "github.com/mlim3/cerberOS/orchestrator/internal/io"
	memoryiface "github.com/mlim3/cerberOS/orchestrator/internal/memory"
	"github.com/mlim3/cerberOS/orchestrator/internal/mocks"
	"github.com/mlim3/cerberOS/orchestrator/internal/monitor"
	"github.com/mlim3/cerberOS/orchestrator/internal/policy"
	"github.com/mlim3/cerberOS/orchestrator/internal/recovery"
	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		cfg = demoConfig()
		log.Printf("starting in demo mode with in-memory mocks: %v", err)
	}

	fmt.Printf("Aegis OS — Orchestrator starting | node_id=%s\n", cfg.NodeID)

	rt, err := buildRuntime(cfg)
	if err != nil {
		log.Fatalf("FATAL: build runtime failed: %v", err)
	}

	rt.health.StartMonitorLoop(cfg.HealthCheckIntervalSeconds, cfg.HealthCheckIntervalSeconds)
	go func() {
		addr := ":8080"
		log.Printf("health endpoint listening on %s", addr)
		if err := http.ListenAndServe(addr, http.HandlerFunc(rt.health.ServeHTTP)); err != nil {
			log.Printf("health server stopped: %v", err)
		}
	}()

	go emitMetrics(cfg, rt.gateway, rt.dispatcher, rt.health)

	if err := rt.gateway.Start(); err != nil {
		log.Fatalf("FATAL: gateway start failed: %v", err)
	}

	fmt.Println("Orchestrator ready — waiting for tasks")

	// Block until SIGINT or SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("Orchestrator shutting down gracefully...")
	rt.nats.Close()
}

type runtime struct {
	memory     *memoryiface.Interface
	vault      *mocks.VaultMock
	nats       *mocks.NATSMock
	gateway    *gateway.Gateway
	monitor    *monitor.Monitor
	recovery   *recovery.Manager
	dispatcher *dispatcher.Dispatcher
	executor   *executor.PlanExecutor
	health     *health.Handler
}

func buildRuntime(cfg *config.OrchestratorConfig) (*runtime, error) {
	mockMemory := mocks.NewMemoryMock()
	memClient := memoryiface.New(mockMemory, cfg)
	if err := memClient.MigrateSchema(); err != nil {
		return nil, err
	}

	vaultClient := &mocks.VaultMock{}
	policyEnforcer := policy.New(cfg, vaultClient, memClient, nil)

	natsClient := mocks.NewNATSMock()
	gw := gateway.New(natsClient, cfg.NodeID)

	recoveryBridge := &recoveryProxy{}
	taskMonitor := monitor.New(cfg, memClient, recoveryBridge)

	var taskDispatcher *dispatcher.Dispatcher
	planExecutor := executor.New(
		cfg,
		memClient,
		gw,
		policyEnforcer,
		func(ts *types.TaskState, aggregatedResults []types.PriorResult) {
			if taskDispatcher != nil {
				taskDispatcher.HandlePlanComplete(ts, aggregatedResults)
			}
		},
		func(ts *types.TaskState, errorCode string, partial bool, partialResults []types.PriorResult) {
			if taskDispatcher != nil {
				taskDispatcher.HandlePlanFailed(ts, errorCode, partial, partialResults)
			}
		},
	)

	recoverMgr := recovery.New(cfg, memClient, gw, policyEnforcer, taskMonitor)
	recoveryBridge.target = recoverMgr
	memClient.SetWriteFailureHandler(recoverMgr.HandleComponentFailure)

	if err := taskMonitor.RehydrateFromMemory(); err != nil {
		return nil, err
	}

	// IO Component client — optional; no-op when IO_API_BASE is not set.
	ioClient := ioclient.New(cfg.IOAPIBase)
	if !ioClient.Disabled() {
		log.Printf("IO Component integration enabled: %s", cfg.IOAPIBase)
	}

	taskDispatcher = dispatcher.New(cfg, memClient, vaultClient, gw, policyEnforcer, taskMonitor, planExecutor, ioClient)

	gw.RegisterTaskHandler(taskDispatcher.HandleInboundTask)
	gw.RegisterAgentStatusHandler(taskMonitor.HandleAgentStatusUpdate)
	gw.RegisterTaskResultHandler(taskDispatcher.HandleTaskResult)

	// Forward agent user_input credential requests to the IO Component.
	gw.RegisterCredentialRequestHandler(func(agentID, taskID, requestID, keyName, label string) error {
		return ioClient.PushCredentialRequest(ioclient.CredentialRequestPayload{
			TaskID:    taskID,
			RequestID: requestID,
			KeyName:   keyName,
			Label:     label,
		})
	})

	healthHandler := health.New(vaultClient, memClient, natsClient, taskMonitor, cfg.NodeID)

	return &runtime{
		memory:     memClient,
		vault:      vaultClient,
		nats:       natsClient,
		gateway:    gw,
		monitor:    taskMonitor,
		recovery:   recoverMgr,
		dispatcher: taskDispatcher,
		executor:   planExecutor,
		health:     healthHandler,
	}, nil
}

type recoveryProxy struct {
	target *recovery.Manager
}

func (p *recoveryProxy) HandleRecovery(ts *types.TaskState, reason types.RecoveryReason) {
	if p.target != nil {
		p.target.HandleRecovery(ts, reason)
	}
}

func emitMetrics(cfg *config.OrchestratorConfig, gw *gateway.Gateway, d *dispatcher.Dispatcher, h *health.Handler) {
	interval := time.Duration(cfg.MetricsEmitIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		received, completed, failed, violations, _, queueDepth := d.GetMetrics()
		h.SetQueueDepth(queueDepth)
		metrics := types.MetricsPayload{
			NodeID:                cfg.NodeID,
			Timestamp:             time.Now().UTC(),
			TasksReceivedTotal:    received,
			TasksCompletedTotal:   completed,
			TasksFailedTotal:      failed,
			PolicyViolationsTotal: violations,
			ActiveTasks:           int64(len(d.GetActiveTasks())),
			VaultAvailable:        boolToInt(true),
			QueueDepth:            queueDepth,
		}
		if err := gw.PublishMetrics(metrics); err != nil {
			log.Printf("metrics publish failed: %v", err)
		}
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func demoConfig() *config.OrchestratorConfig {
	return &config.OrchestratorConfig{
		VaultAddr:                   "mock://vault",
		NATSUrl:                     "mock://nats",
		NATSCredsPath:               "mock://creds",
		MemoryEndpoint:              "mock://memory",
		VaultFailureMode:            config.VaultFailureModeClosed,
		VaultPolicyCacheTTL:         60,
		DecompositionTimeoutSeconds: 30,
		MaxSubtasksPerPlan:          20,
		PlanExecutorMaxParallel:     5,
		MaxTaskRetries:              3,
		TaskDedupWindowSeconds:      300,
		HealthCheckIntervalSeconds:  10,
		MetricsEmitIntervalSeconds:  15,
		QueueHighWaterMark:          500,
		MemoryWriteBufferSeconds:    30,
		NodeID:                      "demo-node",
	}
}
