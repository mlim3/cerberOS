// Package factory is M2 — the Agent Factory. It is the central coordinator that
// receives TaskSpecs, queries the Registry for an existing capable agent, and
// initiates provisioning when no match is found. It wires together all other
// modules and is the only module that orchestrates across them.
package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cerberOS/agents-component/internal/audit"
	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/cerberOS/agents-component/internal/credentials"
	"github.com/cerberOS/agents-component/internal/lifecycle"
	"github.com/cerberOS/agents-component/internal/memory"
	"github.com/cerberOS/agents-component/internal/registry"
	"github.com/cerberOS/agents-component/internal/skills"
	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	// spawnContextBudget is the maximum number of tokens allowed in the initial
	// agent context at provisioning time (EDD §13.3, PRD FR-FAC-05).
	spawnContextBudget = 2048

	// permTokenPlaceholder is a fixed-size substitute for the opaque permission
	// token when counting spawn-context tokens before pre-authorization completes.
	// It approximates the byte length of a typical Vault-issued token reference.
	permTokenPlaceholder = "00000000-0000-0000-0000-000000000000"
)

// TokenCounter reports the token count of a text string.
// It is used at provisioning time to enforce the spawn context budget (EDD §13.3).
// Implementations may call the Anthropic SDK CountTokens API or use a local
// approximation. Only internal/comms may open network connections; callers that
// require API-backed counting must inject an implementation via Config.TokenCounter.
type TokenCounter interface {
	CountTokens(text string) (int, error)
}

// IDGenerator is a function that produces a unique agent ID.
type IDGenerator func() string

// Factory coordinates agent provisioning and task dispatch.
type Factory struct {
	registry      registry.Registry
	skills        skills.Manager
	credentials   credentials.Broker
	lifecycle     lifecycle.Manager
	memory        memory.Client
	comms         comms.Client
	log           *slog.Logger
	emitter       *audit.Emitter
	generateID    IDGenerator
	crashDetector *lifecycle.CrashDetector
	maxRetries    int
	tokenCounter  TokenCounter      // optional; when nil, spawn context budget is not enforced
	policy        *PermissionPolicy // optional; when nil, falls back to legacy domain.credential stub

	// inFlightMu guards inFlightRequests.
	inFlightMu       sync.Mutex
	inFlightRequests map[string][]string // agentID → []requestID with no result yet

	// idleSuspendTimeout is the duration an agent may stay IDLE before the idle
	// sweep transitions it to SUSPENDED (OQ-03). 0 = disabled (immediate teardown).
	idleSuspendTimeout time.Duration

	// reuseEnabled is the computed gate for live-process reuse: true when
	// IdleSuspendTimeout > 0 AND the configured lifecycle Manager reports
	// SupportsReuse() = true. When enabled, CompleteTask leaves the agent
	// process alive and the Priority 1 IDLE reuse path in HandleTaskSpec
	// delivers a fresh SpawnContext (Vault re-auth + manifest + memory) to the
	// running process instead of spawning a new VM. When disabled, behaviour
	// matches the historical provision-per-task flow.
	reuseEnabled bool

	// suspendWakeLatencyTarget is the documented SLA budget for SUSPENDED → ACTIVE.
	// Informational only — logged at startup and on each wake for observability (OQ-06).
	suspendWakeLatencyTarget time.Duration

	// Lifecycle hooks (optional; nil-safe).
	onSpawn     func(agentID string)
	onTerminate func(agentID string)
	onRecover   func(agentID string)
	onSuspend   func(agentID string) // called after each successful IDLE → SUSPENDED transition
	onWake      func(agentID string) // called after each successful SUSPENDED → ACTIVE wake
}

// Config carries the dependencies required to construct a Factory.
type Config struct {
	Registry      registry.Registry
	Skills        skills.Manager
	Credentials   credentials.Broker
	Lifecycle     lifecycle.Manager
	Memory        memory.Client
	Comms         comms.Client
	Log           *slog.Logger             // optional; if nil, slog.Default() is used
	GenerateID    IDGenerator              // optional; defaults to timestamp-based ID
	CrashDetector *lifecycle.CrashDetector // optional; when set, Watch/Unwatch are called around spawn/terminate
	MaxRetries    int                      // optional; max crash respawns before permanent termination. Default: 3.
	TokenCounter  TokenCounter             // optional; when set, enforces the 2,048-token spawn context budget (EDD §13.3)

	// Policy is the permission policy that maps skill domains to permitted Vault
	// operation types. When set, PermissionsFor is called at provision time and
	// an unknown domain causes spawn to fail (fail-secure). When nil, the legacy
	// domain.credential stub is used — suitable for local dev and unit tests.
	Policy *PermissionPolicy

	// IdleSuspendTimeout is the duration an agent may remain IDLE before being
	// auto-suspended by the idle sweep (OQ-03). 0 disables auto-suspension and
	// preserves the current behaviour: agents are TERMINATED on task completion.
	IdleSuspendTimeout time.Duration

	// SuspendWakeLatencyTarget is the SLA budget for waking a SUSPENDED agent
	// (SUSPENDED → ACTIVE). Informational only — logged on startup and on wake
	// for Platform team observability (OQ-06).
	SuspendWakeLatencyTarget time.Duration

	// Lifecycle event hooks — all optional. Called synchronously on the
	// provisioning path; implementations must be non-blocking.
	OnSpawn     func(agentID string) // called after each successful initial agent spawn
	OnTerminate func(agentID string) // called after each successful agent termination
	OnRecover   func(agentID string) // called after each successful crash-recovery respawn
	OnSuspend   func(agentID string) // called after each successful IDLE → SUSPENDED transition
	OnWake      func(agentID string) // called after each successful SUSPENDED → ACTIVE wake
}

// New returns a Factory wired with the provided dependencies.
func New(cfg Config) (*Factory, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("factory: Registry is required")
	}
	if cfg.Skills == nil {
		return nil, fmt.Errorf("factory: Skills is required")
	}
	if cfg.Credentials == nil {
		return nil, fmt.Errorf("factory: Credentials is required")
	}
	if cfg.Lifecycle == nil {
		return nil, fmt.Errorf("factory: Lifecycle is required")
	}
	if cfg.Memory == nil {
		return nil, fmt.Errorf("factory: Memory is required")
	}
	if cfg.Comms == nil {
		return nil, fmt.Errorf("factory: Comms is required")
	}
	if cfg.GenerateID == nil {
		cfg.GenerateID = defaultIDGenerator
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	reuseEnabled := cfg.IdleSuspendTimeout > 0 && cfg.Lifecycle.SupportsReuse()
	if reuseEnabled {
		log.Info("live-process reuse enabled",
			"idle_suspend_timeout", cfg.IdleSuspendTimeout,
		)
	}
	return &Factory{
		registry:                 cfg.Registry,
		skills:                   cfg.Skills,
		credentials:              cfg.Credentials,
		lifecycle:                cfg.Lifecycle,
		memory:                   cfg.Memory,
		comms:                    cfg.Comms,
		log:                      log,
		emitter:                  audit.New(cfg.Comms, log),
		generateID:               cfg.GenerateID,
		crashDetector:            cfg.CrashDetector,
		maxRetries:               maxRetries,
		tokenCounter:             cfg.TokenCounter,
		policy:                   cfg.Policy,
		inFlightRequests:         make(map[string][]string),
		idleSuspendTimeout:       cfg.IdleSuspendTimeout,
		reuseEnabled:             reuseEnabled,
		suspendWakeLatencyTarget: cfg.SuspendWakeLatencyTarget,
		onSpawn:                  cfg.OnSpawn,
		onTerminate:              cfg.OnTerminate,
		onRecover:                cfg.OnRecover,
		onSuspend:                cfg.OnSuspend,
		onWake:                   cfg.OnWake,
	}, nil
}

const (
	agentTypeNew      = "new_provision"
	agentTypeExisting = "existing_assigned"
)

