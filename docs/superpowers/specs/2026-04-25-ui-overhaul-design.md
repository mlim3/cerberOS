# UI Overhaul Design Spec

**Issues:** #148, #149, #150, #151
**Date:** 2026-04-25
**Status:** Draft

---

## Context

The cerberOS IO chat surface is functional but visually heavy. As agent outputs grow (plans, tool calls, status updates), the UI needs to get out of the way. This overhaul strips the interface back to essentials — content over chrome, whitespace over borders — while adding three new features that give users visibility into what the agent is doing.

**References:** Linear, Raycast, Arc — minimal chrome, maximum content clarity.

**Strategy:** Foundation-first, 4 sequential PRs. Each PR builds on the previous.

---

## PR 1 — #148: Minimalist Redesign (Foundation)

### Design Direction: Refined Minimal

A slim single-line header replaces the current multi-line header. Status dots replace status pills. The sidebar is narrower and text-only with zero decoration. The overall palette stays dark-first but with more breathing room.

### Layout Changes

**Header** (current → new):
- Current: Multi-line with title, status pill, ETA, last update. `min-height: 56px`.
- New: Single line. Left: task title (14px, weight 500). Right: status dot + phase text (10px). Height: ~40px. No ETA or last-update line — that information moves to the sidebar task item and the inline progress indicator.

**Sidebar** (current → new):
- Current: 340px wide. Cards with backgrounds, padding, borders.
- New: ~260px wide. Text-only items with status dot + title + one-line preview. Selected item: `background: rgba(255,255,255,0.03)`, border-radius 6px. No card backgrounds, no borders on individual items.

**Chat area**:
- Increase horizontal padding from 24px to 32–40px for wider viewports.
- User messages: `color: var(--text-secondary)` (subdued).
- Agent messages: `color: var(--text-primary)` (prominent).
- Message spacing: 20–24px between messages (up from current).

**Empty state**:
- Keep the ASCII logo but reduce opacity further. Simplify helper text.

### Design Token Updates

```
Spacing scale:    4 / 8 / 12 / 16 / 24 / 32 / 40 / 64
Type scale:       9 / 10 / 11 / 12 / 14 / 16 / 20
Font weights:     400 (body), 500 (emphasis), 600 (headings — used sparingly)
```

**Color refinements:**
- `--bg-dark`: `#141414` (deeper)
- `--bg-sidebar`: `#181818` (closer to main, less contrast)
- `--bg-main`: `#141414`
- `--border`: `1px solid rgba(255,255,255,0.04)` (barely visible)
- `--border-subtle`: `1px solid rgba(255,255,255,0.06)` (for interactive elements)
- Status dots: 6px circles using existing status colors (`--success`, `--warning`, `--stuck`)
- Keep `--accent: #6495ed` and `--text-primary: #ddeedd`

### PlanPreviewCard Restyle

