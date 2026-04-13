package tests

import (
	"context"
	"time"
)

const cliPath = "../memory-cli"

type Fact struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type Message struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type AgentExecution struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type SystemEvent struct {
	ID        string `json:"id"`
	EventType string `json:"event_type"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

type VaultSecret struct {
	KeyName string `json:"key_name"`
}

func getBaseEnv() []string {
	return []string{
		"DB_USER=user",
		"DB_PASSWORD=password",
		"DB_NAME=memory_db",
		"DB_HOST=localhost",
		"DB_PORT=5432",
		"VAULT_MASTER_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func cliTestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
