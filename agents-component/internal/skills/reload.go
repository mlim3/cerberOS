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

	// Phase 2 — Build the replacement domains map (no IO — fast).
	newDomains := make(map[string]*types.SkillNode, len(domains))
	for _, n := range domains {
		newDomains[n.Name] = n
	}

	// Phase 3 — Snapshot current state under a read lock.
	// Capturing both domains (for the diff) and embeddings (for the
	// incremental re-embedding step below — unchanged commands reuse their
	// existing vectors without calling the embedder).
	m.mu.RLock()
	oldDomains := m.domains
	oldEmbeddings := m.embeddings
	m.mu.RUnlock()

	result := diffDomains(oldDomains, newDomains)

	// Phase 4 — Compute embeddings incrementally outside the write lock so
	// slow embedder calls (e.g. remote Voyage AI) do not stall readers.
	// Only commands whose (domain, name, description) tuple has changed since
	// the last load call the embedder; all others reuse their existing vector.
	newEmbeddings := m.buildIncrementalEmbeddings(domains, oldEmbeddings)

	// Phase 5 — Atomic swap. Readers see either the full old state or the
	// full new state — never a partial mix.
	m.mu.Lock()
	m.domains = newDomains
	m.embeddings = newEmbeddings
	m.mu.Unlock()

	return result, nil
}

// embeddingCacheKey returns a unique key for a (domain, command, description)
// triple. Used by buildIncrementalEmbeddings to detect unchanged commands.
// The NUL byte separator is safe because no field may contain NUL.
func embeddingCacheKey(domain, name, description string) string {
	return domain + "\x00" + name + "\x00" + description
}

// buildIncrementalEmbeddings constructs the new embedding slice for domains,
// reusing existing vectors for commands whose (domain, name, description)
// triple is unchanged. Only genuinely new or modified commands call the
// embedder — in a single batch when BatchEmbedder is available.
// Called outside the write lock; safe for concurrent reads.
func (m *hierarchyManager) buildIncrementalEmbeddings(
	domains []*types.SkillNode, oldEmbs []commandEmbedding,
) []commandEmbedding {
	// Index existing vectors by cache key.
	cache := make(map[string][]float64, len(oldEmbs))
	for _, e := range oldEmbs {
		cache[embeddingCacheKey(e.domain, e.name, e.description)] = e.vector
	}

	type pending struct {
		domain string
		name   string
		desc   string
		text   string
	}
	var result []commandEmbedding
	var toEmbed []pending

	for _, node := range domains {
		for _, cmd := range node.Children {
			if cmd.Level != "command" {
				continue
			}
			key := embeddingCacheKey(node.Name, cmd.Name, cmd.Description)
			if vec, ok := cache[key]; ok {
				// Unchanged — reuse without calling the embedder.
				result = append(result, commandEmbedding{
					domain:      node.Name,
					name:        cmd.Name,
					description: cmd.Description,
					vector:      vec,
				})
			} else {
				toEmbed = append(toEmbed, pending{
					domain: node.Name,
					name:   cmd.Name,
					desc:   cmd.Description,
					text:   cmd.Name + " " + cmd.Description,
				})
			}
		}
	}

	if len(toEmbed) == 0 {
		return result
	}

	// Batch-embed all new/changed commands in a single call when possible.
	texts := make([]string, len(toEmbed))
	for i, p := range toEmbed {
		texts[i] = p.text
	}
	vecs := m.embedTexts(texts)
	for i, p := range toEmbed {
		if i < len(vecs) && vecs[i] != nil {
			result = append(result, commandEmbedding{
				domain:      p.domain,
				name:        p.name,
				description: p.desc,
				vector:      vecs[i],
			})
		}
		// Non-fatal: commands with nil vectors are excluded from search
		// results but structural queries (GetSpec etc.) still work.
	}
	return result
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
