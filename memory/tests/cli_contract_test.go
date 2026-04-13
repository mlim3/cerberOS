package tests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCLIContract_FactsSaveAndRead(t *testing.T) {
	cliPath := blackboxCLIPath(t)
	userID := validUserFixture(t)
	uniqueFact := fmt.Sprintf("black-box fact %d: I prefer writing Go code.", time.Now().UnixNano())

	t.Run("save_fact_exits_successfully", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, cliPath, "-db", "env", "facts", "save", "--user", userID, uniqueFact)
		if err != nil {
			t.Fatalf("save fact failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if strings.TrimSpace(stdout) == "" {
			t.Fatalf("save fact stdout was empty")
		}
	})

	t.Run("facts_all_returns_json_array_and_includes_saved_fact", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, cliPath, "-db", "env", "facts", "all", "--user", userID)
		if err != nil {
			t.Fatalf("facts all failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}

		var items []map[string]any
		if err := json.Unmarshal([]byte(stdout), &items); err != nil {
			t.Fatalf("facts all stdout was not a JSON array: %v\nstdout:\n%s", err, stdout)
		}

		found := false
		for _, item := range items {
			content, _ := item["content"].(string)
			if content == uniqueFact {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("saved fact %q was not present in facts all output: %s", uniqueFact, stdout)
		}
	})

	t.Run("facts_query_returns_json_array_with_id_and_content", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, cliPath, "-db", "env", "facts", "query", "--user", userID, "prefer writing Go")
		if err != nil {
			t.Fatalf("facts query failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}

		var items []map[string]any
		if err := json.Unmarshal([]byte(stdout), &items); err != nil {
			t.Fatalf("facts query stdout was not a JSON array: %v\nstdout:\n%s", err, stdout)
		}
		for i, item := range items {
			if strings.TrimSpace(asString(item["id"])) == "" {
				t.Fatalf("facts query item %d missing id: %#v", i, item)
			}
			if strings.TrimSpace(asString(item["content"])) == "" {
				t.Fatalf("facts query item %d missing content: %#v", i, item)
			}
		}
	})
}

func TestCLIContract_EmptyListCommandsEmitJSONArray(t *testing.T) {
	cliPath := blackboxCLIPath(t)

	t.Run("chat_history_empty_result_is_array", func(t *testing.T) {
		sessionID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		stdout, stderr, err := runCLI(t, cliPath, "-db", "env", "chat", "history", "--session", sessionID, "--limit", "3")
		if err != nil {
			t.Fatalf("chat history failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertJSONArrayOutput(t, stdout)
	})

	t.Run("agent_history_empty_result_is_array", func(t *testing.T) {
		taskID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		stdout, stderr, err := runCLI(t, cliPath, "-db", "env", "agent", "history", "--task", taskID, "--limit", "5")
		if err != nil {
			t.Fatalf("agent history failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertJSONArrayOutput(t, stdout)
	})

	t.Run("system_events_result_is_array", func(t *testing.T) {
		stdout, stderr, err := runCLI(t, cliPath, "-db", "env", "system", "events", "--limit", "10")
		if err != nil {
			t.Fatalf("system events failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertJSONArrayOutput(t, stdout)
	})
}
