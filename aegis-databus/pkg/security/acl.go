package security

import (
	"errors"
	"strings"
)

// ErrACLDenied is returned when a component is not allowed to publish/subscribe.
var ErrACLDenied = errors.New("ACL denied")

// Subject ACL rules per site/instruction.md:
// - aegis.tasks.>    → Task Router, Task Planner
// - aegis.agents.>   → Agent Manager only
// - aegis.runtime.>  → Runtime Abstraction, Resource Allocator
// - aegis.vault.>    → Permission Manager only
// - aegis.memory.>   → Memory Manager, Knowledge Sharing, Data Ingestion
// - aegis.monitoring.> → Monitoring only
// - aegis.ui.>       → UI Layer
// - aegis.health.>   → DataBus (heartbeat), Self-Healing
// - Agents cannot subscribe to aegis.vault.>
// - Agents cannot publish to aegis.agents.>

// publishPermissions maps subject prefix to allowed component names.
// A subject "aegis.tasks.routed" matches prefix "aegis.tasks.".
var publishPermissions = map[string][]string{
	"aegis.tasks.":      {"task-router", "task-planner", "orchestrator", "aegis-demo", "aegis-stubs"},
	"aegis.agents.":     {"agent-manager", "orchestrator", "aegis-demo", "aegis-stubs"},
	"aegis.runtime.":    {"runtime", "resource-allocator", "agent", "aegis-demo", "aegis-stubs"},
	"aegis.vault.":      {"permission-manager", "vault", "aegis-stubs"},
	"aegis.memory.":     {"memory", "knowledge-sharing", "data-ingestion", "aegis-demo", "aegis-stubs"},
	"aegis.monitoring.": {"monitoring", "aegis-demo", "aegis-stubs"},
	"aegis.ui.":         {"ui", "io", "aegis-demo"},
	"aegis.health.":     {"databus", "aegis-databus", "self-healing", "aegis-demo", "aegis-stubs"},
	"aegis.personalization.": {"personalization", "task-router", "aegis-demo"},
	"aegis.dlq":         {"databus", "aegis-databus"}, // DLQ receives failed messages
}

// subscribeDeny: agents (untrusted) cannot subscribe to vault.
var subscribeDenyVault = []string{"agent"}

// SR-DB-006: DLQ admin-only — only these components may subscribe to aegis.dlq.>
var subscribeAllowDLQ = []string{"databus", "aegis-databus", "admin", "dlq-admin"}

// subjectMatchesPrefix returns true if subject matches the prefix.
// Prefix "aegis.tasks.>" matches "aegis.tasks.routed". Prefix "aegis.dlq" is exact.
func subjectMatchesPrefix(subject, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, ">")
	if strings.HasSuffix(prefix, ".") || strings.HasSuffix(prefix, ">") {
		prefix = strings.TrimSuffix(prefix, ".")
	}
	// Multi-level: aegis.tasks matches aegis.tasks.X or aegis.tasks.X.Y
	if len(subject) >= len(prefix) {
		if subject == prefix || strings.HasPrefix(subject, prefix+".") {
			return true
		}
	}
	return false
}

// CheckPublish returns nil if component is allowed to publish to subject.
func CheckPublish(component, subject string) error {
	component = strings.ToLower(strings.TrimSpace(component))
	subject = strings.TrimSpace(subject)

	// Agents cannot publish to aegis.agents.>
	if contains(subscribeDenyVault, component) || component == "agent" {
		if subjectMatchesPrefix(subject, "aegis.agents.") {
			return ErrACLDenied
		}
	}

	for prefix, allowed := range publishPermissions {
		if subjectMatchesPrefix(subject, prefix) {
			for _, a := range allowed {
				if component == a {
					return nil
				}
			}
			return ErrACLDenied
		}
	}

	// Unknown subject prefix - deny by default
	return ErrACLDenied
}

// CheckSubscribe returns nil if component is allowed to subscribe to subject.
// SR-DB-006: aegis.dlq.> admin-only; agents cannot subscribe to aegis.vault.>
func CheckSubscribe(component, subject string) error {
	component = strings.ToLower(strings.TrimSpace(component))
	subject = strings.TrimSpace(subject)

	// SR-DB-006: DLQ — only admin components may subscribe
	if subjectMatchesPrefix(subject, "aegis.dlq") {
		for _, allowed := range subscribeAllowDLQ {
			if component == allowed {
				return nil
			}
		}
		return ErrACLDenied
	}

	if subjectMatchesPrefix(subject, "aegis.vault.") {
		for _, denied := range subscribeDenyVault {
			if component == denied {
				return ErrACLDenied
			}
		}
	}

	return nil
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}