// LoadSynthesizedSkills reads all persisted "skill_cache" entries from the Memory
// Component and registers them into the Skill Hierarchy Manager. Call this once at
// component startup, after static skills have been loaded from config, so skills
// synthesized in prior sessions extend the live skill tree.
//
// Domains that do not exist in the static config are skipped — a synthesized skill
// requires its parent domain to be registered first. Individual registration failures
// are logged and skipped; they do not abort the startup sequence.
func (f *Factory) LoadSynthesizedSkills(ctx context.Context) error {
	records, err := f.memory.ReadAllByType("skill_cache")
	if err != nil {
		return fmt.Errorf("factory: load synthesized skills: read: %w", err)
	}
	if len(records) == 0 {
		f.log.Info("factory: no synthesized skills found in memory")
		return nil
	}

	loaded := 0
	for _, record := range records {
		domain, ok := record.Tags["domain"]
		if !ok || domain == "" {
			f.log.Warn("factory: synthesized skill record missing domain tag; skipping",
				"agent_id", record.AgentID)
			continue
		}

		// MemoryWrite.Payload is interface{}; after a JSON round-trip through the
		// Memory Component it deserializes as map[string]interface{}. Re-marshal
		// and unmarshal to recover the typed SkillNode.
		raw, err := json.Marshal(record.Payload)
		if err != nil {
			f.log.Warn("factory: marshal synthesized skill payload failed; skipping",
				"domain", domain, "error", err)
			continue
		}
		var node types.SkillNode
		if err := json.Unmarshal(raw, &node); err != nil {
			f.log.Warn("factory: unmarshal synthesized skill node failed; skipping",
				"domain", domain, "error", err)
			continue
		}

		if err := f.skills.RegisterCommand(domain, &node); err != nil {
			f.log.Warn("factory: register synthesized skill failed; skipping",
				"domain", domain, "skill_name", node.Name, "error", err)
			continue
		}
		loaded++
		f.log.Info("factory: synthesized skill registered",
			"domain", domain, "skill_name", node.Name)
	}

	f.log.Info("factory: synthesized skill load complete",
		"loaded", loaded, "found", len(records))
	return nil
}

// emit dispatches an audit event off the critical path. It is a no-op when
// the emitter is nil (unit tests that do not wire a Comms client).
func (f *Factory) emit(event types.AuditEvent) {
	if f.emitter != nil {
		f.emitter.Emit(event)
	}
}

// credDenied returns true when a PreAuthorize error represents an explicit
// vault denial rather than an infrastructure failure (VAULT_UNREACHABLE).
// A denial means the vault responded; an unreachable failure means it did not.
func credDenied(err error) bool {
	return err != nil && credErrorCode(err) != "VAULT_UNREACHABLE"
}

// HandleTaskSpec processes an incoming TaskSpec. It publishes task.accepted
// immediately — before any provisioning work — then either reuses an idle agent
// or provisions a new one (EDD §8.3).
func (f *Factory) HandleTaskSpec(spec *types.TaskSpec) error {
	if spec == nil {
		return fmt.Errorf("factory: TaskSpec must not be nil")
	}
	if spec.TaskID == "" {
		return fmt.Errorf("factory: TaskSpec.TaskID must not be empty")
	}

	// Determine routing: reuse an idle agent or provision a new one.
	candidates, err := f.registry.FindBySkills(spec.RequiredSkills)
	if err != nil {
		return fmt.Errorf("factory: registry lookup: %w", err)
	}

	// Priority 1: IDLE agent — instant reuse; VM already running (M3) or entry warm (M2).
	for _, agent := range candidates {
		if agent.State == registry.StateIdle {
			// Publish task.accepted before doing any work (§8.3 deadline: 5s from receipt).
			if err := f.publishAccepted(agent.AgentID, agentTypeExisting, spec); err != nil {
				return err
			}
			return f.assignTask(agent.AgentID, spec)
		}
	}

	// Priority 2: SUSPENDED agent — requires credential re-auth + VM spawn (OQ-03/OQ-06).
	// Only reachable when IdleSuspendTimeout > 0 (feature enabled); suspended agents
	// only exist when the idle sweep has been running.
	for _, agent := range candidates {
		if agent.State == registry.StateSuspended {
			if err := f.publishAccepted(agent.AgentID, agentTypeExisting, spec); err != nil {
				return err
			}
			return f.wakeAgent(agent.AgentID, spec)
		}
	}

	// Priority 3: No reusable agent — provision a new one.
	agentID := f.generateID()
	if err := f.publishAccepted(agentID, agentTypeNew, spec); err != nil {
		return err
	}
	return f.provision(agentID, spec)
}

// provision runs the full agent provisioning sequence for a TaskSpec.
// agentID is pre-generated by HandleTaskSpec so it can be published in task.accepted
// before any provisioning work begins.
//
// State walk: PENDING → SPAWNING → ACTIVE.
// A status event is published to aegis.orchestrator.agent.status at every transition.
func (f *Factory) provision(agentID string, spec *types.TaskSpec) error {
	if len(spec.RequiredSkills) == 0 {
		return fmt.Errorf("factory: TaskSpec has no required skills")
	}

	f.emit(types.AuditEvent{
		EventType: types.AuditEventProvisioningStart,
		AgentID:   agentID,
		TaskID:    spec.TaskID,
		TraceID:   spec.TraceID,
		Details:   map[string]string{"skill_domains": strings.Join(spec.RequiredSkills, ",")},
	})

	// Step 1: Resolve entry-point skill domain (first required domain).
	entryDomain := spec.RequiredSkills[0]
	if _, err := f.skills.GetDomain(entryDomain); err != nil {
		return fmt.Errorf("factory: skills.GetDomain %q: %w", entryDomain, err)
	}

	// Step 1a: Build the command manifest for the entry domain. GetCommands
	// returns command-level nodes (name + description only, no parameter specs)
	// which are serialised into the "- name: description" format injected into
	// the system prompt. An empty manifest is valid — the agent can still
	// discover commands reactively via the skills tools.
	commands, err := f.skills.GetCommands(entryDomain)
	if err != nil {
		return fmt.Errorf("factory: skills.GetCommands %q: %w", entryDomain, err)
	}
	manifest := buildManifestText(commands)

	// Step 1b: Enforce spawn context token budget (EDD §13.3, PRD FR-FAC-05).
	// Counted components: system prompt (including manifest) + task instructions
	// + entry-point skill domain name + permission token reference.
	// The permission token is not yet available, so a fixed-length placeholder
	// that approximates a real Vault token reference is used.
	// Abort immediately if the budget is exceeded — no agent is created.
	if f.tokenCounter != nil {
		ctxText := buildSpawnContextText(spawnSystemPrompt(entryDomain, manifest), spec.Instructions, entryDomain, permTokenPlaceholder)
		count, err := f.tokenCounter.CountTokens(ctxText)
		if err != nil {
			return fmt.Errorf("factory: token count: %w", err)
		}
		if count > spawnContextBudget {
			_ = f.publishFailed(agentID, spec.TaskID, "CONTEXT_BUDGET_EXCEEDED",
				fmt.Sprintf("spawn context token count %d exceeds the 2,048-token budget", count),
				spec.TraceID, "skill_resolution")
			return fmt.Errorf("factory: spawn context budget exceeded: %d tokens (max %d)", count, spawnContextBudget)
		}
	}

	permSet, err := f.resolvePermissions(spec.RequiredSkills, spec.TaskID, spec.TraceID, agentID)
	if err != nil {
		return err
	}

	// Allocate a VM ID now so it can be stored in the registry entry before the
	// VM is launched. vm_id changes on respawn (same agent_id, new vm_id).
	vmID := f.generateID()

	// Step 2: Register agent in PENDING state. Registry enforces this as the
	// mandatory starting state — the caller does not set State on the record.
	agent := &types.AgentRecord{
		AgentID:       agentID,
		VMID:          vmID,
		SkillDomains:  spec.RequiredSkills,
		PermissionSet: permSet,
		AssignedTask:  spec.TaskID,
		Instructions:  spec.Instructions,
		TraceID:       spec.TraceID,
		UserContextID: spec.UserContextID,
	}
	if err := f.registry.Register(agent); err != nil {
		return fmt.Errorf("factory: registry.Register: %w", err)
	}
	if err := f.publishStatus(agentID, spec.TaskID, registry.StatePending, spec.TraceID); err != nil {
		return err
	}

	// Step 3: Transition to SPAWNING before any external calls begin.
	if err := f.registry.UpdateState(agentID, registry.StateSpawning, "credential pre-authorization starting"); err != nil {
		return fmt.Errorf("factory: UpdateState spawning: %w", err)
	}
	if err := f.publishStatus(agentID, spec.TaskID, registry.StateSpawning, spec.TraceID); err != nil {
		return err
	}

	// Step 4: Pre-authorize credential permission set via real NATS round-trip.
	// On VAULT_UNREACHABLE the broker has already exhausted its retry budget.
	token, err := f.credentials.PreAuthorize(agentID, spec.TaskID, spec.TraceID, spec.RequiredSkills)
	if err != nil {
		f.log.Warn("credential.event",
			"operation_type", "authorize",
			"agent_id", agentID,
			"outcome", "failed",
			"trace_id", spec.TraceID,
		)
		if credDenied(err) {
			f.emit(types.AuditEvent{
				EventType: types.AuditEventCredentialDeny,
				AgentID:   agentID,
				TaskID:    spec.TaskID,
				TraceID:   spec.TraceID,
				Details:   map[string]string{"operation_type": "authorize", "error_code": credErrorCode(err)},
			})
		}
		_ = f.registry.UpdateState(agentID, registry.StateTerminated, "credential pre-authorization failed")
		_ = f.publishFailed(agentID, spec.TaskID, credErrorCode(err),
			"agent credential pre-authorization failed", spec.TraceID, "")
		return fmt.Errorf("factory: credentials.PreAuthorize: %w", err)
	}
	f.log.Info("credential.event",
		"operation_type", "authorize",
		"agent_id", agentID,
		"outcome", "granted",
		"trace_id", spec.TraceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventCredentialGrant,
		AgentID:   agentID,
		TaskID:    spec.TaskID,
		TraceID:   spec.TraceID,
		Details: map[string]string{
			"operation_type": "authorize",
			"skill_domains":  strings.Join(spec.RequiredSkills, ","),
		},
	})

	// Step 5: Fetch multi-layer memory to inject at spawn (capability 2).
	// These reads are best-effort; failure does not abort provisioning.
	agentMemory := f.fetchAgentMemory(entryDomain, spec.TraceID)
	userProfile := f.fetchUserProfile(spec.UserContextID, spec.TraceID)
	priorTurns, _ := f.fetchPriorTurns(spec.ConversationID, spec.TraceID)

	// Step 6: Spawn agent process.
	vmCfg := lifecycle.VMConfig{
		AgentID:         agentID,
		VMID:            vmID,
		TaskID:          spec.TaskID,
		SkillDomain:     entryDomain,
		CredentialPtr:   token,
		Instructions:    spec.Instructions,
		CommandManifest: manifest,
		AgentMemory:     agentMemory,
		UserProfile:     userProfile,
		TraceID:         spec.TraceID,
		UserContextID:   spec.UserContextID,
		ConversationID:  spec.ConversationID,
		PriorTurns:      priorTurns,
		OnComplete:      f.processCompletionHandler(agentID),
	}
	if err := f.lifecycle.Spawn(vmCfg); err != nil {
		return fmt.Errorf("factory: lifecycle.Spawn: %w", err)
	}
	if f.onSpawn != nil {
		f.onSpawn(agentID)
	}

	// Step 6: Transition to ACTIVE — VM is up and task is running.
	if err := f.registry.UpdateState(agentID, registry.StateActive, "VM spawned"); err != nil {
		return fmt.Errorf("factory: UpdateState active: %w", err)
	}

	// Begin heartbeat crash monitoring now that the process is live.
	if f.crashDetector != nil {
		f.crashDetector.Watch(agentID)
	}

	f.emit(types.AuditEvent{
		EventType: types.AuditEventProvisioningComplete,
		AgentID:   agentID,
		TaskID:    spec.TaskID,
		TraceID:   spec.TraceID,
		Details:   map[string]string{"skill_domains": strings.Join(spec.RequiredSkills, ",")},
	})
	return f.publishStatus(agentID, spec.TaskID, registry.StateActive, spec.TraceID)
}

