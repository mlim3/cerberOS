package skills

import (
	"fmt"

	"github.com/cerberOS/agents-component/pkg/types"
)

// ReloadResult summarises which domains changed during a Reload call.
type ReloadResult struct {
	Added    []string // domains present in new tree but not old
	Removed  []string // domains present in old tree but not new
	Modified []string // domains present in both whose command sets changed
}

// Reloader is the hot-reload interface. It is implemented by hierarchyManager
// so callers can atomically swap the full skill tree at runtime (e.g. after
// reloading a skills YAML) without restarting the process.
type Reloader interface {
	// Reload atomically replaces the live skill tree with the supplied nodes.
	// All supplied domain nodes must pass ValidateCommandContract for every
	// command child, or the call returns an error and leaves the tree intact.
	// Returns a ReloadResult describing what changed.
	Reload(nodes []*types.SkillNode) (ReloadResult, error)
}

// Reload implements Reloader. It validates the new tree, then atomically
// replaces the domain map. Unchanged domains are preserved; missing ones are
// removed; new or modified ones are updated.
func (m *hierarchyManager) Reload(nodes []*types.SkillNode) (ReloadResult, error) {
	// Validate all incoming nodes before touching live state.
	newDomains := make(map[string]*types.SkillNode, len(nodes))
	for _, node := range nodes {
		if node == nil || node.Name == "" {
			return ReloadResult{}, fmt.Errorf("skills: reload: domain node must have a non-empty name")
		}
		if node.Level != "domain" {
			return ReloadResult{}, fmt.Errorf("skills: reload: expected level=domain, got %q", node.Level)
		}
		for _, child := range node.Children {
			if child.Level == "command" {
				if err := ValidateCommandContract(child); err != nil {
					return ReloadResult{}, fmt.Errorf("skills: reload: domain %q rejected: %w", node.Name, err)
				}
			}
		}
		newDomains[node.Name] = node
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var result ReloadResult

	// Detect removals and modifications.
	for name, old := range m.domains {
		if _, exists := newDomains[name]; !exists {
			result.Removed = append(result.Removed, name)
		} else {
			if domainChanged(old, newDomains[name]) {
				result.Modified = append(result.Modified, name)
			}
		}
	}

	// Detect additions.
	for name := range newDomains {
		if _, exists := m.domains[name]; !exists {
			result.Added = append(result.Added, name)
		}
	}

	m.domains = newDomains
	return result, nil
}

// domainChanged returns true if the two domain nodes differ in any way that
// affects the observable skill tree (command set or any command field).
func domainChanged(a, b *types.SkillNode) bool {
	if len(a.Children) != len(b.Children) {
		return true
	}
	for name, ca := range a.Children {
		cb, ok := b.Children[name]
		if !ok {
			return true
		}
		if ca.Description != cb.Description || ca.Label != cb.Label || ca.TimeoutSeconds != cb.TimeoutSeconds {
			return true
		}
	}
	return false
}
