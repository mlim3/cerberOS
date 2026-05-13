package factory

import (
	"strings"
	"testing"

	"github.com/cerberOS/agents-component/pkg/types"
)

func TestExtractRuntimeMetadata_AgentSpawnChildIsLeafWorker(t *testing.T) {
	taskKind, spawnDepth, leafWorker := extractRuntimeMetadata(&types.TaskSpec{
		Metadata: map[string]string{
			"task_kind":   "agent_spawn_child",
			"spawn_depth": "1",
		},
	})
	if taskKind != "agent_spawn_child" {
		t.Fatalf("taskKind = %q, want agent_spawn_child", taskKind)
	}
	if spawnDepth != 1 {
		t.Fatalf("spawnDepth = %d, want 1", spawnDepth)
	}
	if !leafWorker {
		t.Fatal("leafWorker = false, want true")
	}
}

func TestExtractRuntimeMetadata_RootSubtaskCanDelegate(t *testing.T) {
	_, _, leafWorker := extractRuntimeMetadata(&types.TaskSpec{
		Metadata: map[string]string{"task_kind": "subtask"},
	})
	if leafWorker {
		t.Fatal("root subtask should not be treated as leaf worker")
	}
}

func TestSpawnSystemPrompt_CoordinatorGuidanceRequiresFullFanIn(t *testing.T) {
	got := spawnSystemPrompt("web", "- web_search: Search the web.\n", true)
	for _, want := range []string{
		"same skill domain as you",
		"one spawn_agent call per item",
		"Do not call task_complete after only listing the discovered items",
		"must satisfy every user-requested deliverable",
		"recommendation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("spawn system prompt missing %q:\n%s", want, got)
		}
	}
}

func TestSpawnSystemPrompt_WorkerGuidanceIncludesFixedCountOutput(t *testing.T) {
	got := spawnSystemPrompt("web", "- web_search: Search the web.\n", false)
	if !strings.Contains(got, "return exactly that number") {
		t.Fatalf("worker prompt missing fixed-count guidance:\n%s", got)
	}
}