// assignTask links an existing idle agent (Priority 1 path) to a new task.
// When live-process reuse is enabled, it drives the full warm-path sequence:
// fresh credential pre-authorization, fresh memory / user-profile fetches, and
// lifecycle.Deliver of a new SpawnContext to the still-running agent process.
// When reuse is disabled, it performs only the minimal registry transition —
// the historical behaviour used by unit tests that set IDLE state manually.
func (f *Factory) assignTask(agentID string, spec *types.TaskSpec) error {
	if !f.reuseEnabled {
		if err := f.registry.AssignTask(agentID, spec.TaskID); err != nil {
			return fmt.Errorf("factory: registry.AssignTask: %w", err)
		}
		return f.publishStatus(agentID, spec.TaskID, registry.StateActive, spec.TraceID)
	}

	// ── Warm path: deliver a fresh SpawnContext to the live process. ──────
	if len(spec.RequiredSkills) == 0 {
		return fmt.Errorf("factory: assignTask: TaskSpec has no required skills")
	}

	// Fresh credential pre-authorization is mandatory: the previous task's
	// token was revoked in CompleteTask and credentials must stay task-scoped.
	token, err := f.credentials.PreAuthorize(agentID, spec.TaskID, spec.TraceID, spec.RequiredSkills)
	if err != nil {
		f.log.Warn("credential.event",
			"operation_type", "authorize",
			"agent_id", agentID,
			"outcome", "failed",
			"trace_id", spec.TraceID,
			"reuse_path", true,
		)
		if credDenied(err) {
			f.emit(types.AuditEvent{
				EventType: types.AuditEventCredentialDeny,
				AgentID:   agentID,
				TaskID:    spec.TaskID,
				TraceID:   spec.TraceID,
				Details:   map[string]string{"operation_type": "authorize", "error_code": credErrorCode(err)},
			})
		}
		_ = f.publishFailed(agentID, spec.TaskID, credErrorCode(err),
			"agent credential re-authorization failed on reuse", spec.TraceID, "reuse")
		return fmt.Errorf("factory: assignTask: credentials.PreAuthorize: %w", err)
	}
	f.log.Info("credential.event",
		"operation_type", "authorize",
		"agent_id", agentID,
		"outcome", "granted",
		"trace_id", spec.TraceID,
		"reuse_path", true,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventCredentialGrant,
		AgentID:   agentID,
		TaskID:    spec.TaskID,
		TraceID:   spec.TraceID,
		Details: map[string]string{
			"operation_type": "authorize",
			"skill_domains":  strings.Join(spec.RequiredSkills, ","),
			"reuse_path":     "true",
		},
	})

	entryDomain := spec.RequiredSkills[0]
	commands, err := f.skills.GetCommands(entryDomain)
	if err != nil {
		_ = f.credentials.Revoke(agentID)
		return fmt.Errorf("factory: assignTask: skills.GetCommands %q: %w", entryDomain, err)
	}
	manifest := buildManifestText(commands)
	agentMemory := f.fetchAgentMemory(entryDomain, spec.TraceID)
	userProfile := f.fetchUserProfile(spec.UserContextID, spec.TraceID)
	priorTurns, _ := f.fetchPriorTurns(spec.ConversationID, spec.TraceID)

	vmCfg := lifecycle.VMConfig{
		AgentID:         agentID,
		TaskID:          spec.TaskID,
		SkillDomain:     entryDomain,
		CredentialPtr:   token,
		Instructions:    spec.Instructions,
		CommandManifest: manifest,
		AgentMemory:     agentMemory,
		UserProfile:     userProfile,
		TraceID:         spec.TraceID,
		UserContextID:   spec.UserContextID,
		ConversationID:  spec.ConversationID,
		PriorTurns:      priorTurns,
		OnComplete:      f.processCompletionHandler(agentID),
	}

	if err := f.lifecycle.Deliver(agentID, vmCfg); err != nil {
		// Delivery failed — the live process is gone, its stdin is closed, or
		// a previous completion callback is still pending. Reuse is an
		// optimization and must never surface as a user-visible task failure:
		// tear down the zombie entry and fall back to Priority 3 (fresh
		// provision). The user sees a slightly longer first task but no error.
		f.log.Warn("lifecycle.Deliver failed — falling back to fresh provision",
			"agent_id", agentID,
			"task_id", spec.TaskID,
			"trace_id", spec.TraceID,
			"error", err,
		)
		// Revoke the just-issued token (Deliver never reached the agent so the
		// token was never consumed) and force-terminate the zombie process so
		// its registry slot cannot be matched again.
		_ = f.credentials.Revoke(agentID)
		_ = f.lifecycle.Terminate(agentID)
		if err := f.registry.UpdateState(agentID, registry.StateTerminated,
			"lifecycle.Deliver failed: "+err.Error()); err != nil {
			f.log.Warn("reuse fallback: registry.UpdateState terminated failed",
				"agent_id", agentID, "error", err)
		}
		_ = f.publishStatus(agentID, spec.TaskID, registry.StateTerminated, spec.TraceID)
		// Provision a fresh agent with a new ID. publishAccepted was already
		// emitted under the zombie agent_id; the new provision emits its own
		// status stream rooted at the new ID, which is what the IO dashboard
		// consumes for progress updates.
		newAgentID := f.generateID()
		f.log.Info("reuse fallback: provisioning fresh agent",
			"zombie_agent_id", agentID,
			"new_agent_id", newAgentID,
			"task_id", spec.TaskID,
			"trace_id", spec.TraceID,
		)
		if err := f.provision(newAgentID, spec); err != nil {
			_ = f.publishFailed(newAgentID, spec.TaskID, "AGENT_PROVISION_FAILED",
				"fresh provision after reuse fallback failed: "+err.Error(),
				spec.TraceID, "reuse-fallback")
			return fmt.Errorf("factory: reuse fallback provision: %w", err)
		}
		return nil
	}

	if err := f.registry.AssignTask(agentID, spec.TaskID); err != nil {
		return fmt.Errorf("factory: registry.AssignTask: %w", err)
	}

	// Re-arm crash monitoring: CompleteTask called Unwatch when the previous
	// task finished, leaving the live process un-monitored until now.
	if f.crashDetector != nil {
		f.crashDetector.Watch(agentID)
	}

	f.log.Info("task delivered to reused agent",
		"agent_id", agentID,
		"task_id", spec.TaskID,
		"trace_id", spec.TraceID,
		"skill_domain", entryDomain,
	)
	return f.publishStatus(agentID, spec.TaskID, registry.StateActive, spec.TraceID)
}

