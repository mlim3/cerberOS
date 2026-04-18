package main

import (
	"os"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func newParams(userMsg string) anthropic.MessageNewParams {
	return anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 4096,
		System:    []anthropic.TextBlockParam{{Text: "sys"}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg)),
		},
	}
}

func TestLLMCache_HitMiss(t *testing.T) {
	t.Setenv("LLM_CACHE_ENABLED", "true")
	t.Setenv("LLM_CACHE_TTL_SECONDS", "60")

	c := NewLLMCache()
	if !c.Enabled() {
		t.Fatalf("cache should be enabled")
	}

	params := newParams("hello world")
	key := c.Key("user-123", params)
	if got := c.Lookup(key); got != nil {
		t.Fatalf("cold lookup should be nil, got %+v", got)
	}

	resp := &anthropic.Message{ID: "msg-1"}
	c.Store(key, resp)
	got := c.Lookup(key)
	if got == nil || got.ID != "msg-1" {
		t.Fatalf("lookup after store should return the stored response, got %+v", got)
	}
}

func TestLLMCache_PersonalizationScoping(t *testing.T) {
	t.Setenv("LLM_CACHE_ENABLED", "true")
	c := NewLLMCache()
	params := newParams("what's my name?")
	keyA := c.Key("user-a", params)
	keyB := c.Key("user-b", params)
	if keyA == keyB {
		t.Fatalf("cache keys must differ per user; personalization scope broken")
	}
}

func TestLLMCache_Disabled(t *testing.T) {
	t.Setenv("LLM_CACHE_ENABLED", "false")
	c := NewLLMCache()
	if c.Enabled() {
		t.Fatalf("cache should be disabled by env override")
	}
	params := newParams("x")
	key := c.Key("u", params)
	c.Store(key, &anthropic.Message{ID: "x"})
	if got := c.Lookup(key); got != nil {
		t.Fatalf("disabled cache must never return a response")
	}
}

func TestLLMCache_KeyStableAcrossInvocations(t *testing.T) {
	os.Unsetenv("LLM_CACHE_ENABLED")
	c := NewLLMCache()
	a := c.Key("u", newParams("hello"))
	b := c.Key("u", newParams("hello"))
	if a != b {
		t.Fatalf("key not deterministic: %s vs %s", a, b)
	}
}
