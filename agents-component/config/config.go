package config

import (
	"fmt"
	"os"
)

// Config holds all external endpoint configuration loaded from the environment.
//
// This component communicates exclusively with the Orchestrator via NATS. There
// are no addresses for OpenBao, the Memory Component, or any other peer — all
// cross-component communication is routed through the Orchestrator. No exceptions.
type Config struct {
	// NATSURL is the NATS JetStream endpoint — the sole external transport.
	NATSURL string

	// ComponentID is the identity published in all outbound message envelopes.
	ComponentID string

	// AgentProcessPath is the path to the compiled cmd/agent-process binary.
	// When set, the Lifecycle Manager spawns real agent processes.
	// When empty, an in-process stub is used (suitable for unit testing only).
	AgentProcessPath string
}

// Load reads configuration from environment variables and returns a validated Config.
func Load() (*Config, error) {
	c := &Config{
		NATSURL:          os.Getenv("AEGIS_NATS_URL"),
		ComponentID:      os.Getenv("AEGIS_COMPONENT_ID"),
		AgentProcessPath: os.Getenv("AEGIS_AGENT_PROCESS_PATH"),
	}

	if c.NATSURL == "" {
		return nil, fmt.Errorf("config: AEGIS_NATS_URL is required")
	}
	if c.ComponentID == "" {
		c.ComponentID = "aegis-agents"
	}

	return c, nil
}
