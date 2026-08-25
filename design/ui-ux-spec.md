# UI/UX Spec — Lexicode

**Version:** 0.1 · Companion to the Product Brief · August 2026
**Visual direction:** dense dev-tool, dark-first (Linear / Vercel / Sentry lane). Compact rows, monospace for anything machine-generated, keyboard-driven, light mode as a real second theme rather than an afterthought.
**Audience for V1:** a small self-hosted team. Light auth, human members alongside agents, activity attribution. No org/workspace layer above the project list.

> **Partially superseded — August 2026 (LEXI-13).** The UI is being rebuilt on Material UI; see
> [D-1 amendment A-1](../plan/00-decisions.md#amendment-a-1-lexi-13-august-2026--reversed-the-ui-moves-onto-material-ui).
>
> **Still binding:** everything about what each screen *does* — the information architecture (§1),
> the status vocabulary (§4), the screens (§5), the keyboard map (§6), the empty-state copy (§8),
> the interaction rules (§9), and the accessibility requirements (§10). §3's **token values** stay
> too: `styles/tokens.css` remains the single source of truth for colour, type and density, and the
> component library's theme consumes it by reference.
>
> **Superseded:** §7's component table as a build instruction ("the spec's component list is the
> component library"). Components now come from the library; a composition of library primitives is
> allowed and must say so. Where a §5 layout sketch and a library convention disagree, the
> convention wins. One rule is added on top of §9: **no action may be reachable only by keyboard** —
> every shortcut also has a visible, labelled control.

---

## 1. Information architecture

```
/                                Home — projects + cross-project "Needs you"
/inbox                           Everything awaiting a human, all projects
/settings                        Workspace: members, integrations, defaults, audit log

/p/:key                          Project Overview
/p/:key/board                    Board / list
/p/:key/board?view=backlog       Backlog (same view, filtered route)
/p/:key/triage                   Intake queue
/p/:key/t/:ticket                Ticket detail
/p/:key/wiki                     Wiki index
/p/:key/wiki/:slug               Wiki page
/p/:key/runs                     Run list
/p/:key/runs/:id                 Run detail
/p/:key/agents                   Agent roster
/p/:key/agents/:id               Agent config
/p/:key/triggers                 Trigger list
/p/:key/triggers/:id             Trigger editor
/p/:key/settings/*               Project settings (general, board, wiki, repo, members, danger)
```

Three IA rules:

1. **The project is the only container.** Nothing lives above it except the project list and the cross-project inbox. Nothing lives beside it.
2. **Settings live inside the object they configure**, as a tab on the project — not in a global settings app with a project dropdown.
3. **A tabbed entity page is the shape of the project.** One URL prefix, a persistent header, tabs beneath. This is a proven IA for "one thing with many facets" and it survives adding a tab later.

---

## 2. Chrome and layout

### 2.1 Global frame

```
┌──────────────────────────────────────────────────────────────────────────┐
│ [◈ Lexicode]  Home  Inbox ③        ⌘K search…            [avatar] [?]    │  40px top bar
├────────────┬─────────────────────────────────────────────────────────────┤
│ PROJECTS   │  ┌──────────────────────────────────────────────────────┐  │
│ ● Payments │  │ PAY · Payments Service              ⚙  ⟳ 2m ago      │  │  project header
│ ○ Web      │  ├──────────────────────────────────────────────────────┤  │
│ ○ Infra    │  │ Overview│Board│Triage②│Wiki│Runs④│Agents│Triggers│⚙  │  │  tabs
│            │  ├──────────────────────────────────────────────────────┤  │
│ ─────────  │  │                                                      │  │
│ NEEDS YOU  │  │              tab content                             │  │
│ ▲ PAY-14   │  │                                                      │  │
│ ▲ WEB-3    │  └──────────────────────────────────────────────────────┘  │
└────────────┴─────────────────────────────────────────────────────────────┘
   208px                              fluid
```

- Left rail collapses to icons at `⌘\` and below 1100px. Persisted per user.
- The **Needs you** block in the left rail is always present, always at the bottom, capped at 5 rows with a "+N more" link to `/inbox`. It is the only place in the chrome that carries a live count badge besides the tabs.
- Tab badges show counts only for actionable states: Triage shows untriaged, Runs shows needs-attention, not total.
- Top bar is fixed. Everything else scrolls.

### 2.2 Density

Base row height 32px. Compact mode (28px) available in preferences. Text 13px UI / 12px monospace. This is a tool for people who keep it open all day; whitespace is a cost, not a virtue — but each *region* gets clear separation via a 1px border and a background step, never via margin alone.

---

## 3. Design tokens

Defined as CSS custom properties on `:root`, redefined under `@media (prefers-color-scheme: dark)` guarded as `:root:not([data-theme="light"])`, and again under `:root[data-theme="dark"]` so an explicit toggle wins both ways.

### 3.1 Surfaces (dark values shown; light is the same ladder inverted)

| Token | Dark | Role |
|---|---|---|
| `--bg` | `#0b0d10` | app background |
| `--surface` | `#121519` | panels, cards |
| `--surface-2` | `#181c22` | raised: hover rows, popovers, inputs |
| `--surface-3` | `#1f242b` | pressed, selected row |
| `--border` | `#232830` | hairlines |
| `--border-strong` | `#333a45` | focused inputs, active tab underline |
| `--text` | `#e6e9ef` | primary |
| `--text-2` | `#9aa4b2` | secondary, labels |
| `--text-3` | `#6b7480` | tertiary, timestamps, disabled |

### 3.2 Semantic colors

One hue per meaning, used everywhere that meaning appears. No decorative color.

| Token | Hue | Means |
|---|---|---|
| `--accent` | indigo `#6c7cff` | interactive, primary action, links |
| `--running` | cyan `#3fb8d4` | a run is in flight (also the pulse animation) |
| `--needs-you` | amber `#e0a33e` | a human is blocking progress |
| `--ok` | green `#3fb37f` | succeeded, merged, passing |
| `--fail` | red `#e05563` | failed, error, changes requested |
| `--halt` | violet `#a072e0` | loop stopped, budget exceeded, superseded, debounced |
| `--muted` | gray `#6b7480` | queued, no action, canceled, archived |

`--halt` earning its own hue is deliberate. "The system deliberately stopped this" is not a failure and is not a success, and collapsing it into either is the single most common source of confusion in automation products.

### 3.3 Type

- UI: system stack — `ui-sans-serif, -apple-system, "Segoe UI", Inter, sans-serif`
- Machine output: `ui-monospace, "SF Mono", "JetBrains Mono", Menlo, monospace`
- Scale: 11 (micro/badges) · 12 (mono, meta) · 13 (body/UI) · 15 (section headers) · 20 (page titles). Nothing larger anywhere in the app.

### 3.4 Motion

Only three animations exist:
- 120ms ease-out for hover/press state changes.
- A 2s pulse on the `--running` dot.
- New log lines fade in over 80ms. Nothing slides, nothing bounces.

---

## 4. Status vocabulary

One vocabulary, used identically on run rows, ticket cards, trigger cards, and the inbox.

### 4.1 Run states

| State | Color | Dot | Means | Human action |
|---|---|---|---|---|
| `queued` | muted | ○ | waiting for a container slot | none |
| `provisioning` | running | ◐ | container starting, repo cloning | none |
| `running` | running | ● pulse | agent working | optional: watch, steer |
| `needs input` | needs-you | ▲ | agent asked a question | answer |
| `awaiting approval` | needs-you | ▲ | a gate you configured | approve / edit / deny |
| `completed` | ok | ✓ | finished, output delivered | review |
| `failed` | fail | ✕ | errored out; branch pushed with partial work | triage |
| `timed out` | fail | ✕ | exceeded wall-clock limit | triage |
| `canceled` | muted | ⊘ | stopped by a human | none |
| `loop stopped` | halt | ⊗ | depth guard tripped | inspect the chain |

### 4.2 Trigger outcome classes

`succeeded` · `no action` (conditions not met) · `awaiting approval` · `errored` · `debounced` · `superseded` · `loop stopped` · `budget exceeded`.

`no action` is its own class with its own color. "The rule fired but nothing happened" is the most common user confusion in every automation product studied, and giving it a name solves most of it.

### 4.3 The four flavors of "needs you"

Never render a generic "waiting" badge. Every needs-you row states which of these it is, in words:

- **Answer a question** — agent hit an ambiguity and asked.
- **Approve a plan** — a configured gate before execution.
- **Review output** — a PR or a proposed wiki page is ready.
- **Fix a failure** — a run died and nothing else will proceed.

---

## 5. Screens

### 5.1 Home — `/`

Two regions.

**Needs you (top, always).** A horizontally scrolling row of cards, one per blocked item across all projects, sorted with *answer a question* first, then *approve*, then *failed*, then *review*. Each card: project key, ticket key + title, agent avatar, the flavor in words, elapsed time, and a single primary action inline (Answer / Approve / View diff). Answering a question is possible **from this card without navigating** — that is the whole point of the surface.

**Projects (below).** A dense table: name, repo, open tickets, running agents (with a live count and pulse), needs-you count, spend today against the ceiling, last activity. Row click → project overview.

Empty: *"No projects yet — Create your first project"*, single CTA, everything else dimmed.

### 5.2 Project Overview — `/p/:key`

**About card** across the top: description, owner, repo + branch + last commit sha and message, agent count, open tickets, runs today, spend today / ceiling. Highest-density orientation device in the app; it is the first thing a returning user reads.

**Three columns below:** recent runs (last 10, compact), pinned wiki pages, and activity feed (tickets created/moved, PRs opened, triggers fired, agent proposals).

### 5.3 Board — `/p/:key/board`

**Header row:** layout toggle (board / list, `⌘B`), `group_by` picker (status / assignee / delegate / priority / label), filter chips, display-properties menu (`⇧V`), search, `+ New ticket` (`C`).

**Pinned lane — "Needs you (3)".** Full-width, above the columns, amber left border, cannot be collapsed away when non-empty, cannot be reordered. Auto-populated from tickets whose run is in a needs-you state. Cards here render the flavor in words and an inline action.

**Columns.** Custom, ordered, each with a category chip visible in board settings (not on the board itself — it would be noise). Header shows name, count, and — if set — a WIP limit as `3/4`, turning amber at the limit and red over it. On the `running` column the limit is enforcing: a run that would exceed it queues, and the header says `4/4 · queued: 2`.

A column may carry an **auto-start** marker (`⚡ auto-runs delegate`). It is the only visual on a column header besides count and limit, and it exists because silent agent spend is unacceptable.

**Card anatomy** (badges appear only when earned — a comment count shows only if there are comments):

```
┌────────────────────────────────────────┐
│ PAY-14                    ▲ needs you  │  key · status dot
│ Add idempotency keys to charge API     │  title, 2 lines max
│ ▸ 3/5 acceptance criteria              │  progress, only if criteria exist
│ [dev] ⟶ @spruce      +142 −18   #219  │  delegate → assignee, diff, PR
└────────────────────────────────────────┘
```

Drag between columns writes the grouped property. **Drag never starts a run.** Starting is `D` (delegate), the Run button on the ticket, or a trigger.

### 5.4 Ticket detail — `/p/:key/t/:ticket`

Two columns: main stream (fluid) + properties sidebar (280px, `⌘I` toggles).

**Main column, in order:**
1. Title (inline editable, `R` to rename).
2. Description — markdown editor with slash commands. Same editor component as the wiki and comments.
3. **Acceptance criteria** — a checklist, first-class, not part of the description. This is what the run gets injected into its prompt and what the run summary checks off.
4. Sub-tickets — one level. `⌘⇧O` creates one; selecting text in the description and pressing `⌘⇧O` converts the selection into sub-tickets. This is the primary agent→human handoff: an agent writes a plan as a checklist, a human turns it into work in one gesture.
5. **Unified stream** — comments, status changes, agent run cards, trigger firings, PR events, all one chronological feed. No tab split. A run appears here as a **collapsed session card** showing agent, status, elapsed, cost, and a one-line current-step; expanding shows the full activity inline; a "open full run" link goes to `/runs/:id`.
6. Composer at the bottom. `@` mentions people, agents, wiki pages, and tickets. Mentioning an agent starts a run scoped to this ticket.

**Sidebar:** status · priority · **assignee (human)** · **delegate (agent)** — visibly separate rows with different iconography, never a single polymorphic field · labels · linked PR with check status · branch (with a copy-command button) · created/updated.

### 5.5 Triage — `/p/:key/triage`

Single-column list of tickets created by triggers or agents. Keyboard-first: `1` accept (moves to the board's default backlog column), `2` mark duplicate (merges, transfers attachments), `3` decline (cancels with an optional reason), `H` snooze (until a time **or until new activity**). `J`/`K` to move, `Space` to peek.

Each row shows what created it: *"Created by trigger `CI failed → file a ticket` from run #482."* Provenance is the whole reason this screen exists.

### 5.6 Wiki — `/p/:key/wiki`

Three columns: tree (220px) · page · outline + backlinks (240px).

**Tree.** Two levels maximum. Each row shows the title and an `agent_scope` badge: `ALWAYS` (amber, because it costs context on every run), `AUTO`, `PATHS`, `MANUAL`, `NEVER` (muted). Search box pinned at the top of the tree, `/` focuses it. Search outranks the tree — it is what saves you from a bad tree.

**Page header:** title, owner avatar, `verified until 2026-11-01` (turning red past due), scope badge with an edit affordance, tags.

**Context budget strip** at the top of the tree: *"Always-on: 3 pages · ~2.4k tokens"* with a meter. Turns amber past the project threshold with the advice inline — *cut what the agent can read from the code, keep pitfalls, rationale, and conventions that differ from defaults.*

**Backlinks pane** at the bottom right: linked mentions with the full containing paragraph as context (a bare list of titles is useless), plus a collapsed `Unlinked mentions (3)` disclosure with one-click linking.

**Agent proposals.** When an agent proposes a page or an edit, it appears in the tree with a dashed border and a `PROPOSED` chip, and the page renders as a diff with Accept / Edit / Dismiss. Never auto-written.

### 5.7 Runs — `/p/:key/runs`

**List.** Columns: status dot + label · agent · ticket · trigger (or "manual") · duration · cost · started. Default filter is a saved view called **Needs attention** (`needs input` + `awaiting approval` + `failed` + `loop stopped`). Filter chips are individually removable, and the filtered-empty state is different copy from the never-had-any state.

**Detail — three panes.**

```
┌──────────────────┬────────────────────────────────┬──────────────────┐
│ STEP TIMELINE    │ DETAIL                         │ CONTEXT & COST   │
│                  │                                │                  │
│ ✓ Provision   4s │  $ npm test                    │ Loaded context   │
│ ✓ Read files 12s │  ─────────────────────────     │ ▸ Conventions    │
│   ▸ Read 23 ▸    │  ✓ 142 passing                 │    always        │
│ ✓ Plan        8s │  ✕ 2 failing                   │ ▸ API runbook    │
│ ● Edit×6     31s │    charge.test.ts:88            │    paths infra/  │
│ ○ Test           │                                │ ▸ CLAUDE.md      │
│ ○ Open PR        │  [expand raw output]           │    repo file     │
│                  │                                │ ──────────────   │
│ ─────────────    │                                │ Tokens   84.2k   │
│ Summary│Normal│  │                                │  in 71k out 13k  │
│ Verbose          │                                │ Cost      $1.42  │
├──────────────────┴────────────────────────────────┴──────────────────┤
│ Step 4/9 — editing src/api/charge.ts                    ● running    │  current-step line
├───────────────────────────────────────────────────────────────────────┤
│ [ Send a message to this run…                    ] [Stop] [Take over] │  steering
└───────────────────────────────────────────────────────────────────────┘
```

Rules for this screen:

- **Group similar tool calls.** Twenty-three consecutive reads collapse to one row: `Read 23 files ▸`. Highest-value log affordance available.
- **Tool-aware rendering.** An edit is a diff hunk. A bash call is a `$` line with collapsible output and exit code. A read is one line. Raw JSON is never a default rendering.
- **Verbosity switch** — Summary / Normal / Verbose, live, no restart. Default follows concurrency: with four or more runs in flight the default drops to Summary.
- **Failed steps auto-expand.** `f` jumps to the next failure.
- **Current-step line** is a mutable single sentence the run writes itself, in the shape *action + specific item*: "editing src/api/charge.ts", not "processing". Visible while running, replaced by the outcome summary when done.
- **Timing gutter** on every step, right-aligned. Duration bars split into three segments — queued / model / tool — so "why was this slow" is answerable at a glance.
- **Cost on every step, rolled up to parents.** Hover a cost for the input / output / reasoning / cache-read split.
- **Approval rows render inline in the timeline** with tick and cross, not as a modal. Four responses where the agent supports them: Approve · Approve with edits · Respond · Deny. "Always allow" writes a concrete, scoped, inspectable rule shown in the agent's settings — never a global mute.
- **Steering queues.** A message sent to a running agent applies after the current tool call completes. The composer says so: *"Applied after the current step."*
- **Take over** stops the run and shows a copy-paste command that checks the branch out locally, plus a note field: *"tell the agent what you changed before resuming."*
- **Permalinks.** Selection state lives in the URL; sharing is copying the URL.

### 5.8 Agents — `/p/:key/agents`

**Roster** as cards: avatar/color, name, role line, model, autonomy level, runs this week, success rate, spend. An enable toggle.

**Agent detail** in sections:

1. **Identity** — name, color, git author name and email, GitHub identity. A line explaining why this matters: *"Events caused by this identity won't re-trigger this agent."*
2. **Directive** — the system prompt. Monospace markdown editor, version history with a diff view, and a live token count.
3. **Model & effort** — model picker, thinking effort, max wall-clock, max steps.
4. **Permissions** — checkboxes with a lock icon, visibly distinct from the directive textarea: read files · edit files · run commands · network (none / allow-list / open) · push branches · open PRs · comment on PRs · submit reviews · create wiki pages. A reviewer with edit unchecked *cannot* write code; that is stronger than telling it not to.
5. **Autonomy** — a four-stop dial: `Suggest` (plans only, never acts) · `Approve each action` · `Auto with gates` (plan gate + destructive-action gate) · `Auto`. The current level is echoed on every run header. Dangerous rungs sit behind a confirmation, and the dial is ordered by increasing risk.
6. **Limits** — max concurrent runs, daily spend cap.
7. **Context preview** — *"what every run of this agent sees"*: the resolved stack of project guidance + always-scoped wiki pages + repo instruction files, with a total token count. This is the single most requested and least-shipped feature in the category.

### 5.9 Triggers — `/p/:key/triggers`

**List.** Each rule is a card that reads as prose:

```
┌───────────────────────────────────────────────────────────────┐
│ ⚡ Review new PRs                              [●━━] enabled  │
│ WHEN pull request opened, ready_for_review · branch main      │
│ IF   author is an agent · files changed < 400                 │
│ THEN run agent Reviewer                                       │
│ ───────────────────────────────────────────────────────────── │
│ Fired 18×  ▪▪▪▪▪▪▪▪▪▪▪▪▪▫▫▪  14 ok · 3 no action · 1 loop     │
│ Ignores events caused by @reviewer-bot            last: 2h ago│
└───────────────────────────────────────────────────────────────┘
```

The outcome sparkline colored by class, inline on the card, is what turns "is this rule working" into a glance instead of an investigation.

**Editor** — three labeled sections, literally headed WHEN / IF / THEN.

- **WHEN**: event picker → activity types (multi-select chips) → filters (branch, path glob, label). Three-level narrowing. `opened` vs `synchronize` are visually distinct and the helper text says why.
- **IF**: `field | operator | value` rows. Operators are type-prefixed in the dropdown (`(text) contains`, `(number) greater than`) which teaches type compatibility without a type system UI. `+ And` is an inline link; `Add Or group` is a separate, heavier button, because OR is rarer and more error-prone and should feel that way.
- **THEN**: action picker. For "run agent": agent, optional prompt override with `{{...}}` interpolation, and where the output goes.

**Loop protection panel**, always visible in the editor, defaults on, each layer a row with a toggle and a plain-language description:

| Layer | Default | Reads as |
|---|---|---|
| Actor suppression | on | *"Ignore events caused by this agent's own identity"* |
| Debounce | 90s, keyed on PR | *"Collapse a burst of pushes into one run"* |
| Cancel in progress | on | *"A new push supersedes the running review"* |
| Depth limit | 3 | *"Stop after 3 agent-caused re-triggers on the same PR"* |
| Budget ceiling | inherits project | *"Stop when this rule has spent $X today"* |

**Backtest.** A button: *"This rule would have fired 7 times in the last 7 days"*, listing the actual historical events with what each would have done. Nobody in the category offers this and it is the strongest dry-run available for an event-driven product.

**Loop chain view.** When a rule trips the depth limit, the run detail and the trigger's history render the causal chain vertically with the repeating element highlighted:

```
run #481 ─▶ pushed to PR #219 ─▶ run #487 ─▶ pushed ─▶ run #492 ─▶ ⊗ stopped
```

That is the difference between "your automation was throttled" and "here is the cycle you built."

### 5.10 Inbox — `/inbox`

The cross-project version of the needs-you lane. One row per blocked run, **updated in place, never stacked** — this is the single most important decision for notification UX at four or more concurrent agents. Grouped by project. Approvals sort to the top always. Each row carries the flavor in words and an inline action. `J`/`K`/`Enter`/`A` to approve, `X` to dismiss.

### 5.11 Project settings — `/p/:key/settings`

Left rail of sections, one scrollable pane each, autosave with an inline saved indicator. No global Save button.

`General` · `Board` (columns + categories + WIP + auto-start) · `Wiki` (default scope, verification period, repo sync path, context threshold) · `Repository` (repo, branch, naming template, setup script, secrets, network policy) · `Agents` (project-wide guidance, defaults) · `Triggers` (link + audit log) · `Members & access` · `Notifications` · `Danger zone` (typed confirmation).

Every project-level setting that has a workspace default renders as the control plus a line: *"Inherited from workspace: `main`. Override."* Once overridden, offer *"Reset to workspace default."* This one pattern eliminates the largest category of settings confusion in tools of this shape.

---

## 6. Keyboard map

Two palettes, deliberately split: `⌘K` is deterministic actions, `⌘J` is talking to an agent.

```
⌘K    command palette            ⌘J    ask an agent
/     search                     ?     shortcut cheatsheet
⌘\    collapse left rail         ⌘I    properties sidebar

G then …  B board · W wiki · R runs · A agents · T triage · G triggers · S settings · H home · I inbox

C     new ticket                 ⌘⇧O   new sub-ticket / convert selection
J K   move in list               Space peek without opening      Enter open
X     select                     Esc   back

S     status        P     priority      A     assign (human)
D     delegate (agent)            L     labels        E     edit        R     rename

⌘B    board / list toggle        ⇧V    display options
1 2 3 H   triage: accept · duplicate · decline · snooze
f     next failure (in a run log)
```

`D` for delegate sitting beside `A` for assign is the keyboard-level expression of the two-field model. It signals the product's thesis every time someone presses it.

Two supporting rules: single letters are for mutation, prefixed chords (`G`, `O`) for navigation; and every context menu displays its own shortcut, so users learn the grammar from the mouse UI.

---

## 7. Components

| Component | Used in | Notes |
|---|---|---|
| `StatusDot` | everywhere | one component, the §4 vocabulary, dot + label + color |
| `NeedsYouCard` | home, board lane, inbox | states the flavor in words, carries one inline action |
| `RunSessionCard` | ticket stream, runs list, overview | collapsed by default; expands to the full activity inline. Same component, three placements |
| `ActivityStream` | ticket detail, run detail | typed entries: thought · action · elicitation · response · error · comment · system |
| `ToolCallRow` | activity stream | tool-aware rendering; groups consecutive same-tool calls; retry badge with attempt number on the row |
| `DiffView` | run detail, PR review, wiki proposals | unified; inline comments on lines become the next message to the agent |
| `RuleRow` | trigger editor | `field / operator / value` |
| `ScopeBadge` | wiki tree, page header, context panel | the five `agent_scope` values |
| `ContextMeter` | wiki, agent detail, run detail | pages + words + tokens, with a threshold |
| `CostChip` | run rows, steps, project header | hover for the input/output/reasoning/cache split |
| `Editor` | ticket description, comments, wiki, directives | one markdown editor, reused everywhere. Slash commands, `@` mentions |
| `EmptyState` | every list | headline · two-sentence body · one primary CTA · optional secondary |

**On the activity stream schema:** five activity types — `thought`, `action`, `elicitation`, `response`, `error` — with run state *derived* from the last emitted activity rather than set independently. An `elicitation` is what turns a run into `needs input`; it is how an agent asks a question without failing, and it is the mechanism that makes the whole needs-you surface work. Adopt an acknowledgment SLA too: a run must emit its first `thought` within 10 seconds or the UI shows a stall warning.

---

## 8. Empty states

One primary CTA each. Everything else dimmed. Never a blank canvas with a toolbar.

| Surface | Headline | Body | Primary | Secondary |
|---|---|---|---|---|
| No projects | Nothing here yet | A project connects a repo, a board, and a roster of agents. | **Create project** | — |
| New project | Connect a repository to get started | We'll import your issues, docs, and agent instructions automatically. | **Connect GitHub repo** | Start from a template |
| Board | No tickets yet | Import from GitHub Issues, or write one — press `C`. | **Import 12 open issues** | New ticket |
| Wiki | Your project has no docs yet | Docs here steer your agents, not just your teammates. | **Import AGENTS.md** *(detected)* | New page |
| Agents | No agents yet | An agent is a name, a prompt, and a set of permissions. | **Add an agent** | Use a starter roster |
| Runs | No runs yet | Delegate a ticket to an agent and its run appears here. | **Go to board** | — |
| Runs (filtered) | No runs match these filters | *with removable filter chips* | **Clear filters** | — |
| Triggers | No triggers yet | Start an agent automatically when something happens in the repo. | **Add trigger** *(3 suggested, pre-filled, off)* | — |
| Triage | Nothing to triage | Tickets created by triggers and agents land here first. | — | — |

**Two moments deserve special treatment.** The first completed run is the true activation event — mark it explicitly and immediately teach the next action (review the diff, or turn the feedback into a wiki page). The first `needs input` state is where users learn agents are interactive rather than fire-and-forget — give it an unmissable treatment and route the notification to the delegating human specifically.

**Onboarding strategy:** make repo-link the single gate, then derive everything from it. Import issues (with a preview and checkboxes, never silently), seed wiki pages from detected instruction files with correct scopes, propose two pre-filled triggers with the toggles off, suggest agents matching the detected stack, and generate the Overview from the README. The empty state becomes a *loading* state, which is a far better experience than six simultaneous "create your first X" prompts.

---

## 9. Interaction rules

Non-negotiables, phrased as rules so they're testable.

1. **Never render a generic "waiting" badge.** Every blocked state names its flavor in words.
2. **Never start an agent as a side effect of a drag.** Only `D`, the Run button, or a trigger.
3. **Never stack notifications for the same run.** One row, updated in place.
4. **Never render a raw JSON tool call as the default.** Tool-aware renderers, always.
5. **Never let a failed run leave nothing behind.** Push the branch with partial work and say so.
6. **Never let an automation reference a board column by name.** Always by category.
7. **Never show a blank spinner during provisioning.** Show the checklist — cloning, installing, starting — and accept queued input during it.
8. **Never let "always allow" be a global mute.** It writes a concrete, scoped, inspectable rule visible in the agent's settings.
9. **Never mix guidance and enforcement in one visual treatment.** Textareas steer. Checkboxes with a lock constrain.
10. **Never auto-write a wiki page.** Agents propose; humans accept.
11. **Approval interrupts inline when watched, escalates to the inbox when not.** Both, not one.
12. **Selection state lives in the URL.** Sharing a run, a log line, or a filtered view is copying the URL.

---

## 10. Responsive and accessibility

- Breakpoints: ≥1400 full three-pane; 1100–1400 right pane collapses to a toggle; <1100 left rail collapses to icons, run detail stacks vertically. The product is not designed for phones in V1, but the inbox and the approve/answer actions must work on one — that is the surface people will want on their phone.
- Every wide element (log output, diffs, tables) scrolls inside its own `overflow-x: auto` container. The page body never scrolls horizontally.
- Color is never the only carrier of status: every status has a distinct glyph (`● ▲ ✓ ✕ ⊘ ⊗ ○ ◐`) alongside its color.
- Focus rings on everything interactive, `--border-strong`, never removed.
- Live regions announce state changes on runs the user is watching; the log stream does not spam the announcer — only step boundaries and state transitions.
- Contrast: body text ≥ 4.5:1 against its surface in both themes; status colors ≥ 3:1 for the glyph.

---

## 11. What the prototype demonstrates

The accompanying `prototype.html` is a self-contained clickable walkthrough of every screen above with realistic fake data. It is a design artifact, not a scaffold — no backend, no state persistence beyond the session, no real diffs.

Specifically worth clicking through, because these are the parts that are hard to imagine from a document:

- The needs-you lane sitting above the board columns, and the same content in the inbox.
- A run detail with a live-ish log, grouped tool calls, the verbosity switch, and an inline approval row.
- The context panel showing exactly which wiki pages loaded and why.
- The trigger editor with WHEN/IF/THEN, the loop protection panel, and the backtest result.
- The loop chain visualization on a stopped run.
- The wiki tree with scope badges and the context budget meter.

---

## 12. Open questions for the next pass

1. **Does a run belong to a ticket, or can it be free-floating?** The spec assumes free-floating runs exist (a trigger fires without a ticket) but the ticket stream is the primary home. That needs resolving before the data model is written.
2. **What happens to a run when its ticket is closed or deleted?** Orphan, cancel, or block the deletion.
3. **Multi-agent on one ticket** — sequential attempts are covered, but two agents working the same ticket simultaneously in separate containers is not modeled. Probably a V1.1 "attempts" feature: N attempts per ticket, each with its own agent/model/base branch, compared side by side.
4. **How much of the wiki should sync to the repo?** Bidirectional sync of `always`-scoped pages out to `AGENTS.md` is attractive and is also a merge-conflict factory. Possibly one-way export only in V1.
5. **Where does human code review live?** The spec assumes GitHub is the review surface and the product links to it. An in-app diff review that posts to GitHub is a larger build and a real fork in the road.
