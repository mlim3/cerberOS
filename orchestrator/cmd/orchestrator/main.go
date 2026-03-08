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
// 10. Begin accepting inbound NATS messages
// 11. Block until OS signal (SIGINT/SIGTERM)
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlim3/cerberOS/orchestrator/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("FATAL: failed to load config: %v", err)
	}

	fmt.Printf("Aegis OS — Orchestrator starting | node_id=%s\n", cfg.NodeID)

	// ── Step 1: Memory Interface (M6) ────────────────────────────────────────
	// TODO Phase 1: initialize real MemoryClient (libSQL or Memory Component REST API)
	// TODO Phase 1: run MigrateSchema() — create tables + append-only triggers
	// var memClient interfaces.MemoryClient = memory.New(...)
	// memIface := memoryiface.New(memClient, cfg)
	// if err := memIface.MigrateSchema(); err != nil {
	//     log.Fatalf("FATAL: schema migration failed: %v", err)
	// }

	// ── Step 2: Policy Enforcer (M3) ─────────────────────────────────────────
	// TODO Phase 2: initialize real VaultClient (OpenBao HTTP)
	// var vaultClient interfaces.VaultClient = vault.NewOpenBaoClient(cfg.VaultAddr)
	// policyEnforcer := policy.New(cfg, vaultClient, memClient)

	// ── Step 3: Communications Gateway (M1) ──────────────────────────────────
	// TODO Phase 3: initialize real NATSClient with mTLS
	// var natsClient interfaces.NATSClient = natsimpl.New(cfg.NATSUrl, cfg.NATSCredsPath)
	// gw := gateway.New(natsClient, cfg.NodeID)

	// ── Step 4: Recovery Manager (M5) ────────────────────────────────────────
	// TODO Phase 6: wire Recovery Manager
	// recoverMgr := recovery.New(cfg, memClient, gw, policyEnforcer, nil /* monitor set below */)

	// ── Step 5: Task Monitor (M4) ────────────────────────────────────────────
	// TODO Phase 5: wire Task Monitor
	// taskMonitor := monitor.New(cfg, memClient, recoverMgr)
	// if err := taskMonitor.RehydrateFromMemory(); err != nil {
	//     log.Fatalf("FATAL: startup rehydration failed: %v", err)
	// }

	// ── Step 6: Task Dispatcher (M2) ─────────────────────────────────────────
	// TODO Phase 4: wire Task Dispatcher
	// dispatcher := dispatcher.New(cfg, memClient, vaultClient, gw, policyEnforcer, taskMonitor)

	// ── Step 7: Register Gateway handlers ────────────────────────────────────
	// TODO Phase 3: register handlers on Gateway
	// gw.RegisterTaskHandler(dispatcher.HandleInboundTask)
	// gw.RegisterAgentStatusHandler(taskMonitor.HandleAgentStatusUpdate)
	// gw.RegisterTaskResultHandler(dispatcher.HandleTaskResult)

	// ── Step 8: Health HTTP server ────────────────────────────────────────────
	// TODO Phase 7: start /health endpoint
	// healthHandler := health.New(vaultClient, memClient, natsClient, taskMonitor, cfg.NodeID)
	// go http.ListenAndServe(":8080", http.HandlerFunc(healthHandler.ServeHTTP))

	// ── Step 9: Metrics emitter ───────────────────────────────────────────────
	// TODO Phase 7: start metrics goroutine
	// go emitMetrics(cfg, gw, dispatcher, taskMonitor)

	// ── Step 10: Start accepting messages ─────────────────────────────────────
	// TODO Phase 3: start Gateway
	// if err := gw.Start(); err != nil {
	//     log.Fatalf("FATAL: gateway start failed: %v", err)
	// }

	fmt.Println("Orchestrator ready — waiting for tasks")

	// Block until SIGINT or SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("Orchestrator shutting down gracefully...")
	// TODO: close NATS connection, flush pending writes, etc.
}
