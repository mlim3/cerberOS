package tests

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestCLIChatHistory(t *testing.T) {
	ctx, cancel := cliTestContext()
	defer cancel()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "conversation flag",
			args: []string{"-db", "env", "chat", "history", "--conversation", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
		},
		{
			name: "deprecated session alias",
			args: []string{"-db", "env", "chat", "history", "--session", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(ctx, cliPath, tt.args...)
			cmd.Env = getBaseEnv()

			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("CLI command failed: %v\nOutput: %s", err, string(output))
			}

			var messages []Message
			if err := json.Unmarshal(output, &messages); err != nil {
				t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, string(output))
			}

			if len(messages) > 0 && (messages[0].ID == "" || messages[0].Role == "" || messages[0].Content == "") {
				t.Errorf("Expected valid message fields, got: %+v", messages[0])
			}
		})
	}
}
