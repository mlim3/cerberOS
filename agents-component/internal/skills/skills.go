// Package skills is M4 — the Skill Hierarchy Manager. It owns the three-level
// skill tree (domain → command → spec) and enforces progressive disclosure:
// agents receive only domain names at spawn and drill down on demand.
//
// The manager also enforces the Tool Contract (EDD §13.2) at registration time:
// every command node must have a label, a description (≤300 chars), and all
// parameters must carry descriptions. Registrations that do not conform are
// rejected with a descriptive error.
//
// Semantic skill search is no longer performed in-process. Queries are routed
// through the Orchestrator to the Memory Component's pgvector index instead
// (see agents-component SessionLog.SearchSkills).
package skills

import (
	"fmt"
	"sync"

	"github.com/cerberOS/agents-component/pkg/types"
)

const (
	maxToolNameLen = 64  // §13.2: name max length
	maxDescLen     = 300 // §13.2: description max length
	maxTimeout     = 300 // §13.2: timeout_seconds hard max
)

// Manager is the interface for skill tree operations.
type Manager interface {
	// RegisterDomain adds a top-level domain node (and its full subtree) to the
	// tree. Rejects any command-level child that does not satisfy the Tool Contract.
	RegisterDomain(node *types.SkillNode) error

	// GetDomain returns the domain node by name (level=domain, no children exposed).
	GetDomain(name string) (*types.SkillNode, error)

	// GetCommands returns command-level children of a domain. Each node includes
	// label, description, required_credential_types, and timeout_seconds, but not
	// the parameter spec (progressive disclosure).
	GetCommands(domain string) ([]*types.SkillNode, error)

	// GetSpec returns the full parameter spec for a specific command.
	GetSpec(domain, command string) (*types.SkillSpec, error)

	// ListDomains returns the names of all registered domains.
	ListDomains() []string

	// RegisterCommand adds or replaces a command-level node within an existing
	// domain. Safe to call concurrently with reads. If a command with the same
	// name already exists it is replaced (upsert — later synthesis wins).
	// Returns an error if the domain does not exist or the node fails the Tool
	// Contract (EDD §13.2). Used to load synthesized skills at startup and to
	// accept new skills created during task execution.
	RegisterCommand(domain string, node *types.SkillNode) error

	// GetSynthesizedSkills returns the full SynthesizedSkillRecord for every
	// command in the domain whose Origin is "synthesized". Unlike GetCommands,
	// this returns the Recipe and Spec so the factory can pass these records to
	// the agent process at spawn time for dynamic tool construction.
	GetSynthesizedSkills(domain string) ([]types.SynthesizedSkillRecord, error)
}

// InvocationHook is called after each successful GetSpec call. It receives the
// domain and command names. Implementations must be non-blocking.
type InvocationHook func(domain, command string)

// Option configures a hierarchyManager.
type Option func(*hierarchyManager)

// WithGetSpecHook registers a callback fired after every successful GetSpec call.
// Pass metrics.Recorder.ObserveSkillInvocation here to drive the
// skill_invocations_total Prometheus counter.
func WithGetSpecHook(h InvocationHook) Option {
	return func(m *hierarchyManager) {
		m.onGetSpec = h
	}
}

// hierarchyManager is the default in-memory implementation.
type hierarchyManager struct {
	mu        sync.RWMutex
	domains   map[string]*types.SkillNode
	onGetSpec InvocationHook // optional; called after each successful GetSpec
}

