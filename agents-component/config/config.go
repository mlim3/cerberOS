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

	// IdleSuspendTimeout is how long an agent may remain in the IDLE state before
	// the idle sweep transitions it to SUSPENDED, freeing VM resources while
	// preserving the agent's registry entry for future reuse (OQ-03).
	//
	// When a task arrives for a SUSPENDED agent the factory issues a fresh
	// credential.authorize and spawns a new microVM (see SuspendWakeLatencyTarget).
	//
	// Set to 0 (default) to disable auto-suspension: all agents are TERMINATED
	// immediately after task completion (current behaviour).
	//
	// Env: AEGIS_IDLE_SUSPEND_TIMEOUT (Go duration, e.g. "5m"). Default: 0 (disabled).
	IdleSuspendTimeout time.Duration

	// SkillsConfigPath is the path to a YAML or JSON skill definitions file.
	// When set, it replaces the embedded default_skills.yaml for both M4
	// registration (cmd/aegis-agents) and tool dispatch (cmd/agent-process).
	// The file must follow the schema defined in internal/skillsconfig.
	// Env: AEGIS_SKILLS_CONFIG_PATH (file path). Default: "" (use embedded default).
	SkillsConfigPath string

	// EmbeddingModel is the Voyage AI model used for semantic skill-search
	// embeddings. A small, fast model is preferred because skill descriptions
	// are short technical strings (≤300 chars).
	// Env: AEGIS_EMBEDDING_MODEL. Default: "voyage-3-lite".
	EmbeddingModel string

	// SuspendWakeLatencyTarget is the expected latency budget for waking a SUSPENDED
	// agent — from task.inbound receipt to the agent process being ACTIVE (OQ-06).
	// This budget covers credential.authorize round-trip + VM spawn + process startup.
	//
	// This value is informational: it is logged at startup so the Platform team can
	// verify that the measured wake latency stays within the agreed SLA. It does NOT
	// gate or throttle any runtime behaviour; the Orchestrator is responsible for
	// routing latency-sensitive tasks away from SUSPENDED agents when needed.
	//
	// Baseline (M2 process manager, no Firecracker): ~2 s.
	// Target with Firecracker snapshot restore (M3): < 500 ms.
	//
	// Env: AEGIS_SUSPEND_WAKE_LATENCY_TARGET (Go duration, e.g. "2s"). Default: 2s.
	SuspendWakeLatencyTarget time.Duration
}

// Load reads configuration from environment variables and returns a validated Config.
func Load() (*Config, error) {
	embeddingModel := os.Getenv("AEGIS_EMBEDDING_MODEL")
	if embeddingModel == "" {
		embeddingModel = "voyage-3-lite"
	}
	c := &Config{
		NATSURL:          os.Getenv("AEGIS_NATS_URL"),
		ComponentID:      os.Getenv("AEGIS_COMPONENT_ID"),
		AgentProcessPath: os.Getenv("AEGIS_AGENT_PROCESS_PATH"),
		SkillsConfigPath: os.Getenv("AEGIS_SKILLS_CONFIG_PATH"),
		EmbeddingModel:   embeddingModel,
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
	// IdleSuspendTimeout: 0 means disabled (no auto-suspension); valid positive
	// durations enable OQ-03. parseDuration returns 0 for unset/empty.
	if raw := os.Getenv("AEGIS_IDLE_SUSPEND_TIMEOUT"); raw != "" {
		if c.IdleSuspendTimeout, err = parseDuration("AEGIS_IDLE_SUSPEND_TIMEOUT", 0); err != nil {
			return nil, err
		}
	}
	if c.SuspendWakeLatencyTarget, err = parseDuration("AEGIS_SUSPEND_WAKE_LATENCY_TARGET", 2*time.Second); err != nil {
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
