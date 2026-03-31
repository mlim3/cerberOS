package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
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

	// HeartbeatInterval is how often each agent process publishes a heartbeat.
	// Env: AEGIS_HEARTBEAT_INTERVAL (Go duration string, e.g. "5s"). Default: 5s.
	HeartbeatInterval time.Duration

	// HeartbeatMaxMissed is the number of consecutive missed heartbeat intervals
	// before the Lifecycle Manager declares the agent crashed.
	// Env: AEGIS_HEARTBEAT_MAX_MISSED (positive integer). Default: 3.
	HeartbeatMaxMissed int

	// MaxAgentRetries is the maximum number of times a crashed agent may be
	// respawned before it is permanently terminated (EDD §6.3, Step 3).
	// When failure_count >= MaxAgentRetries the agent transitions to TERMINATED
	// instead of being respawned.
	// Env: AEGIS_MAX_AGENT_RETRIES (positive integer). Default: 3.
	MaxAgentRetries int

	// MetricsPort is the TCP port on which the /metrics HTTP endpoint is served.
	// Env: AEGIS_METRICS_PORT (positive integer). Default: 9090.
	MetricsPort int

	// CredAuthMaxAttempts is the number of credential.request publish+await cycles
	// before PreAuthorize gives up and returns VAULT_UNREACHABLE, triggering task.failed.
	// Env: AEGIS_CRED_AUTH_MAX_ATTEMPTS (positive integer). Default: 3.
	CredAuthMaxAttempts int

	// CredAuthTimeout is the per-attempt deadline for receiving credential.response.
	// Env: AEGIS_CRED_AUTH_TIMEOUT (Go duration string, e.g. "5s"). Default: 5s.
	CredAuthTimeout time.Duration

	// CredAuthBaseBackoff is the initial sleep between credential authorize retries.
	// Each subsequent retry doubles the backoff (1s → 2s → 4s, …).
	// Env: AEGIS_CRED_AUTH_BASE_BACKOFF (Go duration string). Default: 1s.
	CredAuthBaseBackoff time.Duration

	// CommsMaxDeliver is the redelivery budget applied to every durable JetStream
	// consumer. After this many delivery attempts the message is dead-lettered:
	// the full original envelope is published to aegis.orchestrator.error with
	// MessageType "dead.letter" so the Orchestrator can detect stalled tasks.
	// Env: AEGIS_COMMS_MAX_DELIVER (positive integer). Default: 5.
	CommsMaxDeliver int
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

	var err error
	if c.HeartbeatInterval, err = parseDuration("AEGIS_HEARTBEAT_INTERVAL", 5*time.Second); err != nil {
		return nil, err
	}
	if c.HeartbeatMaxMissed, err = parseInt("AEGIS_HEARTBEAT_MAX_MISSED", 3, 1); err != nil {
		return nil, err
	}
	if c.MaxAgentRetries, err = parseInt("AEGIS_MAX_AGENT_RETRIES", 3, 1); err != nil {
		return nil, err
	}
	if c.MetricsPort, err = parseInt("AEGIS_METRICS_PORT", 9090, 1); err != nil {
		return nil, err
	}
	if c.CredAuthMaxAttempts, err = parseInt("AEGIS_CRED_AUTH_MAX_ATTEMPTS", 3, 1); err != nil {
		return nil, err
	}
	if c.CredAuthTimeout, err = parseDuration("AEGIS_CRED_AUTH_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if c.CredAuthBaseBackoff, err = parseDuration("AEGIS_CRED_AUTH_BASE_BACKOFF", time.Second); err != nil {
		return nil, err
	}
	if c.CommsMaxDeliver, err = parseInt("AEGIS_COMMS_MAX_DELIVER", 5, 1); err != nil {
		return nil, err
	}

	return c, nil
}

// parseDuration reads a Go duration string from an env var.
// Returns defaultVal if the variable is unset or empty.
func parseDuration(envVar string, defaultVal time.Duration) (time.Duration, error) {
	s := os.Getenv(envVar)
	if s == "" {
		return defaultVal, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("config: %s=%q is not a valid positive duration", envVar, s)
	}
	return d, nil
}

// parseInt reads a positive integer from an env var.
// Returns defaultVal if the variable is unset or empty.
// Returns an error if the parsed value is less than min.
func parseInt(envVar string, defaultVal, min int) (int, error) {
	s := os.Getenv(envVar)
	if s == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min {
		return 0, fmt.Errorf("config: %s=%q must be an integer >= %d", envVar, s, min)
	}
	return n, nil
}
