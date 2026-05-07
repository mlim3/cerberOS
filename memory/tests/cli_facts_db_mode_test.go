package tests

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCLIFactsQuery(t *testing.T) {
	ctx, cancel := cliTestContext()
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

	if len(facts) > 0 && (facts[0].ID == "" || facts[0].Content == "") {
		t.Errorf("Expected valid fact fields, got: %+v", facts[0])
	}
}

func TestCLIFactsAll(t *testing.T) {
	ctx, cancel := cliTestContext()
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

	if strings.TrimSpace(string(output)) == "null" {
		t.Fatalf("Expected JSON array output, got null")
	}

	if len(facts) > 0 && (facts[0].ID == "" || facts[0].Content == "") {
		t.Errorf("Expected valid fact fields, got: %+v", facts[0])
	}
}

func TestCLIFactsSave(t *testing.T) {
	ctx, cancel := cliTestContext()
	defer cancel()

	factContent := "CLI fact test " + uuid.NewString()

	cmd := exec.CommandContext(ctx, cliPath, "-db", "env", "facts", "save", "--user", "11111111-1111-1111-1111-111111111111", factContent)
	cmd.Env = getBaseEnv()

	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
	}

	if string(output) != "Fact saved successfully.\n" {
		t.Fatalf("Unexpected output: %s", string(output))
	}

	allCmd := exec.CommandContext(ctx, cliPath, "-db", "env", "facts", "all", "--user", "11111111-1111-1111-1111-111111111111")
	allCmd.Env = getBaseEnv()

	allOutput, err := allCmd.Output()
	if err != nil {
		t.Fatalf("CLI all command failed after save: %v\nOutput: %s", err, string(allOutput))
	}

	if strings.TrimSpace(string(allOutput)) == "null" {
		t.Fatalf("Expected JSON array output after save, got null")
	}

	var facts []Fact
	if err := json.Unmarshal(allOutput, &facts); err != nil {
		t.Fatalf("Failed to parse facts all output after save: %v\nOutput: %s", err, string(allOutput))
	}

	found := false
	for _, fact := range facts {
		if fact.Content == factContent {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("Saved fact was not returned by facts all")
	}
}
