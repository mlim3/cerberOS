package factory

import (
	"fmt"
	"os"
	"sort"

	"go.yaml.in/yaml/v2"
)

// PermissionPolicy maps each skill domain to the set of Vault operation types
// its agents are permitted to invoke. It replaces the old stub that granted
// "<domain>.credential" catch-all permissions regardless of actual task needs.
//
// YAML file format (loaded via AEGIS_PERMISSION_POLICY_PATH):
//
//	web:
//	  - web_fetch
//	  - web_post
//	data:
//	  - data_read
//	  - data_write
//
// An agent spawned for domain "web" will receive only ["web_fetch", "web_post"]
// as its permitted scope — nothing more.
type PermissionPolicy struct {
	entries map[string][]string // domain → []operation_type; immutable after construction
}

// NewPermissionPolicy constructs a PermissionPolicy from a caller-supplied map.
// Returns an error if entries is empty (a policy with no entries is unusable).
func NewPermissionPolicy(entries map[string][]string) (*PermissionPolicy, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("permission policy: entries must not be empty")
	}
	// Defensive copy so the caller cannot mutate the policy after construction.
	cp := make(map[string][]string, len(entries))
	for domain, ops := range entries {
		if len(ops) == 0 {
			return nil, fmt.Errorf("permission policy: domain %q must have at least one operation type", domain)
		}
		dup := make([]string, len(ops))
		copy(dup, ops)
		cp[domain] = dup
	}
	return &PermissionPolicy{entries: cp}, nil
}

// LoadPermissionPolicy reads a YAML permission-policy file from path and
// returns the parsed policy. Returns an error when path is empty, the file
// cannot be read, the YAML is malformed, or any domain entry is empty.
func LoadPermissionPolicy(path string) (*PermissionPolicy, error) {
	if path == "" {
		return nil, fmt.Errorf("permission policy: path must not be empty")
	}
	data, err := os.ReadFile(path) //nolint:gosec // path comes from operator config
	if err != nil {
		return nil, fmt.Errorf("permission policy: read %s: %w", path, err)
	}
	var raw map[string][]string
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("permission policy: parse %s: %w", path, err)
	}
	return NewPermissionPolicy(raw)
}

// PermissionsFor returns the sorted, deduplicated union of permitted operation
// types for the requested domains.
//
// Returns an error if any requested domain has no policy entry. This is
// fail-secure: an unknown domain is rejected at spawn time rather than
// silently granted an empty or catch-all permission set.
func (p *PermissionPolicy) PermissionsFor(domains []string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, domain := range domains {
		ops, ok := p.entries[domain]
		if !ok {
			return nil, fmt.Errorf("permission policy: domain %q has no policy entry — refusing to spawn (fail-secure)", domain)
		}
		for _, op := range ops {
			seen[op] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for op := range seen {
		result = append(result, op)
	}
	sort.Strings(result)
	return result, nil
}
