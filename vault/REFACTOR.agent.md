# Vault V1 refactor — agent execution brief

**Purpose:** This document restates `REFACTOR.md` in a form optimized for coding agents: fixed goals, explicit constraints, ordered work, and checkable outcomes. Read `REFACTOR.md` for the original intent; treat this file as the operational checklist.

---

## 1. Mission (one sentence)

Refactor Vault so it is **only a credential broker**: verify an agent may access a set of secrets (atomically), inject those values into a script template, and **return the script to the agent**—the agent runs the script in its own environment. Remove Vault’s role as the primary script executor (QEMU/sandbox “engine” execution path).

---

## 2. Preconditions

| Item | Value |
|------|--------|
| **Git branch** | `vault-core-v2` |
| **Tracking issues** | GitHub `#75` and `#76` in the CerberOS repo — read both before starting; align PR/commits with their acceptance criteria when they differ from this brief, issues win. |
| **Commits** | Conventional commits (`feat:`, `fix:`, `refactor:`, etc.). Commit at **logical milestones** (not one giant commit). |
| **Comments** | Concise; explain *why* for non-obvious choices so teammates can follow the refactor. |

---

## 3. Current state (context)

- **Two-service layout** (see `vault/compose.yaml`): `engine` (privileged QEMU-based execution, talks to secretstore) and `secretstore`. UI may depend on both.
- **CLI today** (`vault/engine/cmd/vault/`): oriented around **`vault execute`** — run scripts inside the engine environment with placeholder-based secret injection orchestrated there.
- **Security story today:** agent never sees secrets because execution happens inside Vault’s sandbox.

---

## 4. Target state — V1 behavior

1. **New primary CLI flow:** `vault inject <script> <list of secrets>`  
   - **Authorization:** Vault verifies the caller/agent is allowed to access **every** requested secret in **one atomic check** (all-or-nothing; no partial grants).  
   - **Output:** After success, Vault injects credential values into the script (same placeholder / templating model as today unless issues specify otherwise) and **prints or writes the completed script back** to the agent’s environment for the agent to run locally.

2. **Service consolidation:** Move from **two** main services (`engine` + `secretstore`) to **one** service where practical for this milestone—consistent with simplifying Vault’s responsibilities. Update `compose.yaml`, Dockerfiles, and docs that assume two services.

3. **Explicit V1 caveat (do not “fix” away):** Returning a script with plaintext secrets to the agent is **intentionally insecure for V1**. Do not block the refactor on hardening; **do** keep code structured with **clear abstractions** (interfaces, boundaries) so later versions can swap transport, encryption, or execution model without rewriting everything.

---

## 5. Non-goals (unless issues say otherwise)

- Perfect end-to-end security for secret delivery to the agent.  
- Preserving the QEMU/initrd execution path as the primary product surface (removal or demotion is in scope per `REFACTOR.md`).  
- Large unrelated cleanups outside the refactor’s path.

---

## 6. Suggested execution order (agents: follow in order unless issues reorder)

Use this as a default plan; adjust if `#75`/`#76` specify different sequencing.

- [ ] **6.1 Discovery** — Map all entrypoints: CLI flags, HTTP APIs (`openapi.yaml`), compose services, demo/tests (`vault/demo/`, `engine/cmd/vault/*_test.go`, `integration_test.go`). List what must change for `inject` vs `execute`.

- [ ] **6.2 Authorization + atomicity** — Implement or reuse secret-access checks so a single `inject` request succeeds only if **all** requested secrets are permitted. Document behavior when any secret is denied (clear error, no partial injection).

- [ ] **6.3 Injection path** — Implement `vault inject` using existing preprocessor/templating logic where possible; output must be suitable for the agent to run (stdout and/or file flag—pick one consistent pattern and document in CLI help).

- [ ] **6.4 Deprecate or remove engine execution** — Remove or gate `vault execute` and associated orchestrator/VM paths per issues; delete dead code paths that are no longer referenced **after** tests are updated.

- [ ] **6.5 Single service** — Collapse deployment to one Vault-related backend service; update `compose.yaml`, env vars, tokens between services, and any UI/Swagger assumptions.

- [ ] **6.6 Tests & demos** — Update Go tests and demo scripts/docs so CI and manual flows validate `inject` and the new topology. Remove or rewrite tests that only assert QEMU/execute behavior.

- [ ] **6.7 Documentation** — Update `engine/README.md`, `DOCS.md`, `MILESTONE_V1.md` (or issue-specified files) so they describe broker + `inject`, not sandbox execution as the default story.

---

## 7. Acceptance criteria (binary checks)

- [ ] `vault inject …` exists, documents its arguments, and enforces **atomic** access to the listed secrets.  
- [ ] Successful inject yields a script with secrets substituted; **failed** auth yields **no** usable mixed output (no half-injected secrets).  
- [ ] Deployments use **one** consolidated Vault service as specified by the refactor (compose reflects this).  
- [ ] Test suite and integration tests pass in the repo’s standard way (`go test`, compose-based tests if applicable).  
- [ ] Commits are conventional and scoped; PR addresses `#75` and `#76` with explicit links in the PR description.

---

## 8. Files and areas likely to change (hint list, not exhaustive)

- `vault/compose.yaml` — service topology  
- `vault/engine/cmd/vault/main.go` — CLI subcommands  
- `vault/engine/preprocessor/` — injection logic  
- `vault/engine/orchestrator/`, `vault/engine/vm/`, `vault/engine/initrd/` — shrink or remove per new model  
- `vault/secretstore/` — may merge, expose differently, or stay as library inside one binary (follow issues)  
- `vault/openapi.yaml`, `vault/demo/`, `vault/engine/README.md`  

---

## 9. Handoff line for the agent

Start on branch `vault-core-v2`, read issues **#75** and **#76**, then execute section **6** with acceptance checks in **7**. When in doubt, prefer **small commits**, **preserving abstractions for V2+**, and **matching existing code style** in touched packages.
