package tests

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// NOTE: These tests require the memory-cli binary to be built and
// the local postgres database to be running via docker-compose (mem-up.sh).

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

func TestCLIFactsQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "facts", "query", "--user", "11111111-1111-1111-1111-111111111111", "programming")
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	var facts []Fact
	if err := json.Unmarshal(output, &facts); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, string(output))
	}

	if len(facts) > 0 {
		if facts[0].ID == "" || facts[0].Content == "" {
			t.Errorf("Expected valid fact fields, got: %+v", facts[0])
		}
	}
}

func TestCLIFactsAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "facts", "all", "--user", "11111111-1111-1111-1111-111111111111")
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	var facts []Fact
	if err := json.Unmarshal(output, &facts); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, string(output))
	}

	if len(facts) > 0 {
		if facts[0].ID == "" || facts[0].Content == "" {
			t.Errorf("Expected valid fact fields, got: %+v", facts[0])
		}
	}
}

func TestCLIFactsSave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "facts", "save", "--user", "11111111-1111-1111-1111-111111111111", "I love writing Go code.")
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	// For save, we expect a success string, not JSON
	if string(output) != "Fact saved successfully.\n" {
		t.Fatalf("Unexpected output: %s", string(output))
	}
}

func TestCLIChatHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "chat", "history", "--session", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	var messages []Message
	if err := json.Unmarshal(output, &messages); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, string(output))
	}

	if len(messages) > 0 {
		if messages[0].ID == "" || messages[0].Role == "" || messages[0].Content == "" {
			t.Errorf("Expected valid message fields, got: %+v", messages[0])
		}
	}
}

func TestCLIAgentHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "agent", "history", "--task", "11111111-1111-1111-1111-111111111111")
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	var executions []AgentExecution
	if err := json.Unmarshal(output, &executions); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, string(output))
	}
}

func TestCLISystemEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "system", "events")
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	var events []SystemEvent
	if err := json.Unmarshal(output, &events); err != nil {
		t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, string(output))
	}
}

func TestCLIVaultList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Should fail with a clear message in DB mode
	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "vault", "list", "--user", "11111111-1111-1111-1111-111111111111")
	cmd.Env = getBaseEnv()

	_, err := cmd.Output()
	if err == nil {
		t.Fatalf("Expected CLI command to fail for vault in DB mode, but it succeeded")
	}
}
