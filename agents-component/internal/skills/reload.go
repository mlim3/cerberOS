// Package skills — reload.go implements the Reloader extension interface that
// supports hot-reloading the skill tree without restarting the component.
//
// Design constraints:
//   - Validation is completed before any shared state is touched. A single
//     invalid command rejects the entire reload, leaving the live tree intact.
//   - Embeddings for all commands are pre-computed outside the write lock so
//     slow embedding calls (e.g. remote voyage model) do not stall concurrent
//     skill queries.
//   - The atomic swap under the write lock guarantees that concurrent readers
//     see either the full old tree or the full new tree — never a partial mix.
//   - Running agents that obtained a skill spec via GetSpec hold a value copy;
//     the swap does not affect them mid-task.
package skills

import (
	"fmt"
	"sort"

	"github.com/cerberOS/agents-component/pkg/types"
)

// Reloader is an optional capability of a Manager that supports atomic
// hot-reloading of the skill tree on SIGHUP.
//
// The concrete *hierarchyManager returned by New() implements Reloader. Callers
// should type-assert at the point of use:
//
//	if r, ok := mgr.(skills.Reloader); ok {
//	    result, err := r.Reload(nodes)
//	}
type Reloader interface {
	// Reload atomically replaces the entire skill tree with the supplied domain
	// nodes. All Tool Contract validations are applied before any change is
	// committed — if any node fails validation the existing tree is preserved
	// and a descriptive error is returned.
	//
	// Embeddings for all commands are recomputed outside the write lock so that
	// slow embedders do not stall concurrent GetCommands or Search calls.
	//
	// Changes take effect immediately for all new skill queries once Reload
	// returns. Running agents that already hold a GetSpec copy are unaffected.
	Reload(domains []*types.SkillNode) (ReloadResult, error)
}

// ReloadResult summarises the diff applied by a Reload call. Fields are sorted
// alphabetically for deterministic logging and testing.
type ReloadResult struct {
	Added    []string // domain names that were not in the previous tree
	Removed  []string // domain names that were in the previous tree but are now absent
	Modified []string // domain names whose command set or any command field changed
}

// Reload implements Reloader on hierarchyManager.
func (m *hierarchyManager) Reload(domains []*types.SkillNode) (ReloadResult, error) {
	// Phase 1 — Validate all incoming nodes before touching shared state.
	// Any violation causes a full rejection so the live tree is never left in
	// a partially-updated state.
	for _, node := range domains {
		if node == nil || node.Name == "" {
			return ReloadResult{}, fmt.Errorf("skills reload: domain node must have a non-empty name")
		}
		if node.Level != "domain" {
			return ReloadResult{}, fmt.Errorf("skills reload: %q must have level=domain, got %q", node.Name, node.Level)
		}
		for _, child := range node.Children {
			if child.Level == "command" {
				if err := validateCommandContract(child); err != nil {
					return ReloadResult{}, fmt.Errorf("skills reload rejected: %w", err)
				}
			}
		}
	}

	// Phase 2 — Build the replacement domains map and pre-compute embeddings.
	// Both happen outside the write lock so a slow embedding call does not
	// block concurrent readers.
	newDomains := make(map[string]*types.SkillNode, len(domains))
	for _, n := range domains {
		newDomains[n.Name] = n
	}

	var newEmbeddings []commandEmbedding
	for _, node := range domains {
		newEmbeddings = append(newEmbeddings, m.buildEmbeddings(node)...)
	}

	// Phase 3 — Snapshot current domains under a read lock to compute the diff.
	// The diff is informational (returned to the caller for logging); it does
	// not affect correctness of the swap.
	m.mu.RLock()
	oldDomains := m.domains
	m.mu.RUnlock()

	result := diffDomains(oldDomains, newDomains)

	// Phase 4 — Atomic swap. Readers that hold the read lock see either the
	// full old state or the full new state — never a partial mix.
	m.mu.Lock()
	m.domains = newDomains
	m.embeddings = newEmbeddings
	m.mu.Unlock()

	return result, nil
}

// diffDomains computes the added/removed/modified sets between two domain maps.
// Fields in the result are sorted for deterministic output.
func diffDomains(old, cur map[string]*types.SkillNode) ReloadResult {
	var r ReloadResult
	for name := range cur {
		if _, exists := old[name]; !exists {
			r.Added = append(r.Added, name)
		}
	}
	for name := range old {
		if _, exists := cur[name]; !exists {
			r.Removed = append(r.Removed, name)
		}
	}
	for name, newNode := range cur {
		if oldNode, exists := old[name]; exists {
			if domainCommandsChanged(oldNode, newNode) {
				r.Modified = append(r.Modified, name)
			}
		}
	}
	sort.Strings(r.Added)
	sort.Strings(r.Removed)
	sort.Strings(r.Modified)
	return r
}

// domainCommandsChanged returns true when the command set or any command field
// (Description, Label, TimeoutSeconds) differs between the two domain nodes.
// A description change is the primary trigger for embedding recomputation.
func domainCommandsChanged(oldNode, newNode *types.SkillNode) bool {
	if len(oldNode.Children) != len(newNode.Children) {
		return true
	}
	for name, newCmd := range newNode.Children {
		oldCmd, ok := oldNode.Children[name]
		if !ok {
			return true // command added
		}
		if oldCmd.Description != newCmd.Description ||
			oldCmd.Label != newCmd.Label ||
			oldCmd.TimeoutSeconds != newCmd.TimeoutSeconds {
			return true
		}
	}
	return false
}
