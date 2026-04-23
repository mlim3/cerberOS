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

	// Multi-step prompting / plan confirmation (NEW in milestone 3).
	// PLAN_APPROVAL_MODE: off | multi (default) | always
	//   off    — execute every plan without asking the user.
	//   multi  — ask the user only for plans with >1 subtask.
	//   always — ask for every plan.
	PlanApprovalMode           string
	PlanApprovalTimeoutSeconds int // PLAN_APPROVAL_TIMEOUT_SECONDS — default: 300

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

	// Observability (§16)
	LogLevel     string // LOG_LEVEL — default: "info"
	LogFormat    string // LOG_FORMAT — default: "json"
	OTELEndpoint string // OTEL_EXPORTER_OTLP_ENDPOINT — default: "tempo:4317"
	LokiURL      string // LOKI_URL — default: "http://loki:3100"

	// Identity
	NodeID string // NODE_ID — default: os.Hostname()

	// Cron wake — POST /v1/cron/wake wakes the planner with a maintenance task (optional).
	CronWakeSecret         string // CRON_WAKE_SECRET — empty disables the endpoint
	CronWakeSystemPrompt   string // CRON_WAKE_SYSTEM_PROMPT — extra planner directives
	CronWakeRawInput       string // CRON_WAKE_RAW_INPUT — maintenance work description
	CronWakeUserID         string // CRON_WAKE_USER_ID — default: system
	CronWakeCallbackTopic  string // CRON_WAKE_CALLBACK_TOPIC — NATS topic for results
	CronWakeTimeoutSeconds int    // CRON_WAKE_TIMEOUT_SECONDS — default: 3600
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

	switch os.Getenv("PLAN_APPROVAL_MODE") {
	case "off", "always", "multi":
		cfg.PlanApprovalMode = os.Getenv("PLAN_APPROVAL_MODE")
	default:
		cfg.PlanApprovalMode = "multi"
	}
	cfg.PlanApprovalTimeoutSeconds = envInt("PLAN_APPROVAL_TIMEOUT_SECONDS", 300)

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

	// ── Observability ────────────────────────────────────────────────────────
	cfg.LogLevel = envString("LOG_LEVEL", "info")
	cfg.LogFormat = envString("LOG_FORMAT", "json")
	cfg.OTELEndpoint = envString("OTEL_EXPORTER_OTLP_ENDPOINT", "tempo:4317")
	cfg.LokiURL = envString("LOKI_URL", "http://loki:3100")

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

	// ── Cron wake (optional) ─────────────────────────────────────────────────
	cfg.CronWakeSecret = os.Getenv("CRON_WAKE_SECRET")
	cfg.CronWakeSystemPrompt = os.Getenv("CRON_WAKE_SYSTEM_PROMPT")
	cfg.CronWakeRawInput = os.Getenv("CRON_WAKE_RAW_INPUT")
	cfg.CronWakeUserID = envString("CRON_WAKE_USER_ID", "system")
	cfg.CronWakeCallbackTopic = envString("CRON_WAKE_CALLBACK_TOPIC", "aegis.orchestrator.cron.wake.results")
	cfg.CronWakeTimeoutSeconds = envInt("CRON_WAKE_TIMEOUT_SECONDS", 3600)

	return cfg, nil
}

// envString reads a string environment variable with a fallback default.
func envString(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
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
