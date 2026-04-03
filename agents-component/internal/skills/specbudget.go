// Package skills — specbudget.go implements a per-agent context-window budget
// for SkillSpec entries, evicting the least-recently-used specs when the
// configured token ceiling is reached.
//
// Design notes:
//   - The budget tracks token cost only; it does not store spec content.
//     Callers supply the pre-computed token cost via LoadSpec.
//   - Eviction is strictly LRU: the entry that was least recently loaded or
//     touched is evicted first. Pinning is the caller's responsibility
//     (e.g. always-present tools such as task_complete should not be loaded
//     into the budget).
//   - EstimateSpecTokens is provided as a standalone utility for callers who
//     want a consistent cost estimate from a *types.SkillSpec.
//   - SpecBudget is NOT safe for concurrent use. Agent processes run a
//     single-goroutine ReAct loop; each agent owns exactly one instance.
package skills

import (
	"container/list"
	"encoding/json"
	"fmt"

	"github.com/cerberOS/agents-component/pkg/types"
)

// SpecKey uniquely identifies a command's parameter spec within a SpecBudget.
type SpecKey struct {
	Domain  string
	Command string
}

// EvictedSpec is returned by LoadSpec for each entry removed to make room for
// the incoming spec. The Tokens field reflects the cost freed by the eviction.
type EvictedSpec struct {
	Domain  string
	Command string
	Tokens  int
}

// SpecBudget tracks the cumulative token cost of SkillSpecs loaded into a
// single agent's context window and evicts the least-recently-used entries
// when the configured ceiling is reached.
//
// Eviction removes a spec from the budget (and therefore from the agent's
// active context), but it remains in the Skill Hierarchy Manager and can be
// re-fetched via GetSpec on demand.
//
// Not safe for concurrent use — each agent owns exactly one SpecBudget,
// accessed from its single-goroutine ReAct loop.
type SpecBudget struct {
	ceiling int
	used    int
	order   *list.List                // front = MRU, back = LRU
	index   map[SpecKey]*list.Element // O(1) lookup by key
}

// budgetEntry is the value stored in each list.Element.
type budgetEntry struct {
	key    SpecKey
	tokens int
}

// NewSpecBudget returns a SpecBudget with the given token ceiling.
// ceiling must be ≥ 1.
func NewSpecBudget(ceiling int) (*SpecBudget, error) {
	if ceiling < 1 {
		return nil, fmt.Errorf("specbudget: ceiling must be ≥ 1, got %d", ceiling)
	}
	return &SpecBudget{
		ceiling: ceiling,
		order:   list.New(),
		index:   make(map[SpecKey]*list.Element),
	}, nil
}

// LoadSpec registers a spec with its pre-computed token cost.
//
//   - If the spec is already loaded, it is promoted to most-recently-used and
//     no evictions occur (idempotent re-load).
//   - If used + tokens > ceiling, LRU entries are evicted one at a time until
//     enough room exists.
//   - Returns an error only when tokens alone exceeds the ceiling (the spec can
//     never fit regardless of eviction).
//
// tokens must be ≥ 0. A zero-cost spec is accepted and never causes eviction
// (it contributes nothing to the used counter).
func (b *SpecBudget) LoadSpec(domain, command string, tokens int) ([]EvictedSpec, error) {
	if tokens < 0 {
		return nil, fmt.Errorf("specbudget: tokens must be ≥ 0, got %d", tokens)
	}
	if tokens > b.ceiling {
		return nil, fmt.Errorf(
			"specbudget: spec %q.%q costs %d tokens which exceeds ceiling %d (can never fit)",
			domain, command, tokens, b.ceiling,
		)
	}

	key := SpecKey{Domain: domain, Command: command}

	// Already loaded — promote to MRU; idempotent, no evictions.
	if elem, ok := b.index[key]; ok {
		b.order.MoveToFront(elem)
		return nil, nil
	}

	// Evict LRU entries until there is enough room.
	var evicted []EvictedSpec
	for b.used+tokens > b.ceiling && b.order.Len() > 0 {
		lru := b.order.Back()
		entry := lru.Value.(budgetEntry)
		b.order.Remove(lru)
		delete(b.index, entry.key)
		b.used -= entry.tokens
		evicted = append(evicted, EvictedSpec{
			Domain:  entry.key.Domain,
			Command: entry.key.Command,
			Tokens:  entry.tokens,
		})
	}

	// Insert at MRU position.
	elem := b.order.PushFront(budgetEntry{key: key, tokens: tokens})
	b.index[key] = elem
	b.used += tokens

	return evicted, nil
}

// Touch promotes an already-loaded spec to most-recently-used position,
// deferring its eviction. No-ops if the spec is not currently loaded.
func (b *SpecBudget) Touch(domain, command string) {
	if elem, ok := b.index[SpecKey{Domain: domain, Command: command}]; ok {
		b.order.MoveToFront(elem)
	}
}

// Remove explicitly unloads a spec, freeing its token budget immediately.
// No-ops if the spec is not currently loaded.
func (b *SpecBudget) Remove(domain, command string) {
	key := SpecKey{Domain: domain, Command: command}
	if elem, ok := b.index[key]; ok {
		entry := elem.Value.(budgetEntry)
		b.order.Remove(elem)
		delete(b.index, key)
		b.used -= entry.tokens
	}
}

// IsLoaded reports whether the given spec is currently in the budget.
func (b *SpecBudget) IsLoaded(domain, command string) bool {
	_, ok := b.index[SpecKey{Domain: domain, Command: command}]
	return ok
}

// Loaded returns the currently loaded specs ordered from most- to
// least-recently-used. The returned slice is a snapshot; mutations to the
// budget do not affect it.
func (b *SpecBudget) Loaded() []SpecKey {
	keys := make([]SpecKey, 0, b.order.Len())
	for elem := b.order.Front(); elem != nil; elem = elem.Next() {
		keys = append(keys, elem.Value.(budgetEntry).key)
	}
	return keys
}

// Used returns the current total token cost of all loaded specs.
func (b *SpecBudget) Used() int { return b.used }

// Remaining returns the remaining token budget (ceiling − used).
func (b *SpecBudget) Remaining() int { return b.ceiling - b.used }

// Ceiling returns the configured token ceiling.
func (b *SpecBudget) Ceiling() int { return b.ceiling }

// EstimateSpecTokens estimates the LLM context token cost of a SkillSpec by
// marshalling it to JSON and dividing the byte length by 4. This is a
// conservative approximation for short technical descriptions (~4 bytes per
// token for English text). Returns 1 for nil or zero-length specs.
func EstimateSpecTokens(spec *types.SkillSpec) int {
	if spec == nil {
		return 1
	}
	data, err := json.Marshal(spec)
	if err != nil || len(data) == 0 {
		return 1
	}
	if cost := len(data) / 4; cost >= 1 {
		return cost
	}
	return 1
}
