package main

import (
	"context"

	"github.com/google/uuid"
)

// Fact represents a stored piece of personal information
type Fact struct {
	ID      uuid.UUID `json:"id"`
	Content string    `json:"content"`
}

// Message represents a chat message
type Message struct {
	ID        uuid.UUID `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt string    `json:"created_at"`
}

// AgentExecution represents a task execution record
type AgentExecution struct {
	ID        uuid.UUID `json:"id"`
	TaskID    uuid.UUID `json:"task_id"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
}

// SystemEvent represents a system-level event
type SystemEvent struct {
	ID        uuid.UUID `json:"id"`
	EventType string    `json:"event_type"`
	Message   string    `json:"message"`
	CreatedAt string    `json:"created_at"`
}

// VaultSecret represents a vault key reference (values are not exposed via CLI for security usually, or only raw depending on the need)
type VaultSecret struct {
	KeyName string `json:"key_name"`
}

// MemoryClient defines the interface for interacting with the memory service
// This allows us to swap between Direct DB connection and HTTP API connection seamlessly.
type MemoryClient interface {
	// Facts
	QueryFacts(ctx context.Context, userID uuid.UUID, query string, topK int) ([]Fact, error)
	GetAllFacts(ctx context.Context, userID uuid.UUID) ([]Fact, error)
	SaveFact(ctx context.Context, userID uuid.UUID, fact string) error

	// Chat
	GetChatHistory(ctx context.Context, sessionID uuid.UUID, limit int) ([]Message, error)

	// Agent
	GetAgentExecutions(ctx context.Context, taskID uuid.UUID, limit int) ([]AgentExecution, error)

	// System
	GetSystemEvents(ctx context.Context, limit int) ([]SystemEvent, error)

	// Vault (Internal only typically, but useful for CLI admins)
	ListVaultSecrets(ctx context.Context, userID uuid.UUID) ([]VaultSecret, error)

	// Close cleans up resources (e.g., db connection pools)
	Close() error
}
