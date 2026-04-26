---
name: Skills Feature Design Decisions
description: Key architectural decisions for the skills expansion, auto-synthesis, logging, and UI notification feature
type: project
---

Implementing four connected features: log querying skills, web activity skills, skill auto-synthesis, and UI skill notifications.

Key decisions locked in:

- **web.fetch without credentials**: agents-component is allowed to make direct HTTP calls for public URLs. A dedicated `internal/http` package in agents-component will be the only permitted location for this (not vault-delegated). Vault-delegated path still used for credentialed web operations (web.search).
- **Auto-synthesis**: No approval gate. Synthesized skills go live immediately. Tagged `"source": "synthesized"`, rate-limited to 3 per task.
- **UI notifications**: Only "notable" skill usage — web domain, synthesized skills, vault-delegated ops, elapsed_ms > 5000, logs.search.
- **Search API**: Tavily — purpose-built for AI agent web search, structured LLM-optimized output.

**Why:** User prefers simplicity for public web fetches (no vault overhead for unauthenticated calls), fast time-to-value for auto-synthesis (no human approval), and low-noise UI (only surface skill activity worth the user's attention).

**How to apply:** When implementing web.fetch, do NOT route through vault engine. Add `internal/http` package to agents-component with explicit code enforcement comment. When implementing web.search, DO route through vault (needs Tavily API key). For UI notifications, filter by the notable criteria above before publishing skill_activity SSE events.
