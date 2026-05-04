# Agents Skill Embedding Options

## Purpose

This note explains the main ways we could move `agents-component` from file-authoritative skills to a cleaner long-term storage model now that it uses the shared local embedding model work.

It covers:

- whether Agents can talk directly to Memory
- whether skill reads/search must go through Orchestrator
- where skills should be stored
- how static skills could be seeded
- what should remain local versus remote

The goal is to choose a path that matches both:

- the project-wide plan that persistent state lives in Memory
- the Agents architecture constraints already documented in `agents-component/CLAUDE.md`

---

## Current State

Today, skill search in `agents-component` works like this:

- Static skills are defined in config files and loaded into the local skill tree.
- Synthesized skills are persisted as `skill_cache` records through the Memory path and reloaded at startup.
- Semantic skill search is performed locally in-memory by the Skill Hierarchy Manager.
- Semantic embeddings now come from the shared embedding service/runtime.

Important current split:

- authoritative source for static skills: file config
- authoritative source for synthesized skills: Memory
- live runtime search/index: local in-memory cache inside Agents

That split is what this design note is trying to clean up.

---

## Key Constraint: Can Agents Talk Directly To Memory?

### What the current architecture says

`agents-component/CLAUDE.md` is explicit:

- The Agents Component is not authorized to communicate directly with the Memory Component.
- All cross-component communication must route through the Orchestrator.
- `internal/comms` is the only package allowed to make outbound calls.
- Those calls are intended to be Orchestrator-addressed.

So under the current architecture, the answer is:

**No, Agents should not directly call Memory.**

### Why that rule exists

This is not just a style preference. It likely exists to preserve:

- a single coordination authority for cross-component interactions
- centralized routing and authorization
- auditability
- simpler policy enforcement
- fewer hidden component-to-component dependencies

If Agents are allowed to call Memory directly for skills, that becomes an architecture change, not just an implementation detail.

---

## Design Question

There are really two separate questions:

1. Where is the authoritative source of truth for skills?
2. Where does runtime skill search happen?

Those do not have to be the same place.

For example:

- Memory can be the persistent source of truth
- Agents can still keep a local in-memory runtime index for fast search

That distinction is important because it gives us cleaner options than "everything local" or "everything remote."

---

## Option 1: Keep Skills Local In Agents, Reuse Shared Embedding Service

### Shape

- Static skills remain file-backed in `agents-component`
- Synthesized skills continue to persist to Memory
- Agents keep the local in-memory skill tree and local semantic search
- Replace `Voyage-or-hash` with the new shared `embedding-api`

### Pros

- Smallest implementation change
- Easy to land quickly
- Preserves current local search performance
- Reuses the new embedding runtime immediately
- Does not require changing the skill-search control flow

### Cons

- Persistent source of truth for static skills remains outside Memory
- Conflicts with the project direction if "all durable state should live in Memory"
- Keeps a split authority model:
  - static skills in files
  - synthesized skills in Memory

### When this is a good choice

- When the priority is speed of migration
- When the team wants to standardize embeddings first and revisit skill storage later

---

## Option 2: Memory Is The Source Of Truth, Agents Load Skills Through Orchestrator, Search Remains Local

### Shape

- Memory becomes the authoritative persistent store for all skills:
  - static skills
  - synthesized skills
- Agents load skills through the existing Orchestrator-mediated read path
- Agents build a local in-memory search index at startup
- Runtime skill search still happens inside Agents

### Pros

- Matches the project plan that durable state lives in Memory
- Preserves the current Orchestrator-only architecture
- Keeps runtime search fast and local
- Avoids a cross-component call for every skill lookup
- Makes skills consistent with the broader persistence model

### Cons

- More work than Option 1
- Requires defining a proper skill read model through Orchestrator/Memory
- Requires a static skill seeding flow
- Requires startup sync / reload behavior design

### Why this is attractive

This is probably the cleanest architecture if we want to satisfy both:

- "persistent state should be in Memory"
- "Agents should not talk directly to Memory"

It separates:

- authoritative storage: Memory
- live execution/search cache: Agents

That is a very normal and healthy split.

---

## Option 3: Memory Is The Source Of Truth, And Memory Performs Skill Search

### Shape

- Skills are stored in Memory
- Agents ask for skill search results remotely
- Memory performs embedding lookup / vector search for skills
- Agents receive matched skills or commands from Memory

### Pros

- Maximum centralization
- One place to manage skill indexing/search behavior
- Potentially useful if multiple components need the same skill search service

### Cons

- Adds remote latency to a hot path
- Introduces more runtime dependency on cross-component availability
- Makes progressive skill disclosure more complex
- Pushes a highly interactive agent-local operation into another service
- Still must route through Orchestrator under current architecture
- Likely overcomplicates something that is currently simple and fast

### Why this is probably not the best default

Skill search is used by a running agent loop and benefits from being local, synchronous, and cheap.

Moving that search to Memory would make each lookup depend on:

- Orchestrator
- Memory
- embedding / vector search behavior

That feels like unnecessary coupling unless we have a very strong product reason to centralize search itself.

---

## Option 4: Allow Agents To Talk Directly To Memory For Skills

### Shape

- Skills live in Memory
- Agents query Memory directly for skill load and/or search
- This bypasses Orchestrator for this specific use case

### Pros

- Simpler than routing every skill read through Orchestrator
- Direct data path
- Could be easier to reason about mechanically

### Cons

