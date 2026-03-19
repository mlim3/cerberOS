// Package skills is M4 — the Skill Hierarchy Manager. It owns the three-level
// skill tree (domain → command → spec) and enforces progressive disclosure:
// agents receive only domain names at spawn and drill down on demand.
//
// The manager also enforces the Tool Contract (EDD §13.2) at registration time:
// every command node must have a label, a description (≤300 chars), and all
// parameters must carry descriptions. Registrations that do not conform are
// rejected with a descriptive error.
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
}

// hierarchyManager is the default in-memory implementation.
type hierarchyManager struct {
	mu      sync.RWMutex
	domains map[string]*types.SkillNode
}

// New returns a ready-to-use Skill Hierarchy Manager.
func New() Manager {
	return &hierarchyManager{
		domains: make(map[string]*types.SkillNode),
	}
}

// RegisterDomain adds a domain and its command subtree. Every command-level child
// must pass validateCommandContract or the entire registration is rejected.
func (m *hierarchyManager) RegisterDomain(node *types.SkillNode) error {
	if node == nil || node.Name == "" {
		return fmt.Errorf("skills: domain node must have a non-empty name")
	}
	if node.Level != "domain" {
		return fmt.Errorf("skills: expected level=domain, got %q", node.Level)
	}

	// Validate the Tool Contract for every command-level child before accepting
	// the registration. This is the enforcement point described in EDD §13.2.
	for _, child := range node.Children {
		if child.Level == "command" {
			if err := validateCommandContract(child); err != nil {
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
// Agents receive only the domain name at spawn — not its commands.
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
// includes label, description, required_credential_types, and timeout_seconds so
// callers can present the command manifest — but the parameter spec is withheld
// until GetSpec is called (progressive disclosure).
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
			// Spec intentionally withheld — call GetSpec for parameter detail.
		})
	}
	return commands, nil
}

// GetSpec returns the full parameter spec for a command. This is the deepest
// level of disclosure and is only served when the agent is constructing a call.
func (m *hierarchyManager) GetSpec(domain, command string) (*types.SkillSpec, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[domain]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}
	cmd, ok := d.Children[command]
	if !ok {
		return nil, fmt.Errorf("skills: command %q not found in domain %q", command, domain)
	}
	if cmd.Spec == nil {
		return nil, fmt.Errorf("skills: no spec defined for %q.%q", domain, command)
	}
	spec := *cmd.Spec
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

// validateCommandContract enforces the Tool Contract from EDD §13.2 for a single
// command-level SkillNode. Returns a descriptive error for every violation found.
func validateCommandContract(node *types.SkillNode) error {
	// name: required, max 64 chars.
	if node.Name == "" {
		return fmt.Errorf("contract violation: command name is required")
	}
	if len(node.Name) > maxToolNameLen {
		return fmt.Errorf("contract violation: command name %q exceeds %d characters (%d)",
			node.Name, maxToolNameLen, len(node.Name))
	}

	// label: required — used in monitoring and audit logs, never shown to the LLM.
	if node.Label == "" {
		return fmt.Errorf("contract violation: label is required for command %q", node.Name)
	}

	// description: required, max 300 chars.
	if node.Description == "" {
		return fmt.Errorf("contract violation: description is required for command %q", node.Name)
	}
	if len(node.Description) > maxDescLen {
		return fmt.Errorf("contract violation: description for command %q exceeds %d characters (%d)",
			node.Name, maxDescLen, len(node.Description))
	}

	// parameters: every parameter must have a description.
	// Parameters without descriptions cause LLM hallucination (§13.2).
	if node.Spec != nil {
		for paramName, param := range node.Spec.Parameters {
			if param.Description == "" {
				return fmt.Errorf("contract violation: parameter %q in command %q has no description",
					paramName, node.Name)
			}
		}
	}

	// timeout_seconds: optional, but if set must be within the allowed range.
	if node.TimeoutSeconds < 0 || node.TimeoutSeconds > maxTimeout {
		return fmt.Errorf("contract violation: timeout_seconds for command %q must be 0–%d, got %d",
			node.Name, maxTimeout, node.TimeoutSeconds)
	}

	return nil
}
