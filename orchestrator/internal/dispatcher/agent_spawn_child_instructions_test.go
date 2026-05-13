package dispatcher

import (
	"strings"
	"testing"
)

func TestBuildAgentSpawnChildInstructions_AddsLeafWorkerConstraints(t *testing.T) {
	got := buildAgentSpawnChildInstructions("Research exactly one item.")
	if !strings.Contains(got, "Research exactly one item.") {
		t.Fatalf("base instructions missing: %q", got)
	}
	if !strings.Contains(got, "Leaf-worker constraints") {
		t.Fatalf("leaf-worker constraints missing: %q", got)
	}
	if !strings.Contains(got, "web_search") || !strings.Contains(got, "max_results no higher than 5") {
		t.Fatalf("web-search limiting guidance missing: %q", got)
	}
	if !strings.Contains(got, "fixed number of findings") || !strings.Contains(got, "return exactly that number") {
		t.Fatalf("fixed-count output guidance missing: %q", got)
	}
}
