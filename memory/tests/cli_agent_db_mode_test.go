package tests

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestCLIAgentHistory(t *testing.T) {
	ctx, cancel := cliTestContext()
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
