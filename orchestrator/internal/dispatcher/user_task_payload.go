package dispatcher

import (
	"encoding/json"
	"strings"
)

// extractSystemPrompt returns the optional system_prompt string from a user task
// JSON payload. Empty string if absent or invalid.
func extractSystemPrompt(payload []byte) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return ""
	}
	raw, ok := m["system_prompt"]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// isMaintenancePayload reports whether the payload marks a scheduled / cron
// maintenance task (skip IO status noise and optional personalization).
func isMaintenancePayload(payload []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return false
	}
	raw, ok := m["maintenance"]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false
	}
	return b
}