- Violates the current documented Agents architecture
- Creates a special-case cross-component exception
- Weakens the "Orchestrator is the sole coordination authority" rule
- Opens the door to more exceptions later
- Requires explicit team approval as an architecture change

### Can this be done?

Technically: yes.

Architecturally under the current rules: no, not without changing the rules.

If the team wants this path, it should be treated as an intentional architecture decision:

- update `CLAUDE.md`
- define exactly what kinds of Memory access are allowed
- define whether it is startup-only, read-only, or also runtime search

This should not happen accidentally or implicitly.

---

## Recommended Direction

If we want to align with both the project direction and the current Agents architecture:

**Recommended: Option 2**

- Memory is the authoritative persistent store for all skills
- Orchestrator mediates the reads/writes
- Agents build and maintain a local in-memory search index
- Agents use the shared embedding model/service to build that local index

This gives us:

- durable skills in Memory
- no direct Agents -> Memory violation
- fast runtime search
- a clean path to unify static and synthesized skill handling

If we want the smallest short-term migration while deferring architecture cleanup:

**Short-term bridge: Option 1**

- keep local skill authority temporarily
- switch only the embedder/runtime

But that should be treated as a transitional state, not the final design.

---

## Seeding Static Skills

If Memory becomes authoritative for static skills, we need a way to seed them.

There are three realistic ways to do that.

### Seed Option A: Deploy-Time Init Job

Shape:

- A startup/init job reads the static skills config
- It writes those skills into Memory before Agents starts using them

Pros:

- Clear ownership of bootstrap
- Keeps seeding outside of the long-running Agents process
- Works well in Kubernetes/Helm environments

Cons:

- More deployment plumbing
- Need to define idempotency and update semantics

### Seed Option B: Agents Component Seeds Static Skills On Startup If Missing

Shape:

- `agents-component` loads static skill config at startup
- It checks whether static skills are already present in Memory
- If not, it writes them through Orchestrator

Pros:

- Easiest to implement
- Keeps logic close to current skill-loading code
- Naturally reuses existing config parsing

Cons:

- Blurs "consumer" and "bootstrapper" responsibilities
- Need careful idempotency rules
- Need to avoid duplicate/static skill drift

### Seed Option C: Orchestrator Owns Static Skill Seeding

Shape:

- Orchestrator or a coordinated control-plane path seeds skills into Memory

Pros:

- Strong fit if Orchestrator is the central authority for cross-component state

Cons:

- More moving parts
- Probably more work than needed for the first pass

### Best first seeding approach

For a first implementation:

**Best practical option: Seed Option B**

Let Agents seed static skills through the existing Orchestrator-mediated memory write path if they are missing.

It is the smallest change that still moves us toward Memory-authoritative skills.

Longer-term, a dedicated init job may be cleaner.

---

## What Should Actually Be Stored In Memory?

If Memory becomes authoritative, the stored records should probably include:

- domain
- command name
- label
- description
- parameter spec
- origin:
  - static
  - synthesized
- version / updated timestamp
- maybe a stable source identifier for static config provenance

Open design question:

- should embeddings themselves also be stored in Memory for skills?

Possible answers:

- **No, not initially**
  - store canonical skill definitions only
  - Agents compute embeddings on load
- **Maybe later**
  - store precomputed embeddings if startup cost becomes a problem

Initial recommendation:

**Do not store skill embeddings in Memory initially.**

Store the skill definitions only, and let Agents build the local embedding index from those definitions.

That keeps Memory simpler and avoids locking skill storage to one embedding format too early.

---

## Runtime Search: Local Or Remote?

### Local search in Agents

Pros:

- fast
- no network hop per lookup
- fits current design
- easier progressive disclosure

Cons:

- requires a startup load/cache step

### Remote search through Memory

Pros:

- central search implementation

Cons:

- slower
- more coupling
- more runtime dependency on other services

Recommendation:

**Keep runtime search local in Agents.**

Use Memory for persistence, not for every search call.

---

## Practical Migration Sequence

If we want the more correct long-term path, a reasonable sequence is:

1. Standardize the embedder runtime for Agents
   - replace `Voyage-or-hash` with the shared local embedding service

2. Define a canonical persistent skill record shape in Memory
   - enough to represent both static and synthesized skills

3. Add static skill seeding
   - likely startup-if-missing first

4. Add skill read/load through Orchestrator-mediated Memory access
   - Agents loads all relevant skills at startup

5. Build local in-memory embedding index from loaded skills
   - keep `Search(...)` local

6. Retire file-authoritative static skills
   - config may remain as a seed source, but not the final durable authority

---

## Decision Summary

### If we optimize for least code change right now

Choose:

- local skill authority in Agents
- shared embedding service for search

### If we optimize for project consistency and long-term architecture

Choose:

- Memory-authoritative skills
- Orchestrator-mediated load/sync
- local search/index in Agents

### What we probably should not do by default

- remote Memory-backed search for every skill lookup
- direct Agents -> Memory calls without an explicit architecture change

---

## Suggested Recommendation To Adopt

Use this as the default direction unless the team decides otherwise:

1. Skills should be durably stored in Memory.
2. Agents should not talk directly to Memory under the current architecture.
3. Skill reads/writes should route through Orchestrator.
4. Agents should keep a local runtime skill cache and local semantic search index.
5. Static skills should be seeded from config into Memory, then treated as Memory-authoritative.

That gives us the cleanest balance of:

- architectural consistency
- runtime performance
- operational simplicity
- future flexibility