// processCompletionHandler returns the OnComplete callback set on VMConfig at
// spawn time. When the agent-process exits cleanly (exitErr == nil) it decodes
// the TaskOutput from stdout and calls CompleteTask, which unregisters the
// agent from crash monitoring before the heartbeat detector can misfire.
// Non-zero exits are left to the crash detector so recovery follows the normal
// HandleCrash path.
func (f *Factory) processCompletionHandler(agentID string) func(string, []byte, error) {
	return func(_ string, output []byte, exitErr error) {
		if exitErr != nil {
			// Process crashed or was killed — crash detector will fire after
			// heartbeat timeout and call HandleCrash. Nothing to do here.
			return
		}
		var out struct {
			TraceID string `json:"trace_id"`
			Success bool   `json:"success"`
			Result  string `json:"result,omitempty"`
			Error   string `json:"error,omitempty"`
		}
		if err := json.Unmarshal(output, &out); err != nil {
			f.log.Error("agent-process: failed to decode TaskOutput",
				"agent_id", agentID, "error", err)
			return
		}
		var taskErr error
		if !out.Success {
			taskErr = errors.New(out.Error)
		}
		if err := f.CompleteTask(agentID, "", out.TraceID, out.Result, taskErr); err != nil {
			f.log.Error("agent-process: CompleteTask failed",
				"agent_id", agentID, "error", err)
		}
	}
}

// CompleteTask collects results, writes to Memory, publishes task_result, and
// tears down the agent.
func (f *Factory) CompleteTask(agentID, sessionID, traceID string, output interface{}, taskErr error) error {
	agent, err := f.registry.Get(agentID)
	if err != nil {
		return fmt.Errorf("factory: registry.Get: %w", err)
	}

	// Publish tagged output via Memory Interface. The Orchestrator routes it to the Memory Component.
	mw := &types.MemoryWrite{
		AgentID:   agentID,
		SessionID: sessionID,
		DataType:  "task_result",
		TTLHint:   86400,
		Payload:   output,
		Tags:      map[string]string{"context": "result", "task_id": agent.AssignedTask},
	}
	if err := f.memory.Write(mw); err != nil {
		return fmt.Errorf("factory: memory.Write: %w", err)
	}

	// Publish task_result to Orchestrator.
	result := types.TaskResult{
		TaskID:        agent.AssignedTask,
		AgentID:       agentID,
		Success:       taskErr == nil,
		Output:        output,
		TraceID:       traceID,
		UserContextID: agent.UserContextID,
	}
	if taskErr != nil {
		result.Error = taskErr.Error()
	}
	f.log.Info("msg.outbound",
		"topic", comms.SubjectTaskResult,
		"message_type", comms.MsgTypeTaskResult,
		"agent_id", agentID,
		"task_id", agent.AssignedTask,
		"correlation_id", agent.AssignedTask,
		"trace_id", traceID,
		"success", taskErr == nil,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventTaskCompleted,
		AgentID:   agentID,
		TaskID:    agent.AssignedTask,
		TraceID:   traceID,
	})
	if err := f.comms.Publish(
		comms.SubjectTaskResult,
		comms.PublishOptions{MessageType: comms.MsgTypeTaskResult, CorrelationID: agent.AssignedTask},
		result,
	); err != nil {
		return fmt.Errorf("factory: comms.Publish task.result: %w", err)
	}

	// Transition ACTIVE → IDLE.
	// The state machine does not permit ACTIVE → TERMINATED directly.
	if err := f.registry.UpdateState(agentID, registry.StateIdle, "task complete"); err != nil {
		return fmt.Errorf("factory: UpdateState idle: %w", err)
	}
	if err := f.publishStatus(agentID, agent.AssignedTask, registry.StateIdle, traceID); err != nil {
		return err
	}

	// Stop crash monitoring — task is done; the agent is no longer executing.
	// When reuse is enabled, assignTask re-arms the detector on the next task.
	if f.crashDetector != nil {
		f.crashDetector.Unwatch(agentID)
	}

	// Revoke the task's credential token. Credentials are always task-scoped —
	// whether or not we keep the underlying process alive for reuse, the token
	// itself must not outlive its task (CLAUDE.md §5; EDD §6.2).
	if err := f.credentials.Revoke(agentID); err != nil {
		f.log.Warn("credential.event",
			"operation_type", "revoke",
			"agent_id", agentID,
			"outcome", "failed",
			"trace_id", traceID,
		)
		return fmt.Errorf("factory: credentials.Revoke: %w", err)
	}
	f.log.Info("credential.event",
		"operation_type", "revoke",
		"agent_id", agentID,
		"outcome", "ok",
		"trace_id", traceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventCredentialRevoke,
		AgentID:   agentID,
		TaskID:    agent.AssignedTask,
		TraceID:   traceID,
		Details:   map[string]string{"operation_type": "revoke"},
	})

	// ── Reuse-enabled path ────────────────────────────────────────────────
	// Keep the agent-process alive so the next matching task can hit the
	// warm Priority 1 path in HandleTaskSpec and skip the cold-start tax.
	// Registry stays IDLE; the idle sweep (OQ-03) will SUSPEND it after
	// idleSuspendTimeout of inactivity if no new task arrives.
	if f.reuseEnabled {
		f.log.Info("agent kept alive for reuse",
			"agent_id", agentID,
			"idle_suspend_timeout", f.idleSuspendTimeout,
			"trace_id", traceID,
		)
		return nil
	}

	// ── Classic path ───────────────────────────────────────────────────────
	// Reuse disabled or unsupported by this lifecycle: terminate the VM and
	// mark the registry entry TERMINATED. Next matching task provisions fresh.
	if err := f.lifecycle.Terminate(agentID); err != nil {
		return fmt.Errorf("factory: lifecycle.Terminate: %w", err)
	}
	if f.onTerminate != nil {
		f.onTerminate(agentID)
	}

	if err := f.registry.UpdateState(agentID, registry.StateTerminated, "VM terminated, credentials revoked"); err != nil {
		return fmt.Errorf("factory: UpdateState terminated: %w", err)
	}
	return f.publishStatus(agentID, agent.AssignedTask, registry.StateTerminated, traceID)
}