The plan approval card stays as a static readable list. Restyle with new tokens:
- Remove heavy borders/backgrounds
- Use the new spacing scale
- Step numbers in muted text, step descriptions in primary text
- Approve/reject buttons: ghost style (border only, no fill) until hover
- Max-height: 40vh with internal scroll (existing behavior from #147 fix)

### Components Affected

| File | Changes |
|------|---------|
| `index.css` | Updated token values, new variables |
| `App.css` | Header simplified to single line, layout padding |
| `App.tsx` | Remove ETA and last-update from header JSX |
| `TaskSidebar.tsx` | Narrower, text-only items with status dots |
| `TaskSidebar.css` | New sidebar styles |
| `ChatWindow.css` | Increased padding, message spacing, role-based colors |
| `PlanPreviewCard.css` | Restyle with new tokens |
| `SettingsPanel.css` | Restyle to match |
| `SettingsButton.css` | Restyle to match |
| `SidebarLogo.css` | Simplify |
| `CredentialModal.css` | Restyle to match |
| `CredentialRequestCard.css` | Restyle to match |
| `ActivityLog.css` | Restyle to match |
| `VoiceRecorder.css` | Restyle to match |

---

## PR 2 — #149: Progress Indicator

### Inline Ghost Element

A subtle status line that appears below the last message when the agent is working. Updates in place as the agent moves through phases.

**States:**
- `Thinking...` — default when agent is processing
- `Running tool: <tool_name>` — during tool execution
- `Planning step N of M` — during plan generation
- `Executing step N of M` — during plan execution (bridges to PR 4)
- Disappears on completion or error

**Visual treatment:**
- Left-aligned, same horizontal position as messages
- Small animated dot (4px, `--accent` color, pulsing) + text in `--text-muted` at 10px
- Smooth fade-in on appear, fade-out on disappear (200ms)
- No layout shift — uses a reserved space that collapses when inactive

**Data source:**
- Hooks into existing SSE `status` events from orchestrator (`OrchestratorStreamEvent.type === 'status'`)
- `status.lastUpdate` field already contains human-readable phase text
- For tool calls (PR 3), the progress indicator updates when a tool starts and clears when it finishes

### Components Affected

| File | Changes |
|------|---------|
| `ChatWindow.tsx` | New `ProgressIndicator` inline element below messages |
| `ChatWindow.css` | Styles for progress indicator + pulse animation |
| `App.tsx` | Pass status/streaming state to ChatWindow |

**New component:** `ProgressIndicator.tsx` + `ProgressIndicator.css` (small, co-located)

---

## PR 3 — #150: Tool Call Visibility

### Left-Border Block Style

Tool calls render as collapsible inline blocks between user message and agent response, with a left accent border in cornflower blue.

**Collapsed state (default):**
- Left border: 2px solid `rgba(100, 149, 237, 0.4)`
- Background: `rgba(255, 255, 255, 0.015)`
- Border-radius: `0 6px 6px 0`
- Content: `▶ tool_name · duration`
- Tool name in `--text-secondary`, duration in `--text-muted`

**Expanded state (on click):**
- Left border brightens: `rgba(100, 149, 237, 0.6)`
- Background brightens: `rgba(255, 255, 255, 0.025)`
- Arrow rotates: `▶` → `▼`
- Below header: dark inset panel (`rgba(0, 0, 0, 0.3)`, border-radius 4px)
  - `INPUT` label (9px, muted) + input args
  - `OUTPUT` label (9px, muted) + result summary
  - Success: output in `--success` color
  - Error: output in `--warning` color

**Multi-tool sequences:**
- Stack vertically with 8px gap between blocks
- Each independently expandable

**Security:**
- IO layer scrubs credential values from tool inputs/outputs before rendering
- Never display raw values for keys matching `password`, `secret`, `token`, `credential`, `key` patterns
- Replace with `[REDACTED]`

### Data Model

Tool call events need to flow from orchestrator to the UI. This requires:

1. **New SSE event type:** `tool_call` added to `OrchestratorStreamEvent`
   ```typescript
   type ToolCallEvent = {
     type: 'tool_call'
     payload: {
       taskId: string
       toolName: string
       inputs: Record<string, unknown>  // scrubbed
       output?: string                   // scrubbed
       status: 'running' | 'completed' | 'error'
       durationMs?: number
     }
   }
   ```

2. **Core types:** Add `ToolCallEvent` to `@cerberos/io-core` types
3. **IO API:** Scrub sensitive fields before forwarding to web surface
4. **Chat message augmentation:** Tool calls attach to the preceding agent response turn

**Note:** If the orchestrator does not yet emit `tool_call` events, PR 3 should include mock tool call data in demo mode and wire the SSE handler so it's ready when the orchestrator adds support. The UI component ships first; the live data integration follows when the orchestrator contract is ready.

### Components Affected

| File | Changes |
|------|---------|
| `io/core/src/types.ts` | Add `ToolCallEvent` type to `OrchestratorStreamEvent` union |
| `io/api/src/index.ts` | Forward tool_call events, apply credential scrubbing |
| `App.tsx` | Handle `tool_call` SSE events, store in state, pass to ChatWindow |
| `ChatWindow.tsx` | Render tool call blocks between messages |

**New component:** `ToolCallBlock.tsx` + `ToolCallBlock.css`

---

## PR 4 — #151: Plan Execution Pipeline

### Vertical Depth-Stage Model

When a plan is approved and execution begins, the static plan list transforms into an animated vertical pipeline.

**Three visual zones:**

1. **Completed (past):** opacity 0.35–0.5, `scale(0.97)`, slight blur. Clickable to expand and see output.
2. **Active (current):** full opacity, full scale, subtle glow (`box-shadow: 0 0 8px rgba(100,149,237,0.2)`). Shows the inline progress indicator from PR 2. Background highlight: `rgba(100,149,237,0.06)` with `border: 1px solid rgba(100,149,237,0.1)`.
3. **Upcoming (next):** opacity 0.4, no blur, muted text colors. Only the immediate next step is shown; steps beyond N+1 are hidden or at very low opacity.

**Connecting line:**
- 1px vertical line between step circles
- Completed segments: `rgba(255,255,255,0.06)`
- Active → next segment: `rgba(100,149,237,0.2)`
- Future segments: `rgba(255,255,255,0.04)`

**Step circles:**
- Completed: `rgba(126,231,135,0.15)` background, `✓` in `--success`
- Active: `rgba(100,149,237,0.2)` background, solid 6px dot in `--accent`, glow
- Upcoming: `rgba(255,255,255,0.04)` background, step number in `--text-muted`

**Transitions:**
- All animations via CSS `transition` on `opacity`, `transform`, `filter`, `background`
- Duration: 400ms ease-out
- No JavaScript-driven animation
- `@media (prefers-reduced-motion: reduce)`: disable blur, scale, and transitions; rely on opacity alone

**Interaction:**
- Click a completed step → expand to show its output (same dark inset style as tool call blocks)
- Active step shows live progress indicator inline

**Graceful degradation:**
- Single-step plans: no pipeline chrome, just show the step as a simple status line
- Plan approval (pre-execution): remains a static list (PlanPreviewCard, restyled in PR 1)

### Data Flow

The pipeline uses the same `status` SSE events already flowing from the orchestrator:
- `status.lastUpdate` contains step descriptions
- Plan step progression is tracked by parsing step indicators from status payloads
- When a step completes, transition the pipeline to the next step

If the orchestrator adds structured plan progress events in the future, the pipeline component can consume those directly. For now, parsing from `lastUpdate` is sufficient.

### Components Affected

| File | Changes |
|------|---------|
| `App.tsx` | Track plan execution state, pass to new pipeline component |
| `PlanPreviewCard.tsx` | Add post-approval pipeline rendering mode |
| `PlanPreviewCard.css` | Pipeline styles, depth-stage animations |

**New component:** `PlanPipeline.tsx` + `PlanPipeline.css`

---

## Cross-Cutting Concerns

### Accessibility
- Status dots must have `aria-label` describing the status
- Tool call blocks: `role="button"`, `aria-expanded`, keyboard-navigable (Enter/Space to toggle)
- Pipeline: step labels always readable regardless of opacity
- `prefers-reduced-motion`: disable blur, scale, glow animations; use opacity-only transitions
- Font scaling (`data-font-scale="large"`) continues to work with new token values

### Performance
- All animations via CSS transforms and opacity (compositor-only, no layout thrashing)
- No JavaScript animation loops
- Tool call blocks render lazily (only expanded content loads on click)
- Pipeline step count bounded by plan size (typically 3–8 steps)

### Testing
- Visual regression: run the app in demo mode (`VITE_DEMO_MODE=true`) and verify all components render
- Tool call blocks: add mock tool call data to demo mode tasks
- Pipeline: add a mock plan execution sequence to demo mode
- Accessibility: keyboard navigation through tool blocks and pipeline steps
- Responsive: verify at 1280px, 1440px, 1920px viewport widths

---

## Verification Plan

For each PR:

1. **Build:** `cd io/surfaces/web && bun run build` — no errors
2. **Dev server:** `cd io/surfaces/web && bun run dev` — loads in browser
3. **Demo mode:** Set `VITE_DEMO_MODE=true` and verify all components render correctly
4. **Visual check:** Compare against mockups in `.superpowers/brainstorm/` session
5. **Accessibility:** Tab through all interactive elements, verify `prefers-reduced-motion`
6. **No regressions:** Existing functionality (chat, credentials, plan approval, settings) still works
