// Package skills is M4 — the Skill Hierarchy Manager. It owns the three-level
// skill tree (domain → command → spec) and enforces progressive disclosure:
// agents receive only domain names at spawn and drill down on demand.
package skills

import (
	"fmt"
	"sync"

	"github.com/cerberOS/agents-component/pkg/types"
)

// Manager is the interface for skill tree operations.
type Manager interface {
	// RegisterDomain adds a top-level domain node (and its full subtree) to the tree.
	RegisterDomain(node *types.SkillNode) error

	// GetDomain returns the domain node by name (level=domain, no children exposed).
	GetDomain(name string) (*types.SkillNode, error)

	// GetCommands returns the command-level children of a domain (level=command, no specs).
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

func (m *hierarchyManager) RegisterDomain(node *types.SkillNode) error {
	if node == nil || node.Name == "" {
		return fmt.Errorf("skills: domain node must have a non-empty name")
	}
	if node.Level != "domain" {
		return fmt.Errorf("skills: expected level=domain, got %q", node.Level)
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
// Agents receive only the domain name, not its commands.
func (m *hierarchyManager) GetDomain(name string) (*types.SkillNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[name]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", name)
	}
	return &types.SkillNode{Name: d.Name, Level: d.Level}, nil
}

// GetCommands returns command-level children of the domain without leaf specs.
func (m *hierarchyManager) GetCommands(domain string) ([]*types.SkillNode, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.domains[domain]
	if !ok {
		return nil, fmt.Errorf("skills: domain %q not found", domain)
	}

	commands := make([]*types.SkillNode, 0, len(d.Children))
	for _, cmd := range d.Children {
		commands = append(commands, &types.SkillNode{Name: cmd.Name, Level: cmd.Level})
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
	// Return a copy.
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