// HandleCrash implements the full crash recovery sequence for a crashed agent
// (EDD §6.3). It is called by the CrashDetector callback when heartbeat
// monitoring declares an agent dead.
//
// Sequence:
//  1. Save last known state as a snapshot via the Memory Interface.
//  2. Flush in-flight Vault request_ids with no result → flagged UNKNOWN in snapshot.
//  3. Transition to RECOVERING (increments failure_count in registry).
//  4. Decide restart vs. replace: failure_count >= maxRetries → TERMINATED.
//  5. Respawn: fresh VM with same agent_id, new vm_id, recovered state as context.
//  6. Credential re-authorization: fresh PreAuthorize for the same permission set.
//  7. Registry update: new vm_id set, state → ACTIVE, failure_count already incremented.
func (f *Factory) HandleCrash(agentID string) error {
	// Step 1: Read current agent state from registry.
	agent, err := f.registry.Get(agentID)
	if err != nil {
		return fmt.Errorf("factory: HandleCrash: registry.Get: %w", err)
	}

	// Step 2: Flush in-flight Vault request_ids with no result. Any remaining
	// entries had no corresponding vault.execute.result at crash time → UNKNOWN.
	unknownRequestIDs := f.flushInFlightRequests(agentID)

	// Save crash snapshot to Memory Interface before any state mutation.
	snapshot := types.CrashSnapshot{
		AgentID:                agentID,
		TaskID:                 agent.AssignedTask,
		FailureCount:           agent.FailureCount,
		State:                  agent.State,
		SkillDomains:           agent.SkillDomains,
		PermissionSet:          agent.PermissionSet,
		UnknownVaultRequestIDs: unknownRequestIDs,
		CrashedAt:              time.Now().UTC(),
	}
	mw := &types.MemoryWrite{
		AgentID:   agentID,
		SessionID: agent.AssignedTask, // task ID serves as session scope for crash snapshots
		DataType:  "snapshot",
		TTLHint:   86400,
		Payload:   snapshot,
		Tags: map[string]string{
			"context": "crash_snapshot",
			"task_id": agent.AssignedTask,
		},
	}
	if err := f.memory.Write(mw); err != nil {
		f.log.Error("crash recovery aborted: snapshot persistence unavailable",
			"agent_id", agentID,
			"error", err,
		)
		_ = f.registry.UpdateState(agentID, registry.StateTerminated,
			"crash recovery aborted: snapshot persistence unavailable")
		_ = f.publishStatus(agentID, agent.AssignedTask, registry.StateTerminated, "")
		return fmt.Errorf("factory: HandleCrash: memory.Write snapshot: %w", err)
	}

	// Step 3: Transition to RECOVERING — increments failure_count in registry.
	if err := f.registry.UpdateState(agentID, registry.StateRecovering, "crash detected: heartbeat timeout"); err != nil {
		return fmt.Errorf("factory: HandleCrash: UpdateState recovering: %w", err)
	}
	f.emit(types.AuditEvent{
		EventType: types.AuditEventRecoveryAttempt,
		AgentID:   agentID,
		TaskID:    agent.AssignedTask,
		Details: map[string]string{
			"failure_count": fmt.Sprintf("%d", agent.FailureCount+1), // +1 because registry already incremented
			"max_retries":   fmt.Sprintf("%d", f.maxRetries),
		},
	})
	if err := f.publishStatus(agentID, agent.AssignedTask, registry.StateRecovering, ""); err != nil {
		return err
	}

	// Re-fetch to read the incremented failure_count.
	agent, err = f.registry.Get(agentID)
	if err != nil {
		return fmt.Errorf("factory: HandleCrash: registry.Get post-recover: %w", err)
	}

	// Step 4: Decide restart vs. replace.
	if agent.FailureCount >= f.maxRetries {
		// Exceeded retry budget — permanently terminate.
		// Best-effort credential revocation; the process is already dead.
		if err := f.credentials.Revoke(agentID); err != nil {
			f.log.Warn("credential.event",
				"operation_type", "revoke",
				"agent_id", agentID,
				"outcome", "failed",
			)
		} else {
			f.log.Info("credential.event",
				"operation_type", "revoke",
				"agent_id", agentID,
				"outcome", "ok",
			)
			f.emit(types.AuditEvent{
				EventType: types.AuditEventCredentialRevoke,
				AgentID:   agentID,
				TaskID:    agent.AssignedTask,
				Details:   map[string]string{"operation_type": "revoke"},
			})
		}
		if err := f.registry.UpdateState(agentID, registry.StateTerminated,
			fmt.Sprintf("max retries exceeded (%d/%d)", agent.FailureCount, f.maxRetries),
		); err != nil {
			return fmt.Errorf("factory: HandleCrash: UpdateState terminated: %w", err)
		}
		return f.publishStatus(agentID, agent.AssignedTask, registry.StateTerminated, "")
	}

	// Step 5: Clean up the stale VM entry. The process has crashed so it is
	// already dead; Terminate just removes the stale lifecycle entry.
	_ = f.lifecycle.Terminate(agentID)

	// Step 6: Credential re-authorization — fresh permission token for the new VM.
	token, err := f.credentials.PreAuthorize(agentID, agent.AssignedTask, agent.TraceID, agent.SkillDomains)
	if err != nil {
		f.log.Warn("credential.event",
			"operation_type", "authorize",
			"agent_id", agentID,
			"outcome", "failed",
		)
		if credDenied(err) {
			f.emit(types.AuditEvent{
				EventType: types.AuditEventCredentialDeny,
				AgentID:   agentID,
				TaskID:    agent.AssignedTask,
				Details:   map[string]string{"operation_type": "authorize", "error_code": credErrorCode(err)},
			})
		}
		// Vault unreachable during recovery — permanently terminate.
		_ = f.registry.UpdateState(agentID, registry.StateTerminated, "credential re-authorization failed during recovery")
		_ = f.publishFailed(agentID, agent.AssignedTask, credErrorCode(err),
			"agent credential re-authorization failed during crash recovery", "", "")
		return fmt.Errorf("factory: HandleCrash: credentials.PreAuthorize: %w", err)
	}
	f.log.Info("credential.event",
		"operation_type", "authorize",
		"agent_id", agentID,
		"outcome", "granted",
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventCredentialGrant,
		AgentID:   agentID,
		TaskID:    agent.AssignedTask,
		Details: map[string]string{
			"operation_type": "authorize",
			"skill_domains":  strings.Join(agent.SkillDomains, ","),
		},
	})

	// Step 7: Respawn — same agent_id, new vm_id, recovered state injected as context.
	newVMID := f.generateID()
	entryDomain := ""
	if len(agent.SkillDomains) > 0 {
		entryDomain = agent.SkillDomains[0]
	}
	vmCfg := lifecycle.VMConfig{
		AgentID:          agentID,
		VMID:             newVMID,
		TaskID:           agent.AssignedTask,
		SkillDomain:      entryDomain,
		CredentialPtr:    token,
		Instructions:     agent.Instructions,
		RecoveredContext: buildRecoveredContext(snapshot),
		TraceID:          agent.TraceID,
		UserContextID:    agent.UserContextID,
		OnComplete:       f.processCompletionHandler(agentID),
	}
	if err := f.lifecycle.Spawn(vmCfg); err != nil {
		return fmt.Errorf("factory: HandleCrash: lifecycle.Spawn: %w", err)
	}
	if f.onRecover != nil {
		f.onRecover(agentID)
	}

	// Step 8: Registry update — new vm_id, transition RECOVERING → ACTIVE.
	if err := f.registry.SetVMID(agentID, newVMID); err != nil {
		return fmt.Errorf("factory: HandleCrash: registry.SetVMID: %w", err)
	}
	if err := f.registry.UpdateState(agentID, registry.StateActive,
		fmt.Sprintf("respawned after crash (attempt %d/%d)", agent.FailureCount, f.maxRetries),
	); err != nil {
		return fmt.Errorf("factory: HandleCrash: UpdateState active: %w", err)
	}

	// Resume heartbeat monitoring for the new VM.
	if f.crashDetector != nil {
		f.crashDetector.Watch(agentID)
	}

	return f.publishStatus(agentID, agent.AssignedTask, registry.StateActive, "")
}

// TrackVaultRequest records a Vault execute request_id as in-flight for an agent.
// Call this immediately before dispatching a vault.execute.request so the request
// is captured in crash snapshots if the agent dies before a result arrives.
func (f *Factory) TrackVaultRequest(agentID, requestID string) {
	f.inFlightMu.Lock()
	f.inFlightRequests[agentID] = append(f.inFlightRequests[agentID], requestID)
	f.inFlightMu.Unlock()
}

// CompleteVaultRequest removes a Vault execute request_id from the in-flight set
// for an agent. Call this when a vault.execute.result is received. No-op for
// unknown agent or request IDs.
func (f *Factory) CompleteVaultRequest(agentID, requestID string) {
	f.inFlightMu.Lock()
	defer f.inFlightMu.Unlock()

	ids := f.inFlightRequests[agentID]
	for i, id := range ids {
		if id == requestID {
			f.inFlightRequests[agentID] = append(ids[:i], ids[i+1:]...)
			if len(f.inFlightRequests[agentID]) == 0 {
				delete(f.inFlightRequests, agentID)
			}
			return
		}
	}
}

