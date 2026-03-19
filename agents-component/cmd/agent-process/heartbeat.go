package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/pkg/types"
	"github.com/nats-io/nats.go"
)

// startHeartbeat connects to NATS and publishes a HeartbeatEvent on
// aegis.heartbeat.<agent_id> at each tick until ctx is cancelled.
//
// Required environment variables:
//
//	AEGIS_NATS_URL     — NATS server to connect to (injected by Lifecycle Manager)
//	AEGIS_AGENT_ID     — this agent's identity (injected by Lifecycle Manager)
//
// Optional environment variables:
//
//	AEGIS_HEARTBEAT_INTERVAL — Go duration string (e.g. "5s"); default 5s
//
// startHeartbeat returns immediately without error if either required variable is
// absent so that the agent process can still run in environments where heartbeat
// monitoring is not configured.
func startHeartbeat(ctx context.Context, log *slog.Logger, taskID, traceID string) {
	natsURL := os.Getenv("AEGIS_NATS_URL")
	agentID := os.Getenv("AEGIS_AGENT_ID")
	if natsURL == "" || agentID == "" {
		log.Warn("heartbeat disabled: AEGIS_NATS_URL or AEGIS_AGENT_ID not set")
		return
	}

	interval := 5 * time.Second
	if s := os.Getenv("AEGIS_HEARTBEAT_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			interval = d
		}
	}

	nc, err := nats.Connect(natsURL, nats.Name("aegis-agent-process-"+agentID))
	if err != nil {
		log.Warn("heartbeat: NATS connect failed — heartbeat disabled", "error", err)
		return
	}

	subject := comms.HeartbeatSubject(agentID)

	go func() {
		defer nc.Close()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		log.Info("heartbeat started", "interval", interval, "subject", subject)

		for {
			select {
			case <-ctx.Done():
				log.Info("heartbeat stopped")
				return
			case t := <-ticker.C:
				event := types.HeartbeatEvent{
					AgentID:   agentID,
					TaskID:    taskID,
					TraceID:   traceID,
					Timestamp: t.UTC(),
				}
				data, err := json.Marshal(event)
				if err != nil {
					log.Warn("heartbeat: marshal failed", "error", err)
					continue
				}
				if err := nc.Publish(subject, data); err != nil {
					log.Warn("heartbeat: publish failed", "error", err)
				}
			}
		}
	}()
}
