// Package main — dynregistry.go provides a thread-safe, append-capable tool
// registry used by the ReAct loop to support runtime skill loading.
//
// The registry is initialised from the static toolsForDomain slice at the start
// of each RunLoop invocation. The skill_load tool holds a reference to the
// registry and appends newly loaded skills so they become available on the
// next Reason phase without restarting the agent.
package main

import (
	"fmt"
	"sync"
)

// DynamicRegistry is a thread-safe tool registry that can be extended at
// runtime. It is scoped to a single RunLoop invocation — skills loaded by
// one agent session are not visible to other agents or future sessions
// (persistence is out-of-scope for v1).
type DynamicRegistry struct {
	mu    sync.RWMutex
	tools []SkillTool
}

// newDynamicRegistry returns a registry pre-populated with the given tools.
func newDynamicRegistry(initial []SkillTool) *DynamicRegistry {
	cp := make([]SkillTool, len(initial))
	copy(cp, initial)
	return &DynamicRegistry{tools: cp}
}

// Register appends a SkillTool to the registry. Returns an error if a tool
// with the same name is already registered — this prevents skill_load from
// shadowing built-ins or previously loaded skills.
func (r *DynamicRegistry) Register(tool SkillTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tools {
		if t.Definition.Name == tool.Definition.Name {
			return fmt.Errorf("skill %q is already registered", tool.Definition.Name)
		}
	}
	r.tools = append(r.tools, tool)
	return nil
}

// Tools returns a snapshot of the current tool list. The returned slice is
// safe for concurrent use; mutations to it do not affect the registry.
func (r *DynamicRegistry) Tools() []SkillTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make([]SkillTool, len(r.tools))
	copy(cp, r.tools)
	return cp
}