// flushInFlightRequests atomically removes and returns all in-flight request_ids
// for an agent. Used at the start of HandleCrash to collect requests that had no
// result at crash time.
func (f *Factory) flushInFlightRequests(agentID string) []string {
	f.inFlightMu.Lock()
	defer f.inFlightMu.Unlock()

	ids := f.inFlightRequests[agentID]
	delete(f.inFlightRequests, agentID)
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// buildRecoveredContext formats a CrashSnapshot as a human-readable string
// to inject into the respawned agent's spawn context. The agent uses this to
// identify where it left off and which Vault operations to resubmit.
func buildRecoveredContext(s types.CrashSnapshot) string {
	var b strings.Builder
	b.WriteString("[RECOVERED STATE — agent restarted after crash]\n")
	fmt.Fprintf(&b, "Task ID: %s\n", s.TaskID)
	fmt.Fprintf(&b, "Crashed at: %s\n", s.CrashedAt.Format(time.RFC3339))
	if len(s.UnknownVaultRequestIDs) > 0 {
		b.WriteString("In-flight Vault operations at crash time (resubmit with original request_id for idempotent execution):\n")
		for _, id := range s.UnknownVaultRequestIDs {
			fmt.Fprintf(&b, "  - %s (status: UNKNOWN)\n", id)
		}
	}
	return b.String()
}

// publishStatus sends a StatusUpdate to the Orchestrator via Comms.
func (f *Factory) publishStatus(agentID, taskID, state, traceID string) error {
	f.log.Info("agent.state.transition",
		"topic", comms.SubjectAgentStatus,
		"message_type", comms.MsgTypeAgentStatus,
		"agent_id", agentID,
		"task_id", taskID,
		"state", state,
		"correlation_id", taskID,
		"trace_id", traceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventStateTransition,
		AgentID:   agentID,
		TaskID:    taskID,
		TraceID:   traceID,
		Details:   map[string]string{"state": state},
	})
	update := types.StatusUpdate{
		TaskID:  taskID,
		AgentID: agentID,
		State:   state,
		TraceID: traceID,
	}
	// Best-effort: propagate user_context_id if the agent record is available.
	if agent, err := f.registry.Get(agentID); err == nil {
		update.UserContextID = agent.UserContextID
	}
	if err := f.comms.Publish(
		comms.SubjectAgentStatus,
		comms.PublishOptions{MessageType: comms.MsgTypeAgentStatus, CorrelationID: taskID},
		update,
	); err != nil {
		return fmt.Errorf("factory: comms.Publish agent.status: %w", err)
	}
	return nil
}

// publishFailed sends a TaskFailed to the Orchestrator. Called when a task cannot
// be executed due to a non-recoverable provisioning or credential failure.
// errorMessage must be user-safe — it must not expose credential values or vault paths.
// phase is the provisioning phase where the failure occurred (e.g. "skill_resolution");
// pass "" when the phase is not applicable.
func (f *Factory) publishFailed(agentID, taskID, errorCode, errorMessage, traceID, phase string) error {
	f.log.Warn("msg.outbound",
		"topic", comms.SubjectTaskFailed,
		"message_type", comms.MsgTypeTaskFailed,
		"agent_id", agentID,
		"task_id", taskID,
		"error_code", errorCode,
		"correlation_id", taskID,
		"trace_id", traceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventTaskFailed,
		AgentID:   agentID,
		TaskID:    taskID,
		TraceID:   traceID,
		Details:   map[string]string{"error_code": errorCode, "phase": phase},
	})
	f.emit(types.AuditEvent{
		EventType: types.AuditEventProvisioningFail,
		AgentID:   agentID,
		TaskID:    taskID,
		TraceID:   traceID,
		Details:   map[string]string{"error_code": errorCode, "phase": phase},
	})
	failed := types.TaskFailed{
		TaskID:       taskID,
		AgentID:      agentID,
		ErrorCode:    errorCode,
		ErrorMessage: errorMessage,
		Phase:        phase,
		TraceID:      traceID,
	}
	// Best-effort: propagate user_context_id if the agent record is available.
	if agent, err := f.registry.Get(agentID); err == nil {
		failed.UserContextID = agent.UserContextID
	}
	if err := f.comms.Publish(
		comms.SubjectTaskFailed,
		comms.PublishOptions{MessageType: comms.MsgTypeTaskFailed, CorrelationID: taskID},
		failed,
	); err != nil {
		return fmt.Errorf("factory: comms.Publish task.failed: %w", err)
	}
	return nil
}

// credErrorCode maps a credentials.PreAuthorize error to an error code for task.failed.
func credErrorCode(err error) string {
	if err != nil && strings.Contains(err.Error(), "VAULT_UNREACHABLE") {
		return "VAULT_UNREACHABLE"
	}
	return "PROVISION_FAILED"
}

// publishAccepted sends a TaskAccepted to the Orchestrator. Must be called before
// any provisioning work so the Orchestrator knows the task has been received.
func (f *Factory) publishAccepted(agentID, agentType string, spec *types.TaskSpec) error {
	f.log.Info("msg.outbound",
		"topic", comms.SubjectTaskAccepted,
		"message_type", comms.MsgTypeTaskAccepted,
		"agent_id", agentID,
		"task_id", spec.TaskID,
		"correlation_id", spec.TaskID,
		"trace_id", spec.TraceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventTaskAccepted,
		AgentID:   agentID,
		TaskID:    spec.TaskID,
		TraceID:   spec.TraceID,
		Details:   map[string]string{"agent_type": agentType},
	})
	accepted := types.TaskAccepted{
		TaskID:        spec.TaskID,
		AgentID:       agentID,
		AgentType:     agentType,
		UserContextID: spec.UserContextID,
		TraceID:       spec.TraceID,
		// EstimatedCompletionAt is left nil — no reliable estimate available at this point.
	}
	if err := f.comms.Publish(
		comms.SubjectTaskAccepted,
		comms.PublishOptions{MessageType: comms.MsgTypeTaskAccepted, CorrelationID: spec.TaskID},
		accepted,
	); err != nil {
		return fmt.Errorf("factory: comms.Publish task.accepted: %w", err)
	}
	return nil
}

// resolvePermissions returns the permission set for the requested skill domains.
// When a PermissionPolicy is configured it delegates to policy.PermissionsFor,
// which fails-secure if any domain has no entry. When no policy is configured
// it falls back to the legacy domain.credential stub — acceptable for local dev
// and unit tests, but must not be used in production (no *.credential catch-all).
func (f *Factory) resolvePermissions(domains []string, taskID, traceID, agentID string) ([]string, error) {
	if f.policy != nil {
		perms, err := f.policy.PermissionsFor(domains)
		if err != nil {
			_ = f.publishFailed(agentID, taskID,
				"PERMISSION_POLICY_VIOLATION", err.Error(), traceID, "permission_resolution")
			return nil, fmt.Errorf("factory: %w", err)
		}
		return perms, nil
	}
	// Legacy stub: grant one credential key per domain.
	// Replace in production by supplying a PermissionPolicy in factory.Config.
	perms := make([]string, len(domains))
	for i, d := range domains {
		perms[i] = d + ".credential"
	}
	return perms, nil
}

func defaultIDGenerator() string {
	return fmt.Sprintf("agent-%d", time.Now().UnixNano())
}

// buildManifestText serialises a slice of command-level SkillNodes into the
// "- name: description\n" format injected into the system prompt. Commands are
// sorted alphabetically so the output is deterministic (important for token
// budget counting and test stability). Nodes without a description are still
// included — the name alone is better than nothing.
func buildManifestText(commands []*types.SkillNode) string {
	if len(commands) == 0 {
		return ""
	}
	sorted := make([]*types.SkillNode, len(commands))
	copy(sorted, commands)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	for _, c := range sorted {
		b.WriteString("- ")
		b.WriteString(c.Name)
		b.WriteString(": ")
		b.WriteString(c.Description)
		b.WriteByte('\n')
	}
	return b.String()
}

