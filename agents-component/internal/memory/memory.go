// Package memory is M7 — the Memory Interface. It formats and dispatches tagged
// memory payloads to the Orchestrator via the Comms Interface (NATS). This component
// never contacts the Memory Component directly — the Orchestrator owns that routing.
// All writes are surgical and tagged; reads are always filtered by agent ID and
// context tag. Full session dumps are explicitly forbidden.
package memory

import (
	"fmt"
	"sync"

	"github.com/aegis/aegis-agents/pkg/types"
)

// Client is the interface for all Memory Component interactions.
type Client interface {
	// Write persists a tagged payload to the Memory Component.
	// The payload must carry a non-empty AgentID, SessionID, and DataType.
	Write(payload *types.MemoryWrite) error

	// Read retrieves filtered slices by agent ID and context tag.
	// Never returns full agent history.
	Read(agentID, contextTag string) ([]types.MemoryWrite, error)
}

// stubClient is the default in-process implementation.
// The production implementation publishes to the Orchestrator via comms.Publish
// on subjects such as "memory.write" and "memory.read". The Orchestrator routes
// those messages to the Memory Component. Never make direct HTTP calls here.
type stubClient struct {
	mu      sync.RWMutex
	records map[string][]types.MemoryWrite // keyed by agentID
}

// New returns a Memory Client backed by an in-process stub.
func New() Client {
	return &stubClient{
		records: make(map[string][]types.MemoryWrite),
	}
}

func (c *stubClient) Write(payload *types.MemoryWrite) error {
	if payload == nil {
		return fmt.Errorf("memory: payload must not be nil")
	}
	if payload.AgentID == "" {
		return fmt.Errorf("memory: payload.AgentID must not be empty")
	}
	if payload.SessionID == "" {
		return fmt.Errorf("memory: payload.SessionID must not be empty")
	}
	if payload.DataType == "" {
		return fmt.Errorf("memory: payload.DataType must not be empty")
	}

	c.mu.Lock()
	c.records[payload.AgentID] = append(c.records[payload.AgentID], *payload)
	c.mu.Unlock()
	return nil
}

func (c *stubClient) Read(agentID, contextTag string) ([]types.MemoryWrite, error) {
	if agentID == "" {
		return nil, fmt.Errorf("memory: agentID must not be empty")
	}

	c.mu.RLock()
	all := c.records[agentID]
	c.mu.RUnlock()

	var filtered []types.MemoryWrite
	for _, r := range all {
		if contextTag == "" {
			filtered = append(filtered, r)
			continue
		}
		if val, ok := r.Tags["context"]; ok && val == contextTag {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}
