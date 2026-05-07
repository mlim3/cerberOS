// Package main — llmcache.go implements a per-process LLM response cache.
//
// Design notes (milestone 3 / Assignment #9 "LLM caching: Personalization"):
//
//   - The cache lives inside the agent-process (guest microVM). It is not
//     persisted across VM restarts. A process-lifetime cache is sufficient to
//     demonstrate the caching + personalization requirement without adding a
//     new NATS round-trip or changing the memory service protocol (the guest
//     VM cannot reach the memory HTTP API directly — see CLAUDE.md).
//
//   - Cache keys include the UserContextID so personalization is preserved:
//     User A's cached response is never served to User B. The system prompt
//     already contains user-profile material injected by buildSystemPrompt, so
//     hashing the system prompt is sufficient; including UserContextID in the
//     key explicitly is a belt-and-braces safeguard.
//
//   - Only end_turn responses are cached. Tool-use responses (stop_reason ==
//     "tool_use") depend on tool outputs that are not part of the request, so
//     caching them would be semantically incorrect.
//
//   - Metrics are emitted via the existing MetricsEvent NATS path to aegis-
//     agents so llm_cache_hits_total / llm_cache_misses_total show up in
//     Prometheus / Grafana without adding a new scrape target for the guest.
//
// Disable by setting LLM_CACHE_ENABLED=false. Tune TTL via LLM_CACHE_TTL_SECONDS
// (default 3600 = 1 hour).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

type cacheEntry struct {
	resp      *anthropic.Message
	expiresAt time.Time
}

// LLMCache is an in-process, TTL-bounded cache of Anthropic Messages.New
// responses keyed by (model + user + tools + system + messages).
type LLMCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
	enabled bool
}

// NewLLMCache constructs a cache honouring LLM_CACHE_ENABLED (default true) and
// LLM_CACHE_TTL_SECONDS (default 3600).
func NewLLMCache() *LLMCache {
	enabled := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_CACHE_ENABLED"))); v == "false" || v == "0" || v == "off" {
		enabled = false
	}
	ttl := time.Hour
	if raw := strings.TrimSpace(os.Getenv("LLM_CACHE_TTL_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			ttl = time.Duration(n) * time.Second
		}
	}
	return &LLMCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
		enabled: enabled,
	}
}

// Enabled reports whether cache reads and writes will occur.
func (c *LLMCache) Enabled() bool {
	return c != nil && c.enabled
}

// Key builds a deterministic sha256 hash over the cacheable portions of a
// Messages.New request, scoping by userContextID so caches never cross users.
func (c *LLMCache) Key(userContextID string, params anthropic.MessageNewParams) string {
	h := sha256.New()
	_, _ = h.Write([]byte(params.Model))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(userContextID))
	_, _ = h.Write([]byte{0})
	if systemBytes, err := json.Marshal(params.System); err == nil {
		_, _ = h.Write(systemBytes)
	}
	_, _ = h.Write([]byte{0})
	if toolsBytes, err := json.Marshal(params.Tools); err == nil {
		_, _ = h.Write(toolsBytes)
	}
	_, _ = h.Write([]byte{0})
	if msgBytes, err := json.Marshal(params.Messages); err == nil {
		_, _ = h.Write(msgBytes)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Lookup returns a non-nil response if a fresh entry exists for key.
func (c *LLMCache) Lookup(key string) *anthropic.Message {
	if !c.Enabled() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil
	}
	if time.Now().After(e.expiresAt) {
		delete(c.entries, key)
		return nil
	}
	return e.resp
}

// Store persists resp under key. Only call for responses with
// stop_reason == "end_turn" — tool-use responses are not safe to cache because
// their correctness depends on tool outputs not reflected in the request.
func (c *LLMCache) Store(key string, resp *anthropic.Message) {
	if !c.Enabled() || resp == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{resp: resp, expiresAt: time.Now().Add(c.ttl)}
}
