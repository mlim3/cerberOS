package security

import (
	"errors"
	"fmt"
	"strings"
)

// ErrACLDenied is returned when a component is not allowed to publish/subscribe.
var ErrACLDenied = errors.New("ACL denied")

// EDD §9.2 / M3: subscribe-only for Agents consumer group (queue aegis-agents); ingress from Vault / Permission Manager.
const (
	subjectAgentsVaultExecuteResult = "aegis.agents.vault.execute.result"
	subjectAgentsCredentialResponse = "aegis.agents.credential.response"
	subjectAgentsRecursiveWildcard  = "aegis.agents.>"
	subjectAgentsLeafWildcard       = "aegis.agents.*"
)

// QueueAgentsComponent is the required JetStream queue group for sensitive agent-only subjects.
const QueueAgentsComponent = "aegis-agents"

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
	"aegis.capability.": {"ui", "io", "orchestrator", "task-router", "task-planner", "agent", "agent-manager",
		"monitoring", "vault", "memory", "aegis-demo", "aegis-stubs"},
	"aegis.vaultprogress.": {"vault", "permission-manager", "monitoring", "orchestrator", "aegis-demo", "aegis-stubs"},
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

	// EDD §9.2: sensitive agent ingress — Vault / Permission Manager only (+ demo stubs).
	if subject == subjectAgentsVaultExecuteResult || subject == subjectAgentsCredentialResponse {
		for _, a := range []string{"vault", "permission-manager", "aegis-demo", "aegis-stubs"} {
			if component == a {
				return nil
			}
		}
		return ErrACLDenied
	}

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

	// EDD §9.2 / M3: only Agents runtime may subscribe (use QueueSubscribeWithACL + QueueAgentsComponent).
	if subject == subjectAgentsVaultExecuteResult || subject == subjectAgentsCredentialResponse {
		for _, a := range []string{"agent", "aegis-agent"} {
			if component == a {
				return nil
			}
		}
		return ErrACLDenied
	}

	// No component may use recursive wildcard — would include M3 sensitive nested subjects.
	if subject == subjectAgentsRecursiveWildcard {
		return ErrACLDenied
	}

	// Single-level wildcard for monitoring-style observation (excludes aegis.agents.vault.*).
	if subject == subjectAgentsLeafWildcard {
		for _, a := range []string{"monitoring", "aegis-demo", "aegis-stubs"} {
			if component == a {
				return nil
			}
		}
		return ErrACLDenied
	}

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

// CheckSubscribePlainForbidden returns an error if subject must not use plain js.Subscribe (M3 queue-only).
func CheckSubscribePlainForbidden(subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == subjectAgentsVaultExecuteResult || subject == subjectAgentsCredentialResponse {
		return fmt.Errorf("%w: use QueueSubscribeWithACL with queue %q", ErrACLDenied, QueueAgentsComponent)
	}
	return nil
}

// CheckQueueSubscribe enforces queue groups for M3 sensitive subjects (EDD §9.2).
func CheckQueueSubscribe(component, subject, queue string) error {
	if err := CheckSubscribe(component, subject); err != nil {
		return err
	}
	if subject == subjectAgentsVaultExecuteResult || subject == subjectAgentsCredentialResponse {
		if strings.TrimSpace(queue) != QueueAgentsComponent {
			return ErrACLDenied
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
