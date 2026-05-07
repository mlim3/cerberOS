# AGENTS.md — cerberOS Agent Skills

This file configures AI coding agents (Claude Code, Cursor, Copilot, etc.) working in this repository. It describes which agent skills are available, how to install them, and how they apply to cerberOS development.

---

## Required: Install Superpowers

**[Superpowers](https://github.com/obra/superpowers)** is a skills framework that gives agents structured workflows for TDD, debugging, planning, code review, and more. It is the foundation for all agent work in this repo.

### Cursor

```
/add-plugin superpowers
```

### Claude Code

```
/plugin install superpowers@claude-plugins-official
```

### GitHub Copilot CLI

```
copilot plugin marketplace add obra/superpowers-marketplace
copilot plugin install superpowers@superpowers-marketplace
```

### Gemini CLI

```
gemini extensions install https://github.com/obra/superpowers
```

Once installed, Superpowers activates automatically — agents will use brainstorming, TDD, systematic debugging, git worktrees, code review, and planning workflows without being explicitly asked.

---

## Repo-Specific Skills

The following skills live in `.cursor/skills/` and are available automatically to Cursor agents. Other agents should read these files directly when the task applies.

### `github-cli-workflow`

**File:** `.cursor/skills/github-cli-workflow/SKILL.md`

Full `git` + `gh` automation for this repo: conventional commits, push/ship workflows, creating and tackling GitHub issues by label, sub-issue decomposition, git worktrees for isolation, and PR creation with structured bodies.

**Use when:** pushing code, creating PRs, working issues by label or number, breaking work into sub-tasks, or anything involving `git`/`gh` end-to-end.

---

## Key Project Context

Before writing any code, read the relevant `CLAUDE.md` files:

| Component | CLAUDE.md path |
|---|---|
| Vault (credential broker) | `vault/CLAUDE.md` |
| Agents Component | `agents-component/CLAUDE.md` |

These files contain settled architectural decisions, interface contracts, security invariants, and build/test commands. **Do not re-litigate settled decisions.**

### Critical constraints (non-negotiable)

- All cross-component communication routes through the **Orchestrator** via NATS — no direct component-to-component calls.
- **No raw credential values** anywhere: not in structs, NATS payloads, logs, or error messages.
- Go stdlib only in `engine/` — no third-party Go modules without strong justification.
- `internal/comms` is the **only** package permitted to make outbound network calls.

---

## Worktree Convention

Use `.worktrees/<branch-name>` for isolated feature work. The directory is gitignored. See the `using-git-worktrees` skill (from Superpowers) for the full setup process.

```bash
git worktree add .worktrees/<branch-name> -b <branch-name> origin/main
```

---

## Labels Reference

| Label | Meaning |
|---|---|
| `Agents` | Agents Component work |
| `Vault` | Credential Vault / OpenBao work |
| `Orchestrator` | Orchestrator Component work |
| `Memory` | Memory Component work |
| `Comms` | Communications Component work |
| `IO` | I/O Component work |
| `P0`–`P3+` | Priority tiers |
| `documentation` | Docs / CLAUDE.md / AGENTS.md |
| `enhancement` | New features |
| `good first issue` | Newcomer-friendly tasks |