// New returns a ready-to-use Skill Hierarchy Manager.
func New(opts ...Option) Manager {
	m := &hierarchyManager{
		domains: make(map[string]*types.SkillNode),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// RegisterDomain adds a domain and its command subtree. Every command-level child
// must pass ValidateCommandContract or the entire registration is rejected.
func (m *hierarchyManager) RegisterDomain(node *types.SkillNode) error {
	if node == nil || node.Name == "" {
		return fmt.Errorf("skills: domain node must have a non-empty name")
	}
	if node.Level != "domain" {
		return fmt.Errorf("skills: expected level=domain, got %q", node.Level)
	}

	for _, child := range node.Children {
		if child.Level == "command" {
			if err := ValidateCommandContract(child); err != nil {
				return fmt.Errorf("skills: domain %q registration rejected: %w", node.Name, err)
			}
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.domains[node.Name]; exists {
		return fmt.Errorf("skills: domain %q already registered", node.Name)
	}
	m.domains[node.Name] = node
	return nil
}

// GetDomain returns a shallow copy of the domain node with no children exposed.
func (m *hierarchyManager) GetDomain(name string) (*types.SkillNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[name]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", name)
	}
	return &types.SkillNode{Name: d.Name, Level: d.Level}, nil
}

// GetCommands returns command-level metadata for the domain. Each returned node
// includes label, description, required_credential_types, and timeout_seconds;
// parameter spec is withheld until GetSpec is called (progressive disclosure).
func (m *hierarchyManager) GetCommands(domain string) ([]*types.SkillNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[domain]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}

	commands := make([]*types.SkillNode, 0, len(d.Children))
	for _, cmd := range d.Children {
		commands = append(commands, &types.SkillNode{
			Name:                    cmd.Name,
			Level:                   cmd.Level,
			Label:                   cmd.Label,
			Description:             cmd.Description,
			RequiredCredentialTypes: cmd.RequiredCredentialTypes,
			TimeoutSeconds:          cmd.TimeoutSeconds,
			Origin:                  cmd.Origin,
			OwnerUserID:             cmd.OwnerUserID,
			Scope:                   cmd.Scope,
		})
	}
	return commands, nil
}

// GetSpec returns the full parameter spec for a command.
func (m *hierarchyManager) GetSpec(domain, command string) (*types.SkillSpec, error) {
	m.mu.RLock()

	d, ok := m.domains[domain]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}
	cmd, ok := d.Children[command]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("skills: command %q not found in domain %q", command, domain)
	}
	if cmd.Spec == nil {
		m.mu.RUnlock()
		return nil, fmt.Errorf("skills: no spec defined for %q.%q", domain, command)
	}
	spec := *cmd.Spec
	hook := m.onGetSpec
	m.mu.RUnlock()

	if hook != nil {
		hook(domain, command)
	}
	return &spec, nil
}

func (m *hierarchyManager) ListDomains() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.domains))
	for n := range m.domains {
		names = append(names, n)
	}
	return names
}

// RegisterCommand adds or replaces a single command node within an existing domain.
func (m *hierarchyManager) RegisterCommand(domain string, node *types.SkillNode) error {
	if node == nil {
		return fmt.Errorf("skills: command node must not be nil")
	}
	if node.Level != "command" {
		return fmt.Errorf("skills: RegisterCommand: expected level=command, got %q", node.Level)
	}
	if err := ValidateCommandContract(node); err != nil {
		return fmt.Errorf("skills: RegisterCommand rejected: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	d, ok := m.domains[domain]
	if !ok {
		return fmt.Errorf("skills: domain %q not found", domain)
	}
	if d.Children == nil {
		d.Children = make(map[string]*types.SkillNode)
	}
	d.Children[node.Name] = node
	return nil
}

// GetSynthesizedSkills returns the full SynthesizedSkillRecord for every command
// in the domain whose Origin is "synthesized".
func (m *hierarchyManager) GetSynthesizedSkills(domain string) ([]types.SynthesizedSkillRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[domain]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}

	var records []types.SynthesizedSkillRecord
	for _, cmd := range d.Children {
		if cmd.Origin != "synthesized" {
			continue
		}
		records = append(records, types.SynthesizedSkillRecord{
			Name:        cmd.Name,
			Description: cmd.Description,
			Recipe:      cmd.Recipe,
			Spec:        cmd.Spec,
			OwnerUserID: cmd.OwnerUserID,
			Scope:       cmd.Scope,
		})
	}
	return records, nil
}

// ValidateCommandContract enforces the Tool Contract from EDD §13.2 for a single
// command-level SkillNode. Returns a descriptive error for every violation found.
func ValidateCommandContract(node *types.SkillNode) error {
	if node.Name == "" {
		return fmt.Errorf("contract violation: command name is required")
	}
	if len(node.Name) > maxToolNameLen {
		return fmt.Errorf("contract violation: command name %q exceeds %d characters (%d)",
			node.Name, maxToolNameLen, len(node.Name))
	}
	if node.Label == "" {
		return fmt.Errorf("contract violation: label is required for command %q", node.Name)
	}
	if node.Description == "" {
		return fmt.Errorf("contract violation: description is required for command %q", node.Name)
	}
	if len(node.Description) > maxDescLen {
		return fmt.Errorf("contract violation: description for command %q exceeds %d characters (%d)",
			node.Name, maxDescLen, len(node.Description))
	}
	if node.Spec != nil {
		for paramName, param := range node.Spec.Parameters {
			if param.Description == "" {
				return fmt.Errorf("contract violation: parameter %q in command %q has no description",
					paramName, node.Name)
			}
		}
	}
	if node.TimeoutSeconds < 0 || node.TimeoutSeconds > maxTimeout {
		return fmt.Errorf("contract violation: timeout_seconds for command %q must be 0–%d, got %d",
			node.Name, maxTimeout, node.TimeoutSeconds)
	}
	return nil
}
