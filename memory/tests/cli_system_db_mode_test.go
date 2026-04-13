package tests

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestCLISystemEvents(t *testing.T) {
	ctx, cancel := cliTestContext()
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
