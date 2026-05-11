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

// Replace swaps the existing tool entry whose name matches name with the
// provided replacement. Returns an error if no tool with that name is found.
// This is used by loop.go to upgrade skills_search with a registry-aware
// version after the DynamicRegistry is created, resolving the chicken-and-egg
// dependency between toolsForDomain and the registry.
func (r *DynamicRegistry) Replace(name string, tool SkillTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, t := range r.tools {
		if t.Definition.Name == name {
			r.tools[i] = tool
			return nil
		}
	}
	return fmt.Errorf("skill %q not found in registry; cannot replace", name)
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
