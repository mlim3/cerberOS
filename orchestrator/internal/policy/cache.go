// PolicyCache is a TTL-based in-memory cache for Vault policy responses (§FR-PE-05).
//
// Cache key: userID + sorted(requiredSkillDomains)
// Invalidation: on any Vault policy update event received via NATS.
// TTL: configurable via VAULT_POLICY_CACHE_TTL_SECONDS (default 60s).
package policy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mlim3/cerberOS/orchestrator/internal/types"
)

// cachedEntry holds a cached policy scope and its expiry time.
type cachedEntry struct {
	scope     types.PolicyScope
	expiresAt time.Time
}

// PolicyCache is a goroutine-safe TTL cache for policy scopes.
type PolicyCache struct {
	mu      sync.RWMutex
	entries map[string]cachedEntry
	ttl     int // seconds
}

// NewPolicyCache creates a new PolicyCache with the given TTL in seconds.
func NewPolicyCache(ttlSeconds int) *PolicyCache {
	return &PolicyCache{
		entries: make(map[string]cachedEntry),
		ttl:     ttlSeconds,
	}
}

// Get retrieves a cached PolicyScope for the given userID and skill domains.
// Returns false if no valid (non-expired) entry exists.
func (c *PolicyCache) Get(userID string, skillDomains []string) (types.PolicyScope, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := cacheKey(userID, skillDomains)
	entry, ok := c.entries[key]
	if !ok {
		return types.PolicyScope{}, false
	}
	if time.Now().After(entry.expiresAt) {
		return types.PolicyScope{}, false // expired
	}
	return entry.scope, true
}

// Set stores a PolicyScope in the cache with the configured TTL.
func (c *PolicyCache) Set(userID string, skillDomains []string, scope types.PolicyScope) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(userID, skillDomains)
	c.entries[key] = cachedEntry{
		scope:     scope,
		expiresAt: time.Now().Add(time.Duration(c.ttl) * time.Second),
	}
}

// InvalidateAll clears all cached entries.
// Called when any Vault policy update event is received (conservative invalidation).
func (c *PolicyCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cachedEntry)
}

// cacheKey produces a stable, sorted cache key from userID and skill domains.
func cacheKey(userID string, skillDomains []string) string {
	sorted := make([]string, len(skillDomains))
	copy(sorted, skillDomains)
	sort.Strings(sorted)
	return fmt.Sprintf("%s|%s", userID, strings.Join(sorted, ","))
}