// spawnSystemPrompt returns the domain-scoped system prompt (including command
// manifest) that will be used at spawn time for token-budget enforcement.
// Must stay in sync with buildSystemPrompt in cmd/agent-process/loop.go.
// agentMemory and userProfile are intentionally excluded from the budget count —
// they are system overhead, not task payload.
func spawnSystemPrompt(skillDomain, manifest string) string {
	base := fmt.Sprintf(
		`You are an Aegis OS agent scoped to the "%s" skill domain. `+
			`Execute the assigned task using only the capabilities available within that domain. `+
			`When the task is complete, call task_complete with the final result. `+
			`Be concise and factual.`,
		skillDomain,
	)
	if manifest == "" {
		return base
	}
	return base + "\n\nAvailable commands:\n" + manifest
}

// fetchAgentMemory reads domain-scoped agent memory facts from the Memory
// Component via the in-process stub or NATS-backed client. Returns "" when
// no records exist or the read fails (best-effort).
//
// The synthetic agent ID "domain:<domain>" is the stable key used by
// PersistAgentMemory in cmd/agent-process/session.go.
func (f *Factory) fetchAgentMemory(domain, traceID string) string {
	if domain == "" {
		return ""
	}
	records, err := f.memory.Read("domain:"+domain, "agent_memory")
	if err != nil || len(records) == 0 {
		if err != nil {
			f.log.Warn("factory: fetchAgentMemory failed", "domain", domain, "error", err, "trace_id", traceID)
		}
		return ""
	}
	return joinMemoryPayloads(records, 4000)
}

// fetchUserProfile reads user-scoped profile observations from the Memory
// Component. Returns "" when userContextID is empty, no records exist, or
// the read fails (best-effort).
//
// The synthetic agent ID "user:<userContextID>" is the stable key used by
// PersistUserProfile in cmd/agent-process/session.go.
func (f *Factory) fetchUserProfile(userContextID, traceID string) string {
	if userContextID == "" {
		return ""
	}
	records, err := f.memory.Read("user:"+userContextID, "user_profile")
	if err != nil || len(records) == 0 {
		if err != nil {
			f.log.Warn("factory: fetchUserProfile failed", "user_context_id", userContextID, "error", err, "trace_id", traceID)
		}
		return ""
	}
	return joinMemoryPayloads(records, 2000)
}

// fetchPriorTurns retrieves the latest ConversationSnapshot for conversationID
// from the Memory Component and returns the prior turns and their recorded token
// count. Returns nil, 0 when conversationID is empty, no snapshot exists, or the
// read fails (best-effort — callers treat absence as a standalone task).
//
// Token guard: if the snapshot's recorded token count already exceeds the
// compaction threshold, turns are dropped from the front (oldest first) until the
// estimated count fits within the budget. This is O(n) on turn count and requires
// no LLM call. The per-turn estimate is snapshotTokens/len(turns) — a rough
// average that is conservative enough for the guard's purpose.
func (f *Factory) fetchPriorTurns(conversationID, traceID string) ([]anthropic.MessageParam, int64) {
	if conversationID == "" {
		return nil, 0
	}
	records, err := f.memory.Read("conversation:"+conversationID, "conversation_snapshot")
	if err != nil || len(records) == 0 {
		if err != nil {
			f.log.Warn("factory: fetchPriorTurns failed", "conversation_id", conversationID, "error", err, "trace_id", traceID)
		}
		return nil, 0
	}

	// Deserialise all returned records and select the most recent snapshot by
	// WrittenAt. Memory Component return ordering is not guaranteed by the
	// interface contract, so we scan all records rather than assuming [0] is
	// the latest.
	var snap types.ConversationSnapshot
	for _, rec := range records {
		raw, mErr := json.Marshal(rec.Payload)
		if mErr != nil {
			f.log.Warn("factory: fetchPriorTurns: marshal record payload", "error", mErr, "trace_id", traceID)
			continue
		}
		var s types.ConversationSnapshot
		if uErr := json.Unmarshal(raw, &s); uErr != nil {
			f.log.Warn("factory: fetchPriorTurns: unmarshal snapshot", "error", uErr, "trace_id", traceID)
			continue
		}
		if s.WrittenAt.After(snap.WrittenAt) {
			snap = s
		}
	}
	if len(snap.Turns) == 0 {
		return nil, 0
	}

	turns := make([]anthropic.MessageParam, 0, len(snap.Turns))
	for _, rawTurn := range snap.Turns {
		var msg anthropic.MessageParam
		if err := json.Unmarshal(rawTurn, &msg); err != nil {
			f.log.Warn("factory: fetchPriorTurns: unmarshal turn", "error", err, "trace_id", traceID)
			return nil, 0
		}
		turns = append(turns, msg)
	}

	// Token guard: drop oldest turns until the snapshot fits within the
	// compaction budget. This prevents a long conversation from arriving
	// already over the 80% threshold before the new Instructions are added.
	budget := int64(float64(types.ModelContextWindow) * types.CompactThreshold)
	tokenEst := snap.TotalTokens
	for len(turns) > 1 && tokenEst > budget {
		tokenEst -= tokenEst / int64(len(turns))
		turns = turns[1:]
	}

	f.log.Info("factory: prior turns fetched",
		"conversation_id", conversationID,
		"turn_count", len(turns),
		"snapshot_tokens", snap.TotalTokens,
		"token_est_after_guard", tokenEst,
		"trace_id", traceID,
	)
	return turns, tokenEst
}

// joinMemoryPayloads extracts string payloads from MemoryWrite records and
// joins them as a newline-separated list, truncated to maxChars total.
func joinMemoryPayloads(records []types.MemoryWrite, maxChars int) string {
	var parts []string
	total := 0
	for _, r := range records {
		s, ok := r.Payload.(string)
		if !ok {
			// payload may be map[string]interface{} after JSON round-trip
			raw, err := json.Marshal(r.Payload)
			if err != nil {
				continue
			}
			s = string(raw)
		}
		if total+len(s) > maxChars {
			break
		}
		parts = append(parts, s)
		total += len(s) + 1
	}
	return strings.Join(parts, "\n")
}

// buildSpawnContextText assembles the full spawn-context string whose token count
// is measured against the spawnContextBudget. Components match those injected into
// the agent at spawn time: system prompt, task instructions, entry-point skill domain
// name, and the opaque permission token reference (EDD §13.3, PRD FR-FAC-05).
func buildSpawnContextText(systemPrompt, instructions, entryDomain, permTokenRef string) string {
	return systemPrompt + "\n" + instructions + "\n" + entryDomain + "\n" + permTokenRef
}

// ─── OQ-03 / OQ-06 — SUSPENDED state idle timeout and wake-up ───────────────

// StartIdleSweep starts the background goroutine that auto-suspends agents
// that have been IDLE for longer than IdleSuspendTimeout (OQ-03).
//
// Returns immediately when IdleSuspendTimeout is 0 (feature disabled).
// Runs until ctx is cancelled; safe to call from cmd/aegis-agents/main.go
// after factory.New.
func (f *Factory) StartIdleSweep(ctx context.Context) {
	if f.idleSuspendTimeout <= 0 {
		return // OQ-03 disabled — no sweep needed
	}
	f.log.Info("idle sweep started (OQ-03)",
		"idle_suspend_timeout", f.idleSuspendTimeout,
		"wake_latency_target", f.suspendWakeLatencyTarget,
	)
	// Sweep interval = 25% of the timeout so agents are never suspended more than
	// 1.25× the configured timeout late. Minimum 10 s to avoid busy-spinning on
	// very short (test) timeouts.
	interval := f.idleSuspendTimeout / 4
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				f.log.Info("idle sweep stopped")
				return
			case <-ticker.C:
				f.sweepIdleAgents()
			}
		}
	}()
}

// sweepIdleAgents scans the registry for agents that have exceeded the idle
// timeout and suspends each one. Called periodically by StartIdleSweep.
func (f *Factory) sweepIdleAgents() {
	agents := f.registry.List()
	deadline := time.Now().Add(-f.idleSuspendTimeout)
	for _, agent := range agents {
		if agent.State != registry.StateIdle {
			continue
		}
		// UpdatedAt is stamped when the agent entered IDLE.
		if agent.UpdatedAt.After(deadline) {
			continue // not yet timed out
		}
		f.log.Info("idle sweep: suspending agent",
			"agent_id", agent.AgentID,
			"idle_since", agent.UpdatedAt,
			"idle_duration", time.Since(agent.UpdatedAt).Round(time.Second),
		)
		if err := f.SuspendAgent(agent.AgentID, ""); err != nil {
			f.log.Warn("idle sweep: SuspendAgent failed",
				"agent_id", agent.AgentID,
				"error", err,
			)
		}
	}
}

