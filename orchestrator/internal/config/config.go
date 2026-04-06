package config

import (
	"fmt"
	"os"
	"strconv"
)

// VaultFailureMode controls Orchestrator behavior when Vault is unreachable (§FR-PE-04).
type VaultFailureMode string

const (
	VaultFailureModeClosed VaultFailureMode = "FAIL_CLOSED" // Default — deny all tasks
	VaultFailureModeOpen   VaultFailureMode = "FAIL_OPEN"   // Use cached policy if within TTL
)

// OrchestratorConfig holds all configuration injected via environment variables (§16).
// No configuration is hard-coded. Fail loudly at startup if required vars are missing.
type OrchestratorConfig struct {
	// External dependencies
	VaultAddr      string // VAULT_ADDR — OpenBao API endpoint
	NATSUrl        string // NATS_URL — NATS JetStream server URL
	NATSCredsPath  string // NATS_CREDS_PATH — optional path to NATS credentials file
	MemoryEndpoint string // MEMORY_ENDPOINT — Memory Component write/read API

	// Vault behavior
	VaultFailureMode    VaultFailureMode // VAULT_FAILURE_MODE — default: FAIL_CLOSED
	VaultPolicyCacheTTL int              // VAULT_POLICY_CACHE_TTL_SECONDS — default: 60

	// Task decomposition (NEW in v3.0)
	DecompositionTimeoutSeconds int // DECOMPOSITION_TIMEOUT_SECONDS — default: 30
	MaxSubtasksPerPlan          int // MAX_SUBTASKS_PER_PLAN — default: 20
	PlanExecutorMaxParallel     int // PLAN_EXECUTOR_MAX_PARALLEL — default: 5

	// Task lifecycle
	MaxTaskRetries         int // MAX_TASK_RETRIES — default: 3
	TaskDedupWindowSeconds int // TASK_DEDUP_WINDOW_SECONDS — default: 300

	// Health & metrics
	HealthCheckIntervalSeconds int // HEALTH_CHECK_INTERVAL_SECONDS — default: 10
	MetricsEmitIntervalSeconds int // METRICS_EMIT_INTERVAL_SECONDS — default: 15

	// Queue
	QueueHighWaterMark int // QUEUE_HIGH_WATER_MARK — default: 500

	// Memory resilience
	MemoryWriteBufferSeconds int // MEMORY_WRITE_BUFFER_SECONDS — default: 30

	// IO Component integration
	IOAPIBase string // IO_API_BASE — IO component HTTP base URL (e.g. http://localhost:3001); optional

	// Identity
	NodeID string // NODE_ID — default: os.Hostname()
}

// Load reads all environment variables and returns a validated OrchestratorConfig.
// Returns an error if any required variable is missing or invalid.
func Load() (*OrchestratorConfig, error) {
	cfg := &OrchestratorConfig{}
	var missing []string

	// ── Required fields ──────────────────────────────────────────────────────
	cfg.VaultAddr = os.Getenv("VAULT_ADDR")
	if cfg.VaultAddr == "" {
		missing = append(missing, "VAULT_ADDR")
	}

	cfg.NATSUrl = os.Getenv("NATS_URL")
	if cfg.NATSUrl == "" {
		missing = append(missing, "NATS_URL")
	}

	cfg.NATSCredsPath = os.Getenv("NATS_CREDS_PATH")

	cfg.MemoryEndpoint = os.Getenv("MEMORY_ENDPOINT")
	if cfg.MemoryEndpoint == "" {
		missing = append(missing, "MEMORY_ENDPOINT")
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %v", missing)
	}

	// ── Vault behavior ───────────────────────────────────────────────────────
	switch VaultFailureMode(os.Getenv("VAULT_FAILURE_MODE")) {
	case VaultFailureModeOpen:
		cfg.VaultFailureMode = VaultFailureModeOpen
	default:
		cfg.VaultFailureMode = VaultFailureModeClosed // Default: FAIL_CLOSED
	}

	cfg.VaultPolicyCacheTTL = envInt("VAULT_POLICY_CACHE_TTL_SECONDS", 60)

	// ── Task decomposition (NEW in v3.0) ─────────────────────────────────────
	cfg.DecompositionTimeoutSeconds = envInt("DECOMPOSITION_TIMEOUT_SECONDS", 30)
	cfg.MaxSubtasksPerPlan = envInt("MAX_SUBTASKS_PER_PLAN", 20)
	cfg.PlanExecutorMaxParallel = envInt("PLAN_EXECUTOR_MAX_PARALLEL", 5)

	// ── Task lifecycle ───────────────────────────────────────────────────────
	cfg.MaxTaskRetries = envInt("MAX_TASK_RETRIES", 3)
	cfg.TaskDedupWindowSeconds = envInt("TASK_DEDUP_WINDOW_SECONDS", 300)

	// ── Health & metrics ─────────────────────────────────────────────────────
	cfg.HealthCheckIntervalSeconds = envInt("HEALTH_CHECK_INTERVAL_SECONDS", 10)
	cfg.MetricsEmitIntervalSeconds = envInt("METRICS_EMIT_INTERVAL_SECONDS", 15)

	// ── Queue ────────────────────────────────────────────────────────────────
	cfg.QueueHighWaterMark = envInt("QUEUE_HIGH_WATER_MARK", 500)

	// ── Memory resilience ────────────────────────────────────────────────────
	cfg.MemoryWriteBufferSeconds = envInt("MEMORY_WRITE_BUFFER_SECONDS", 30)

	// ── IO Component integration ─────────────────────────────────────────────
	cfg.IOAPIBase = os.Getenv("IO_API_BASE") // Optional — empty disables IO push

	// ── Identity ─────────────────────────────────────────────────────────────
	cfg.NodeID = os.Getenv("NODE_ID")
	if cfg.NodeID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			cfg.NodeID = "unknown-node"
		} else {
			cfg.NodeID = hostname
		}
	}

	return cfg, nil
}

// envInt reads an integer environment variable with a fallback default.
func envInt(key string, defaultVal int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultVal
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return defaultVal
	}
	return val
}
