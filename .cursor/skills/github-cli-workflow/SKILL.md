---
name: github-cli-workflow
description: Full git and GitHub CLI automation—conventional commits (or repo-inferred style), add/commit/push, PRs, tackling issues (user-named labels, confirm parent issue before starting), creating sub-issues under that parent to decompose work, git worktrees for isolation. Use when the user says to push or ship code, create a PR, work issues by label or number, break work into sub-tasks on GitHub, or wants gh/git end-to-end; combine with other applicable skills when completing those tasks.
---

# GitHub CLI and Git workflow

Assume a Unix-like shell (`bash`/`zsh`). Prefer **`gh`** for GitHub and **`git`** for the local repo.

**Automation scope:** For **push / ship**, run the full loop unless blocked (conflicts, hooks, auth). For **issues**, follow **Label input**, **Confirmation before tackle**, and **Use all applicable skills** below—do not skip user confirmation when it applies.

---

## Use all applicable skills

While completing any task this skill covers, **invoke and follow every other available skill** whose description fits the work (for example worktrees, TDD, systematic debugging, subagent-driven development, brainstorming, finishing a branch, canvas for heavy artifacts). This skill is one layer; **stack** it with others rather than treating Git/GitHub steps as the only rules.

If the user says to **use whatever skills help**, **apply every relevant skill** you have access to for that session—especially when triaging, implementing, testing, or shipping.

---

## Label input (issues)

**Issue labels filter comes from the user.** They tell you which label(s) to use (e.g. `Agent`, `good first issue`). **Do not guess** labels.

- If they gave **no label and no issue number**, **ask**: which label(s) to filter, or which issue **#number** / URL to use.
- Use **`gh issue list`** with **`--label`** exactly as agreed (multiple `--label` flags per `gh` semantics).

---

## Confirmation before tackle

**Before** creating a worktree, branch, or editing code for an issue, **confirm with the user** which issue you will tackle:

- **Confirmation required:** User only specified label(s) or vague intent ("tackle Agent issues"). Then **list** matching open issues (number, title, URL) and **ask them to confirm** one issue—or to pick from the list. **Wait** for that confirmation **before** `git worktree add` / implementation.
- **Confirmation skipped (already explicit):** User named a **specific issue** (`#42`, "issue 42", full GitHub URL) as the target. Summarize (#n, title) once for clarity, then proceed without blocking on a second approval unless they correct you.

If only **one** issue matches the filter, still **present it and ask** "Proceed with #n …?" unless they already pointed at that issue by number.

---

## Sub-issues (decompose work)

After the **parent issue is confirmed** (see **Confirmation before tackle**), the agent **may create GitHub sub-issues** under that parent to organize work—**no extra confirmation** unless the repo or user policy forbids creating issues. Use sub-issues when the parent is multi-step, risky to do in one shot, or clearer progress tracking would help.

**Try in order** (what works depends on `gh` version and repo features):

1. **Native `gh` (Issues 2.0 / parent linkage)** — if `gh issue create -h` lists **`--parent`**:

   ```bash
   gh issue create --parent <parent#|URL> --title "…" --body "…"
   ```

   Link or adjust later if needed:

   ```bash
   gh issue edit <child#> --set-parent <parent#>
   gh issue edit <parent#> --add-sub-issue <child#>
   ```

2. **`gh-sub-issue` extension** — if native flags are missing:

   ```bash
   gh extension install yahsan2/gh-sub-issue
   gh sub-issue create --parent <parent#> --title "…" --body "…"
   gh sub-issue list <parent#>
   ```

3. **Fallback** — create a normal issue whose body states **`Tracks #<parent>`** / **`Part of #<parent>`**, then **`gh issue comment <parent>`** listing the new issue, or edit parent with a checklist. Relationships may be looser than native sub-issues but stay traceable.

**Practice:** Short, imperative sub-issue titles; reference sub-issues in PR bodies (`Fixes #child` when that PR completes the sub-task; parent completion may be `Fixes #parent` on the final integrating PR). Close sub-issues as work lands so the graph stays honest.

---

## Prerequisites

- **`git`**, **`gh`** installed; `origin` points at GitHub.
- `gh auth login` once; verify with `gh auth status`.
- Repo-scoped commands: add `-R owner/repo` when not in the target clone.

---

## Commit message convention

**Default:** [Conventional Commits](https://www.conventionalcommits.org/) — `type(optional-scope): description` (imperative mood, ~50 chars subject line). Types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`. Breaking change: `feat!: …` or footer `BREAKING CHANGE:`.

**If the user specifies another style**, follow theirs.

**Otherwise infer from the repo** (read-only discovery, quick):

1. Project docs: `CONTRIBUTING.md`, `AGENTS.md`, `.github/*.md`, `COMMIT_CONVENTION.md`.
2. Recent history: `git log -20 --oneline` and sample full messages: `git log -5 --format=%B`.
3. If messages clearly follow another pattern (e.g. `[JIRA-123]`, `Merge branch`, or pure sentence case without types), **match that pattern** for consistency.
4. If still ambiguous, **use conventional commits**.

**Body:** Use when the change needs context; second line blank; wrap at ~72 chars. **Footers:** `Fixes #123`, `Refs #456`, `Co-authored-by:` as needed.

---

## Automation: "push the code" / ship / commit and push

When the user asks to push, ship, or save work to GitHub **without** splitting into manual steps, execute end-to-end:

1. **`git status`** and **`git diff`** (and `git diff --staged` if anything staged). If working tree is clean, report that and stop (or only push if ahead of remote).
2. **Safety:** Do not stage or commit secrets, obvious local-only files, or paths the user clearly wanted untracked. Respect `.gitignore`; if unsure, prefer staging by path rather than blind `git add -A`.
3. **Branch:** If on the default branch (`main`/`master`) with only **this** task's work, create a feature branch when the change is non-trivial or project norms imply branches (`git switch -c <type>/<short-slug>`). If already on a topic branch, stay on it.
4. **Stage** all intentional changes for this delivery (`git add …`).
5. **Commit** using the convention section above. **Prefer atomic commits** — one logical unit of change per commit — so each commit is independently meaningful, reviewable, and cherry-pickable. Split whenever changes address different concerns (e.g. a refactor, a bug fix, and a new feature should be separate commits even if delivered together). Only bundle changes into one commit when they are genuinely inseparable (e.g. a single-purpose feature with its tests and types).
6. **`git push`** — use **`git push -u origin HEAD`** when upstream is not set.
7. If the user also wants a PR (or repo workflow expects it), go to **Open or update PR** below after push.

**Blocked states:** merge conflicts, hook failures, push rejected (pull/rebase with care—never force-push shared branches unless the user asks). Explain briefly and fix or ask once.

---

## Git worktrees for issue completion

**Default:** When completing an issue (implement + commit + push + PR), use a **dedicated git worktree** so the primary checkout stays untouched. **Invoke and follow the `using-git-worktrees` skill** for directory choice (`.worktrees/` vs `worktrees/` vs CLAUDE.md vs ask), **`git check-ignore`** for project-local paths, dependency setup, and baseline tests.

**From the main repository** (not inside another worktree unless intentional): after you know the branch name, create the worktree and branch in one step:

```bash
git fetch origin
git worktree add <resolved-path>/<branch-or-folder-name> -b <branch-name> [<start-point>]
```

Use `<start-point>` (e.g. `origin/main`) if you must branch from latest remote default. Then **`cd` into that worktree** and do all file edits, commits, **`gh`** commands, and `git push` there.

**After the PR is merged** (or if abandoning): remove the worktree when done — `git worktree remove <path>` from the primary repo — and align with **`finishing-a-development-branch`** if your session uses it.

**Skip the worktree** only when the user explicitly wants work in the current tree, or the task is clearly trivial and they agree to in-place work.

---

## Automation: tackle open issues (by label, assignee, etc.)

When the user asks to work on, fix, or "take" issues (using the **labels they specified**—see **Label input**):

1. **Discover issues**

   ```bash
   gh issue list --state open --label "<user-specified-label>" --json number,title,labels,assignees,url --limit 30
   ```

   Replace the label placeholder with what the user asked for. Multiple labels: repeat `--label "L1" --label "L2"` per your `gh` version; for OR semantics, use separate queries or `gh api` with GraphQL. Prefer **`--json`** for triage.

2. **Confirm which issue** per **Confirmation before tackle**—do not start implementation until the user confirms the issue (unless they already named **#n** / URL).

3. **Optional — sub-issues:** If decomposition helps, create **Sub-issues** under the confirmed parent (**Sub-issues**) before or while implementing.

4. **Branch** naming: `issue-<number>-short-kebab-slug` or `feat/issue-123-slug` / `fix/issue-123-slug`.

5. **Isolate with a worktree** per **Git worktrees for issue completion** (using-git-worktrees); implement only inside that worktree path.

6. **Implement** the fix/feature; run tests/lint per project norms; apply other skills as needed (**Use all applicable skills**).

7. **Commit** message: conventional header + optional footer **`Fixes #<n>`** for auto-close on merge (GitHub)—use **`#parent`** and/or **`#child`** sub-issues as appropriate.

8. **Push** as in **Automation: "push the code"** from within the worktree.

9. **PR:** Create with **`gh pr create`**; title often mirrors commit or issue title; body should include **`Fixes #<n>`** or **`Closes #<n>`** (and a short summary). Link related issues with `Refs #m` if partial.

10. **Optional:** Comment on the issue: `gh issue comment <n> --body "Working on this in <PR-url>"` when useful for the team.

---

## Open or update PR (after push)

| Goal                   | Command                                                                |
| ---------------------- | ---------------------------------------------------------------------- |
| Create PR              | `gh pr create --title "type(scope): summary" --body "$(cat <<'EOF' ... EOF)"` |
| Draft                  | add `--draft`                                                          |
| Existing PR for branch | `gh pr view` or `gh pr list --head <branch>`                           |

CI: `gh pr checks` and/or `gh run list --branch <branch>`.

### PR body structure

Every PR body should include these sections (omit any that genuinely don't apply):

```
## Overview
One short paragraph — what this PR does and why, written for a reviewer who hasn't seen the branch.

## Changes

### New files
- `path/to/file.ts` — what it does in one line

### Modified
- `path/to/file.ts` — what changed and why

### Removed *(if any)*
- `path/to/file.ts` — why it was deleted

## Testing
Step-by-step instructions a reviewer can follow to verify the feature or fix locally.
Include any required env vars, seed data, URLs, or credentials to obtain.

## Notes *(optional)*
Anything that doesn't fit above: trade-offs, follow-up work, links to docs/issues.

Fixes #123
```

Pass the body via a heredoc so newlines and backticks survive the shell:

```bash
gh pr create --title "feat(auth): add Google OAuth for judge page" --body "$(cat <<'EOF'
## Overview
…

## Changes

### New files
- `auth.ts` — NextAuth config …

### Modified
- `package.json` — adds next-auth@beta

## Testing
1. Copy `.env.example` → `.env.local` and fill in credentials
2. `pnpm dev` → navigate to /judge

Fixes #42
EOF
)"
```

---

## Local Git (reference)

| Goal          | Command                                                      |
| ------------- | ------------------------------------------------------------ |
| Status / diff | `git status`, `git diff`, `git diff --staged`                |
| Stage         | `git add <path>`                                             |
| Commit        | `git commit -m "…"` (multi-line: heredoc or multiple `-m`)   |
| Amend         | `git commit --amend` (no `--no-edit` only if fixing message) |
| Branch / push | `git switch -c …`, `git push -u origin HEAD`                 |

**Do not** `git push --force` or rewrite published shared branches unless the user explicitly requests it.

---

## Pull requests (`gh pr`)

| Goal             | Command                                                                 |
| ---------------- | ----------------------------------------------------------------------- |
| List / view      | `gh pr list`, `gh pr view [<n>]`, `gh pr diff`                          |
| Checkout         | `gh pr checkout <n>`                                                    |
| Merge            | `gh pr merge <n>` (`--squash`, `--rebase`, or merge; `--delete-branch`) |
| Comment / review | `gh pr comment`, `gh pr review`                                         |

Use `--base <branch>` when not targeting the default branch.

---

## Issues (`gh issue`)

| Goal                            | Command                                                                                              |
| ------------------------------- | ---------------------------------------------------------------------------------------------------- |
| List (human)                    | `gh issue list --label Agent -L 20`                                                                  |
| List (agent)                    | `gh issue list --label Agent --json number,title,url`                                                |
| View                            | `gh issue view <n>`                                                                                  |
| Create / close / comment / edit | `gh issue create`, `gh issue close`, `gh issue comment`, `gh issue edit`                             |
| Sub-issue (native)              | `gh issue create --parent <parent#>` if supported; `gh issue edit … --set-parent`, `--add-sub-issue` |
| Sub-issue (extension)           | `gh sub-issue create --parent <n> --title "…"` (see **Sub-issues**)                                  |

---

## Repository helpers

| Goal         | Command                                   |
| ------------ | ----------------------------------------- |
| Root         | `git rev-parse --show-toplevel`           |
| Remote       | `git remote -v`, `gh repo view`           |
| Clone / fork | `gh repo clone`, `gh repo fork … --clone` |

---

## API escape hatch

`gh api …` for filters or fields `gh issue list` does not expose; prefer typed `gh` when sufficient.

---

## Agent defaults (summary)

| Situation             | Behavior                                                                                                                               |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Push / ship           | Full loop: inspect → branch if needed → stage → commit (convention + infer) → push → PR if expected                                    |
| Issue labels          | **User names** label(s); if missing, **ask**—do not invent                                                                             |
| Confirm before tackle | **List + confirm** issue (or "proceed with #n?") unless user already gave **#n**/URL                                                   |
| Issues by label       | After confirm: optional **sub-issues** → `gh issue list` → **worktree + branch** → implement (+ other skills) → `Fixes #n` → push → PR |
| Sub-issues            | After **parent** confirmed, may create sub-issues to decompose; native `gh` → extension → body fallback (**Sub-issues**)               |
| Other skills          | **Stack** applicable skills for the whole task when user asks or when work clearly needs them                                          |
| Issue completion      | **Default:** new worktree per issue; **override** if user says in-place only or trivial + agreed                                       |
| Commits               | Conventional by default; override from docs + `git log`; **atomic commits** by default — split by logical boundary so each commit is cherry-pickable |
| PR body               | Structured: **Overview**, **Changes** (new/modified/removed), **Testing** steps, optional **Notes**; pass body via heredoc             |
| Safety                | No secrets in commits; no force-push to shared defaults                                                                                |

---

For submodules, signed commits, or complex rebases, follow project docs—this skill targets routine GitHub automation.