// SuspendAgent transitions an IDLE agent to SUSPENDED, preserving its registry
// entry for future reuse while freeing it from crash monitoring (OQ-03).
//
// When live-process reuse is enabled, CompleteTask leaves the agent-process
// alive in IDLE so Priority 1 reuse can deliver a fresh SpawnContext without
// paying the cold-start tax. Suspension is the resource-reclamation fallback:
// if the process stays idle past IdleSuspendTimeout, we tear it down here so
// a future task reaches Priority 2 wake (fresh VM, fresh credentials). When
// reuse is disabled CompleteTask already terminated the VM, so this call is a
// no-op on the lifecycle side.
//
// Returns an error if the agent is not IDLE or does not exist.
func (f *Factory) SuspendAgent(agentID, traceID string) error {
	agent, err := f.registry.Get(agentID)
	if err != nil {
		return fmt.Errorf("factory: SuspendAgent: registry.Get: %w", err)
	}
	if agent.State != registry.StateIdle {
		return fmt.Errorf("factory: SuspendAgent: agent %q is %s (want %s)",
			agentID, agent.State, registry.StateIdle)
	}

	// Tear down the live agent-process, if any. On the reuse-disabled path
	// CompleteTask already called Terminate; the no-such-agent error returned
	// by the Manager in that case is expected and swallowed.
	if f.reuseEnabled {
		if err := f.lifecycle.Terminate(agentID); err != nil {
			f.log.Warn("SuspendAgent: lifecycle.Terminate failed — continuing",
				"agent_id", agentID,
				"trace_id", traceID,
				"error", err,
			)
		}
	}

	if err := f.registry.UpdateState(agentID, registry.StateSuspended, "idle timeout"); err != nil {
		return fmt.Errorf("factory: SuspendAgent: UpdateState: %w", err)
	}

	f.log.Info("agent suspended",
		"agent_id", agentID,
		"skill_domains", agent.SkillDomains,
		"trace_id", traceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventStateTransition,
		AgentID:   agentID,
		TaskID:    agent.AssignedTask,
		TraceID:   traceID,
		Details:   map[string]string{"state": registry.StateSuspended, "reason": "idle_timeout"},
	})

	if f.onSuspend != nil {
		f.onSuspend(agentID)
	}

	return f.publishStatus(agentID, agent.AssignedTask, registry.StateSuspended, traceID)
}

// wakeAgent transitions a SUSPENDED agent to ACTIVE for a new task (OQ-06).
//
// Wake sequence:
//  1. Issue a fresh credential.authorize (prior token expired during suspension).
//  2. Allocate a new VMID and update the registry.
//  3. Spawn a new agent process.
//  4. Transition SUSPENDED → ACTIVE via registry.AssignTask.
//
// The measured wake latency is logged against SuspendWakeLatencyTarget so the
// Platform team can verify the SLA is met in production.
func (f *Factory) wakeAgent(agentID string, spec *types.TaskSpec) error {
	wakeStart := time.Now()

	agent, err := f.registry.Get(agentID)
	if err != nil {
		return fmt.Errorf("factory: wakeAgent: registry.Get: %w", err)
	}
	if agent.State != registry.StateSuspended {
		return fmt.Errorf("factory: wakeAgent: agent %q is %s (want %s)",
			agentID, agent.State, registry.StateSuspended)
	}

	// Step 1: Fresh credential pre-authorization — the prior token expired during
	// suspension. Required before the new VM can be spawned (EDD §6.2, CLAUDE.md §5).
	token, err := f.credentials.PreAuthorize(agentID, spec.TaskID, spec.TraceID, agent.SkillDomains)
	if err != nil {
		f.log.Warn("credential.event",
			"operation_type", "authorize",
			"agent_id", agentID,
			"outcome", "failed",
			"trace_id", spec.TraceID,
		)
		if credDenied(err) {
			f.emit(types.AuditEvent{
				EventType: types.AuditEventCredentialDeny,
				AgentID:   agentID,
				TaskID:    spec.TaskID,
				TraceID:   spec.TraceID,
				Details:   map[string]string{"operation_type": "authorize", "error_code": credErrorCode(err)},
			})
		}
		_ = f.registry.UpdateState(agentID, registry.StateTerminated, "credential re-authorization failed on wake")
		_ = f.publishFailed(agentID, spec.TaskID, credErrorCode(err),
			"agent credential re-authorization failed on wake from suspended state", spec.TraceID, "wake")
		return fmt.Errorf("factory: wakeAgent: credentials.PreAuthorize: %w", err)
	}
	f.log.Info("credential.event",
		"operation_type", "authorize",
		"agent_id", agentID,
		"outcome", "granted",
		"trace_id", spec.TraceID,
	)
	f.emit(types.AuditEvent{
		EventType: types.AuditEventCredentialGrant,
		AgentID:   agentID,
		TaskID:    spec.TaskID,
		TraceID:   spec.TraceID,
		Details: map[string]string{
			"operation_type": "authorize",
			"skill_domains":  strings.Join(agent.SkillDomains, ","),
		},
	})

	// Step 2: Allocate a new VMID. The old VM was terminated when the agent
	// completed its last task and entered IDLE. A new VM identity is required.
	newVMID := f.generateID()
	if err := f.registry.SetVMID(agentID, newVMID); err != nil {
		return fmt.Errorf("factory: wakeAgent: registry.SetVMID: %w", err)
	}

	// Step 3: Spawn new agent process.
	entryDomain := ""
	if len(agent.SkillDomains) > 0 {
		entryDomain = agent.SkillDomains[0]
	}
	commands, err := f.skills.GetCommands(entryDomain)
	if err != nil {
		return fmt.Errorf("factory: wakeAgent: skills.GetCommands %q: %w", entryDomain, err)
	}
	manifest := buildManifestText(commands)
	agentMemory := f.fetchAgentMemory(entryDomain, spec.TraceID)
	userProfile := f.fetchUserProfile(spec.UserContextID, spec.TraceID)
	priorTurns, _ := f.fetchPriorTurns(spec.ConversationID, spec.TraceID)
	vmCfg := lifecycle.VMConfig{
		AgentID:         agentID,
		VMID:            newVMID,
		TaskID:          spec.TaskID,
		SkillDomain:     entryDomain,
		CredentialPtr:   token,
		Instructions:    spec.Instructions,
		CommandManifest: manifest,
		AgentMemory:     agentMemory,
		UserProfile:     userProfile,
		TraceID:         spec.TraceID,
		UserContextID:   spec.UserContextID,
		ConversationID:  spec.ConversationID,
		PriorTurns:      priorTurns,
		OnComplete:      f.processCompletionHandler(agentID),
	}
	if err := f.lifecycle.Spawn(vmCfg); err != nil {
		return fmt.Errorf("factory: wakeAgent: lifecycle.Spawn: %w", err)
	}

	// Step 4: SUSPENDED → ACTIVE. AssignTask links the task and validates the
	// state transition — StateSuspended → StateActive is permitted (EDD §6.2).
	if err := f.registry.AssignTask(agentID, spec.TaskID); err != nil {
		return fmt.Errorf("factory: wakeAgent: registry.AssignTask: %w", err)
	}

	// Resume crash monitoring for the freshly-spawned VM.
	if f.crashDetector != nil {
		f.crashDetector.Watch(agentID)
	}

	wakeLatency := time.Since(wakeStart)
	f.log.Info("agent woken from suspended state",
		"agent_id", agentID,
		"task_id", spec.TaskID,
		"wake_latency", wakeLatency.Round(time.Millisecond),
		"wake_latency_target", f.suspendWakeLatencyTarget,
		"within_target", wakeLatency <= f.suspendWakeLatencyTarget,
		"trace_id", spec.TraceID,
	)
	if wakeLatency > f.suspendWakeLatencyTarget && f.suspendWakeLatencyTarget > 0 {
		f.log.Warn("wake latency exceeded target (OQ-06)",
			"agent_id", agentID,
			"wake_latency", wakeLatency.Round(time.Millisecond),
			"target", f.suspendWakeLatencyTarget,
		)
	}

	if f.onWake != nil {
		f.onWake(agentID)
	}

	return f.publishStatus(agentID, spec.TaskID, registry.StateActive, spec.TraceID)
}
