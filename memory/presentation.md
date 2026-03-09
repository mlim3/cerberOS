---
marp: true
title: Memory Service Design Choices
description: Thought process behind the Memory Service architecture
paginate: true
theme: default
style: |
  :root {
    --bg: #f4f1ea;
    --ink: #1e2a2f;
    --muted: #5a6a70;
    --accent: #0f766e;
    --accent-soft: #d9f1ee;
    --line: #d7d2c6;
  }

  section {
    font-family: "Avenir Next", "Segoe UI", sans-serif;
    background: linear-gradient(135deg, #f8f5ee 0%, #efe9dc 100%);
    color: var(--ink);
    padding: 56px 68px;
  }

  h1, h2, h3 {
    letter-spacing: 0.2px;
    margin-bottom: 0.35em;
  }

  h1 {
    font-size: 2.1em;
    color: #0b3c49;
  }

  h2 {
    font-size: 1.45em;
    color: #114e60;
  }

  p, li {
    font-size: 1.0em;
    line-height: 1.4;
    color: var(--ink);
  }

  small, .muted {
    color: var(--muted);
  }

  strong {
    color: #0c5f58;
  }

  code {
    background: #e7f3f1;
    color: #0f4a45;
    border-radius: 4px;
    padding: 0.12em 0.3em;
  }

  blockquote {
    border-left: 6px solid var(--accent);
    background: var(--accent-soft);
    padding: 0.6em 0.9em;
    margin: 0.6em 0;
    color: #0f3f3a;
  }

  hr {
    border: none;
    border-top: 1px solid var(--line);
    margin: 0.8em 0 1em;
  }
---

# Memory Service
## Design Thinking, not just endpoints

<small>How we chose shape, boundaries, and tradeoffs</small>

---

## The Core Question

How do we give agents **useful memory** without making every caller become a retrieval engineer?

> Design target: low caller complexity, high internal rigor.

---

## Design Lens

- Keep API intent-focused: `save`, `query`, `all`, targeted fact correction
- Keep architecture future-ready: logical sharding keys from day one
- Keep behavior predictable: standard envelope + typed errors
- Keep ownership strict: user-scoped access and `not_found` masking

---

## Why a Facade API?

- Callers send **raw content**, not chunking/embedding params
- Service owns pipeline choices and can evolve internally
- Fewer integration mistakes across the OS
- Easier to enforce traceability and observability centrally

---

## Data Model Choices

- `chat_schema`: immutable transcript, append-only
- `personal_info_schema`: semantic chunks + structured facts + source links
- `agent_logs_schema`: auditable agent execution timeline
- `service_log_schema`: operational telemetry

<small>Separation by intent keeps query patterns clean.</small>

---

## Retrieval Strategy

- pgvector in Postgres keeps relational + semantic retrieval together
- ANN search for practical latency at scale
- API returns **similarity score [0,1]**, not raw distance
- Tie-break with `created_at DESC` for deterministic responses

---

## Concurrency and Correctness

- Fact updates use optimistic concurrency (`version`)
- Retry-safe writes use idempotency keys
- Conflict is explicit (`409`) when intent diverges

> We prefer explicit conflict over silent overwrite.

---

## Security and Multi-Tenancy Posture

- Validate user existence on all user-scoped paths
- Constrain lookups by owner keys
- Return `not_found` to avoid cross-user information leaks
- Internal vault access protected and audited

---

## Observability as a First-Class Feature

- Health endpoint for orchestrators
- System event stream for operational debugging
- Agent execution logs for decision traceability
- Response envelope standardization for client simplicity

---

## Tradeoffs We Accepted

- More complexity inside service, less at the edge
- Slightly stricter API contract, better long-term consistency
- Initial mock components (embedder/extractor) to unblock shape validation

<small>We optimized for stable interfaces first, perfect internals second.</small>

---

## Evolution Plan

1. Replace mock embedding/fact extraction with production adapters
2. Expand test matrix around ownership, conflicts, and retrieval ranking
3. Tighten docs/swagger as contract source of truth
4. Add migration discipline for schema/index changes

---

# Final Principle

## Memory should feel simple to use,
## but never simplistic under the hood.

<small>Design for trustworthy behavior before feature volume.</small>
