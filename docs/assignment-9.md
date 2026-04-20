# Assignment #9 — Milestone 3 Requirement Status

Status report for the requirements listed in
`[context/assignment-9.txt](../context/assignment-9.txt)`.

**Branch:** `a9-stefan-chemero` (PR #142)


| #   | Requirement                                             | Status | Evidence                                                                                                                                                                                                                                                                                         |
| --- | ------------------------------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | TraceID at first entry point                            | ✅      | `[io/api/src/trace-context.ts](../io/api/src/trace-context.ts)` initializes W3C `traceparent` at the HTTP entry; propagated through NATS headers to orchestrator and agents (`trace_id` appears in every log line)                                                                               |
| 2   | Logging library + standard levels + standardized format | ✅      | Go uses `log/slog` with structured JSON output; Node side uses `[io/api/src/logger.ts](../io/api/src/logger.ts)`. `INFO`/`WARN`/`ERROR`/`DEBUG` appear consistently across orchestrator + agents                                                                                                 |
| 3   | Centralized logging system                              | ✅      | Loki (`:3100`) + Promtail + Grafana (`:3000`) + Tempo (traces, `:4317`) all wired in `[docker-compose.yml](../docker-compose.yml)`                                                                                                                                                               |
| 4   | Multi-step prompting and confirmation                   | ✅      | Commit `5c85e07 feat: multi prompting - v1` + `io/surfaces/web/src/components/PlanPreviewCard.tsx` + `awaiting_approval` / `awaiting_feedback` task states + `PLAN_REJECTED` / `PLAN_APPROVAL_TIMEOUT` error codes                                                                               |
| 5   | Multi-agent planning — parallel + serial                | ✅      | `[orchestrator/internal/executor/executor.go](../orchestrator/internal/executor/executor.go)` dispatches independent subtasks in parallel goroutines; `DependsOn` enforces sequential ordering when dependencies exist                                                                           |
| 6   | LLM caching + Personalization                           | ✅      | `[agents-component/cmd/agent-process/llmcache.go](../agents-component/cmd/agent-process/llmcache.go)` — per-process TTL cache keyed by `UserContextID + system + messages + tools`; explicitly documented personalization safeguard (User A's cached response is never served to User B)         |
| 7   | Agent outcome tracking: Success, Failure                | ✅      | `[orchestrator/observability/grafana/dashboards/cerberos-agent-outcomes.json](../orchestrator/observability/grafana/dashboards/cerberos-agent-outcomes.json)` dashboard + telemetry events emitted from `agents-component/cmd/agent-process/telemetry.go` + success/failure counts in Prometheus |


## Caveats (not assignment-blocking)

- **LLM cache coverage is scoped.** Only `end_turn` responses are cached — tool-use iterations are not, by design. Caching tool-use responses would be unsound because tool outputs are not part of the request. Documented in the `llmcache.go` file header.
- **Personalization is structural.** The cache key includes `UserContextID`, and the system prompt is personalized via user profile memory, so caches are correctly scoped per-user. There is no dedicated test that explicitly demonstrates "User A got a personalized response that User B cannot see from the cache" — trivial to add if a reviewer asks for a concrete demo.
- **Web search is flaky.** Out of scope for Assignment #9 (M4/future item).

