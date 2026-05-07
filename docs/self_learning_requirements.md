# Self-Learning Capabilities: Requirements & Design

**Reference:** [Hermes Agent — A Self-Improving AI Agent](https://dev.to/arshtechpro/hermes-agent-a-self-improving-ai-agent-that-runs-anywhere-2b7d)

---

## What Hermes Does

Hermes implements self-improvement through three mechanisms:

1. **Autonomous Skill Creation** — After completing complex tasks (5+ tool calls), the agent writes a structured markdown document capturing the procedure, pitfalls, and verification steps. Future agents load these instead of re-solving from scratch.
2. **Persistent Memory Injection** — Two curated files (`MEMORY.md`, `USER.md`) store environment facts, conventions, and user preferences. These are injected into the system prompt at every session start.
3. **Session History Search** — All past conversations are stored in SQLite with full-text search. Agents can recall context from past sessions on demand.

None of these involve model weight updates. All self-improvement is behavioral — via prompt content and reusable skill documents.

---

## Current State in cerberOS

| Capability | Status | Notes |
|---|---|---|
| Skill invocation telemetry | **Exists** | `telemetry.go` emits outcome + elapsed_ms per call |
| Prometheus metrics | **Exists** | `metrics.go` — `skill_invocations_total`, `vault_execute_duration_ms` |
| Episodic session logging | **Exists** | `session.go` writes turns to Memory Component |
| Task outcome reporting | **Exists** | `TaskResult`/`TaskFailed` published to Orchestrator |
| Static skills | **Exists** | Three-level hierarchy in `internal/skills/`; YAML-configured |
| System prompt construction | **Exists** | `buildSystemPrompt()` in `loop.go` — static, built once per spawn |
| Memory reads | **Exists** | `memory.Read(agentID, contextTag)` — filtered by agent + tag |
| Cross-session memory | **Absent** | Memory reads are scoped by `agent_id`; agents are ephemeral |
| Outcome scoring | **Absent** | No success quality score, only binary success/failure |
| Skill generation from experience | **Absent** | Skills are static YAML; no runtime writing |
| Prompt enrichment from history | **Absent** | System prompt is static; past outcomes never injected |
| Knowledge aggregation pipeline | **Absent** | No cross-agent memory aggregation |

---

## What Would Be Required

### 1. Outcome Scoring (Feedback Signal)

**What Hermes does:** Implicitly scores task success via tool call count and completion state.

**What cerberOS needs:** A structured quality score attached to every `TaskResult`.

**Required changes:**

**`pkg/types/types.go`** — extend `TaskResult`:
```go
type TaskResult struct {
    // existing fields...
    QualityScore  *float64          `json:"quality_score,omitempty"`   // 0.0–1.0
    ToolCallCount int               `json:"tool_call_count"`            // complexity proxy
    RetryCount    int               `json:"retry_count"`                // from failure_count
    SkillOutcomes []SkillOutcome    `json:"skill_outcomes,omitempty"`
}

type SkillOutcome struct {
    Domain    string  `json:"domain"`
    Command   string  `json:"command"`
    Outcome   string  `json:"outcome"`   // success | error | timeout
    ElapsedMS int64   `json:"elapsed_ms"`
}
```

**`cmd/agent-process/loop.go`** — accumulate `SkillOutcome` entries during Phase 3 (Observe) alongside the existing `toolOutcome` structs. Publish the enriched `TaskResult` on `task_complete`.

**Scoring heuristics (no LLM needed):**
- `success=true` + `retry_count=0` + no tool errors → 1.0
- `success=true` + some tool errors recovered → 0.7–0.9
- `success=true` + `retry_count > 0` → penalize by 0.1 per retry
- `success=false` → 0.0

---

### 2. Skill Performance Tracking

**What Hermes does:** Skills "self-improve during use when the agent discovers a better approach."

**What cerberOS needs:** Aggregate telemetry per skill command, stored in the Memory Component, readable at spawn time.

**Required changes:**

**New DataType in memory writes:** `skill_performance`
```go
// Written by the Orchestrator (or a new aggregator process) after each task
type SkillPerformanceRecord struct {
    Domain        string    `json:"domain"`
    Command       string    `json:"command"`
    SuccessRate   float64   `json:"success_rate"`    // rolling 30-day
    AvgElapsedMS  int64     `json:"avg_elapsed_ms"`
    ErrorPatterns []string  `json:"error_patterns"`  // top recurring error messages
    SampleCount   int       `json:"sample_count"`
    UpdatedAt     time.Time `json:"updated_at"`
}
```

**`internal/memory/memory.go`** — add `ReadAllByType("skill_performance")` (already in the interface signature, just needs to be exercised).

**`internal/skillsconfig/`** — add a `PerformanceAnnotator` that reads `skill_performance` records and can emit warnings or deprecation hints about underperforming commands.

**NATS subject needed (new):**
```
aegis.orchestrator.skill.performance.update   (outbound, at-least-once)
aegis.agents.skill.performance                (inbound, at-least-once — skill perf snapshot for spawn)
```

---

### 3. Dynamic Skill Generation (Core Self-Improvement)

**What Hermes does:** After tasks requiring 5+ tool calls, the agent writes a markdown skill document. Future agents load it.

**What cerberOS needs:** A mechanism for agents to propose new skill specs or annotate existing ones, which the Orchestrator validates and promotes to the skills YAML.

**Required changes:**

**New tool in `cmd/agent-process/tools.go`:**
```go
toolNameProposeSkill = "propose_skill"
// Input:
// {
//   "domain": "web",
//   "command": "web_summarize_page",
//   "description": "Fetches and summarizes a webpage into bullet points. Use when the task requires extracting key information from a URL. Do NOT use for raw HTML parsing.",
//   "parameters": { ... JSON Schema ... },
//   "procedure": "Step-by-step notes captured from this task execution",
//   "pitfalls": ["Always check response status before parsing", "..."],
//   "trigger_condition": "tool_call_count >= 5 AND outcome == success"
// }
// Output: proposal_id (Orchestrator validates async)
```

**New NATS subjects:**
```
aegis.orchestrator.skill.proposal            (outbound, at-least-once)
aegis.agents.skill.proposal.ack              (inbound, at-least-once — accepted | rejected | duplicate)
```

**Orchestrator-side skill promotion pipeline** (outside agents-component):
1. Receive `skill.proposal`
2. Validate: schema completeness, description quality, duplicate detection (cosine similarity against existing skills)
3. Dry-run: spawn a test agent with the proposed skill against a synthetic task
4. If success rate > threshold → merge into `default_skills.yaml` + hot-reload via existing `internal/skills/reload.go`
5. ACK the proposing agent

**`internal/skills/skills.go`** — add `ProposeCommand(domain string, draft *types.SkillProposal) error` to the Manager interface.

**Constraint alignment:** Proposals are published as NATS messages (not direct writes), respecting the "all communication via Orchestrator" settlement.

---

### 4. Cross-Session Knowledge Injection (Prompt Enrichment)

**What Hermes does:** Injects `MEMORY.md` and `USER.md` into the system prompt at every session start.

**What cerberOS needs:** A `knowledge_context` memory type, scoped by domain (not agent_id), injected into `buildSystemPrompt()` at spawn time.

**Required changes:**

**New DataType:** `knowledge_context`
```go
type KnowledgeEntry struct {
    Domain    string    `json:"domain"`      // "web", "data", "general", or "*" for all
    Scope     string    `json:"scope"`       // "fact" | "lesson" | "pitfall" | "convention"
    Content   string    `json:"content"`     // max 200 chars (enforced)
    Source    string    `json:"source"`      // task_id that produced it
    UseCount  int       `json:"use_count"`   // incremented when injected
    CreatedAt time.Time `json:"created_at"`
}
```

**`internal/memory/memory.go`** — add `ReadKnowledgeContext(domain string) ([]KnowledgeEntry, error)` that reads `knowledge_context` entries filtered by domain + `"*"`.

**`internal/factory/factory.go`** — at spawn time, after skill resolution, call `ReadKnowledgeContext(domain)`. Pass the results to the agent as part of spawn context.

**`cmd/agent-process/loop.go` — `buildSystemPrompt()`:**
```go
func buildSystemPrompt(skillDomain, manifest string, knowledge []KnowledgeEntry) string {
    // existing base prompt...
    if len(knowledge) > 0 {
        prompt += "\n\nLessons from past tasks:\n"
        for _, k := range knowledge {
            prompt += fmt.Sprintf("- [%s] %s\n", k.Scope, k.Content)
        }
    }
    // existing manifest append...
}
```

**Token budget impact:** Knowledge entries must be counted against `spawnContextBudget` (currently 2,048 tokens). Either raise the budget or enforce a max entry count (e.g., top-10 by `use_count`).

**New tool for agents to write knowledge:**
```go
toolNameRecordLesson = "record_lesson"
// Input: { "scope": "pitfall", "content": "Always set timeout on web_fetch for external APIs", "domain": "web" }
// Triggers memory.Write with DataType: "knowledge_context", policy: policyDegradable
```

---

### 5. Session History Search (Recall)

**What Hermes does:** Full-text SQLite search over all past conversations.

**What cerberOS needs:** A domain-scoped semantic search over past `episode` memory entries, available as a tool.

**Required changes:**

**New NATS subjects:**
```
aegis.orchestrator.session.search.request    (outbound, at-least-once)
aegis.agents.session.search.response         (inbound, at-least-once)
```

**New tool in `cmd/agent-process/tools.go`:**
```go
toolNameRecallSession = "recall_session"
// Input: { "query": "how did we handle rate limiting on web_fetch last week?", "domain": "web", "top_k": 3 }
// Publishes search request to Orchestrator; Orchestrator queries Memory Component
// Returns: []SessionSnippet{ task_id, summary, relevance_score }
```

**Memory Component side** (outside agents-component): The Memory Component would need to implement semantic search over `episode` records, either via embeddings index or BM25 full-text. This is the largest infrastructure addition.

---

## Implementation Phases

| Phase | Description | Effort | Risk |
|---|---|---|---|
| **1** | Outcome scoring + `SkillOutcome` on `TaskResult` | Low | Low — additive only |
| **2** | `knowledge_context` DataType + `record_lesson` tool + prompt injection | Medium | Low — uses existing memory infra |
| **3** | `skill_performance` tracking + `ReadAllByType` aggregation | Medium | Low — additive |
| **4** | `propose_skill` tool + Orchestrator validation pipeline | High | Medium — requires Orchestrator changes |
| **5** | `recall_session` tool + Memory Component semantic search | High | High — requires new infra |

Phases 1–3 are entirely within the agents-component and existing memory contracts. Phases 4–5 require Orchestrator and Memory Component changes.

---

## Constraints That Must Be Respected

All changes must comply with the settled decisions in `agents-component/CLAUDE.md`:

1. **Stateless component** — no local state survives restarts; all knowledge stored via Memory Interface
2. **All communication via Orchestrator** — `propose_skill` and `recall_session` must use NATS, not direct calls
3. **No full session dumps** — `record_lesson` writes are surgical (max 200 chars per entry, tagged, not raw turn dumps)
4. **Token budget** — knowledge injection must be counted against `spawnContextBudget`; use `topK` capping
5. **Vault credential isolation** — self-improvement data never touches the vault; memory writes use `policyDegradable`
6. **Audit trail** — `propose_skill` calls must emit to `aegis.orchestrator.audit.event` (same as skill invocations)

---

## Key Files to Modify

| File | Change |
|---|---|
| `pkg/types/types.go` | Add `QualityScore`, `SkillOutcome`, `KnowledgeEntry`, `SkillProposal` types |
| `cmd/agent-process/loop.go` | Accumulate `SkillOutcome`; enrich `TaskResult`; call `buildSystemPrompt` with knowledge |
| `cmd/agent-process/tools.go` | Add `propose_skill`, `record_lesson`, `recall_session` tools |
| `internal/memory/memory.go` | Add `ReadKnowledgeContext(domain)` method |
| `internal/factory/factory.go` | Fetch knowledge context at spawn; pass to system prompt builder |
| `internal/skills/skills.go` | Add `ProposeCommand()` to Manager interface |
| `internal/comms/subjects.go` | Add new NATS subjects for skill proposals, performance, session search |
| `internal/skillsconfig/default_skills.yaml` | Add `record_lesson` and `propose_skill` as built-in skills |
