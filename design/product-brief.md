# Product Brief — Lexicode

*Working name taken from the repo folder. Swap it freely; nothing in this document depends on it.*

**Version:** 0.1 · Design pass 1 · August 2026
**Status:** Pre-architecture. This document and the accompanying UI/UX spec define *what* gets built and *what it looks like*, not *how* it's built.

---

## 1. What it is

A single self-hosted process that acts as an agent orchestrator and serves its own web dashboard. A small team creates a project, points it at a GitHub repo, writes tickets and docs, and defines a roster of agents with role prompts. Agents pick up tickets, work them in disposable Docker containers running Claude Code, and hand back a pull request or a wiki page. Event triggers wire the outputs of one agent into the input of the next, so a ticket can travel from "written" to "reviewed PR ready to merge" without a human touching a keyboard — while every step stays visible and interruptible.

The thesis in one line: **the scarce resource is not agent capability, it is human attention.** Every design decision below is downstream of that.

---

## 2. Who it's for

A small self-hosted engineering team — call it 2 to 8 people — that already trusts coding agents for scoped work and wants to stop babysitting them one terminal at a time. They run their own box. They want their work on their infrastructure, their repos, their spend.

They are *not* trying to remove humans from the loop. They are trying to move the human from "operator" to "reviewer and unblocker."

**Explicit non-users for V1:** enterprises needing SSO/SAML/audit compliance, teams without a GitHub repo, non-technical stakeholders as primary users, anyone wanting to orchestrate agents across many repos at once.

---

## 3. The core loop

```
   ┌─────────────────────────────────────────────────────────────────┐
   │                                                                 │
   │   Ticket written  ──delegate──▶  Agent run                       │
   │   (human or agent)               (container + Claude Code)      │
   │        │                              │                         │
   │        │                              ├──▶ PR opened            │
   │        │                              ├──▶ Wiki page proposed   │
   │        │                              └──▶ Question / blocked   │
   │        │                                       │                │
   │        │                                       ▼                │
   │        │                              ┌─────────────────┐       │
   │        │                              │   NEEDS YOU     │       │
   │        │                              │ (inbox + lane)  │       │
   │        │                              └─────────────────┘       │
   │        │                                                        │
   │        ▼                                                        │
   │   Trigger fires ◀────── GitHub event (PR opened, review          │
   │        │                submitted, CI failed, comment added)     │
   │        │                                                        │
   │        └──▶ spawns the next agent run ───────────┐              │
   │                                                   │              │
   └───────────────────────────────────────────────────┘              │
                                                                      │
                        Human merges. Always. ◀────────────────────────┘
```

The canonical chain the product must make trivial to set up:

1. Human writes a ticket with acceptance criteria, delegates it to **Dev**.
2. Dev runs in a container, opens a PR, moves the ticket to In Review.
3. Trigger: *PR opened by an agent* → spawn **Reviewer** with a review prompt.
4. Reviewer posts a review with severity-tagged findings.
5. Trigger: *review submitted with changes requested* → spawn **Dev** again to address comments on the same branch.
6. Trigger: *CI failed* → spawn **Dev** to fix.
7. Loop guard stops the cycle at depth 3 and surfaces it as an actionable state, not a mystery.
8. Human reviews and merges.

If a team can set that up in ten minutes and then walk away for two hours and come back to something reviewable, the product works.

---

## 4. Where it sits in the market

Four archetypes exist today, and all four leave the same hole.

| Archetype | Examples | What they solve | What they leave open |
|---|---|---|---|
| Agent-native cloud IDE | Devin, OpenHands, Cursor cloud | One agent, watched closely, rich workspace | No project model. Every session starts from zero context. |
| Task queue / inbox | Codex cloud, Jules, Claude Code on the web | Async parallel runs → diffs | No board, no docs, no chaining. Each run is an island. |
| In-situ agent | Copilot coding agent, CodeRabbit, Charlie | Zero new UI, lives in the PR | Nowhere to see the whole pipeline. Config lives in YAML nobody reads. |
| Mission control | GitHub Agent HQ, Factory Missions | Many agents, one pane | Hosted, vendor-coupled, opaque, expensive. |

Nobody ships the boring middle: **a self-hosted project workspace where the tickets, the docs, the agents, the triggers, and the run logs are all one thing.** Backstage did this for services and humans; nothing has done it for agents.

Three things would make this distinctive rather than derivative — none of them are hard, and none of them are currently shipped well by anyone:

1. **A context budget meter and a per-run context panel.** File-based agent instruction systems (`CLAUDE.md`, `.cursor/rules`, `AGENTS.md`) make agent context invisible. This product has a UI. Showing exactly which wiki pages steered a run, why each one loaded, and what it cost in tokens is the clearest unmet need in the whole category — and it is the only credible answer to "why did the agent ignore my instructions."
2. **Wiki pages as scoped, owned, expiring agent instructions.** A doc that steers machines and has been stale for six months is not a mild annoyance, it is a live defect. Per-page owner + `verified_until` + automatic demotion on expiry makes staleness a tracked property rather than folklore.
3. **Loop protection you can see.** Every automation product on the market either bans cascades entirely or discovers loops through the billing statement. Rendering the actual causal chain — *run #1 → PR update → run #2 → PR update → run #3 → stopped* — turns "your automation was throttled" into "here is the cycle you built."

---

## 5. The seven decisions everything else hangs off

These are the load-bearing calls. Change one and the rest of the design moves.

**D1 — Assignee and delegate are two fields, never one.**
A ticket has a human assignee (accountable) and optionally an agent delegate (doing the work). Notifications for a run route to the delegating human specifically. This solves accountability, notification routing, and "who do I ask about this" in one move, and it makes the product's thesis legible every time someone presses `D`.

**D2 — Board columns are customizable, but every column carries a fixed category.**
`backlog · ready · running · review · done · canceled`. Users rename and reorder columns freely. Triggers, automations, and progress rollups key off the **category**, never the name. This is the single most important schema decision in the board — it is what stops a rename from silently breaking every trigger.

**D3 — Dragging a card never starts an agent.**
Starting a run is always an explicit act: the Run button, `D` to delegate, or a trigger firing. A column can opt into `auto-start delegate on entry`, off by default, and when it's on the column header says so. Surprise agent spend is the fastest way to lose a user's trust.

**D4 — "Needs you" is a first-class state, and "waiting" is never one state.**
Four distinct reasons a run stops for a human: *needs input* (the agent asked a question), *plan approval* (a gate you configured), *review ready* (output waiting on you), *failed*. Each gets its own color, its own filter, and its own place in the pinned lane and the global inbox. Collapsing these into one "waiting" badge is the most common failure in this category.

**D5 — Loop protection is on by default, layered, and visible.**
Five layers, each with its own run status and color: actor suppression (events caused by an agent's own git identity don't re-trigger that agent) → debounce per PR → cancel-in-progress → depth counter (default 3) → per-project budget ceiling. Plus a `skip-agents` escape token users can put in a PR body. Nothing here is optional in V1; a "PR opened → review → address → push → PR updated" chain runs forever without it.

**D6 — The human merges. Always.**
An agent may open a PR, push to a branch, comment, and review. It may not merge, may not force-push a protected branch, and may not approve its own PR in a way that counts. There is no setting to turn this off in V1.

**D7 — Guidance and enforcement look different in the UI.**
A prompt or a wiki page *steers* an agent; it does not *constrain* it. Tool permissions and the container's network policy *constrain*. These get visibly different treatments — free-text areas versus checkboxes with a lock icon — because users who confuse the two get badly surprised exactly once and then stop trusting the product.

---

## 6. V1 scope

### 6.1 Projects

A project is the only container. It owns everything. One project, one repo, one board, one wiki, one agent roster.

Name, description, icon color, key (drives ticket IDs like `PAY-14`). Overview page with an About card: owner, linked repo + branch + last commit, agent count, open ticket count, last run, plus recent activity and pinned wiki pages.

### 6.2 Agents

A roster of named agents. Each has:

- **Name and role** — Dev, Reviewer, Docs, Architect. Freeform.
- **Directive** — the system prompt. Markdown, versioned, with a diff view on change.
- **Model and effort** — which model, how hard it thinks.
- **Tool permissions** — what it may do. A reviewer that structurally *cannot* write files is a better reviewer than one told not to. Read/write/bash/network/push/comment as explicit grants.
- **Autonomy level** — `suggest` (plan only, never acts) / `approve each step` / `auto with gates` / `auto`. Visible at all times on the run, never buried in settings.
- **Git identity** — a distinct commit author and GitHub identity per agent, which is what makes actor suppression (D5) and blame work.
- **Concurrency cap** — max simultaneous runs for this agent.

### 6.3 Repository

One GitHub repo per project. Default branch, branch naming template (`{agent}/{ticket-key}-{slug}`), PR conventions, an optional setup script the container runs before the agent starts, and a secrets store for env vars the container needs.

On connect, the project bootstraps itself: import open issues as tickets (with a preview and checkboxes, not silently), detect `AGENTS.md` / `CLAUDE.md` / `.cursor/rules/` / `README.md` / `docs/` and offer to seed wiki pages with the right scope, detect CI config and propose two pre-filled triggers with the toggles off, and generate an Overview from the README. **The empty state becomes a loading state.** This is the single highest-leverage onboarding decision available.

### 6.4 Board

Kanban and list are the same view with a layout toggle and a `group_by` picker — a board is `group_by` rendered horizontally, not a separate data structure. Custom columns with categories (D2). Display properties control which badges appear on a card.

One pinned lane above everything: **Needs you**. It is auto-populated and cannot be reordered or removed. It is the only swimlane in V1.

A ticket has: key, title, markdown description, **acceptance criteria as a checklist**, labels, priority, assignee, delegate, linked PR, and one level of sub-tickets. Select text in a description → convert to sub-tickets is the highest-leverage agent↔human handoff in the product: the agent writes a plan as a checklist, the human one-shots it into work.

The ticket detail is a **single unified stream** — human comments, status changes, agent thoughts, tool calls, and run cards all interleaved chronologically. No Comments tab beside an Activity tab.

WIP limits exist but are reinterpreted: on the `running` column the limit is an **enforcing concurrency governor**, not a social contract. It is cost control, rate-limit protection, and merge-conflict avoidance in one number.

**Triage** is a separate intake queue. Everything created by a trigger or by an agent lands there, not on the board. Four actions on number keys: accept, mark duplicate, decline, snooze. This is the pressure valve that makes automatic ticket creation safe.

### 6.5 Wiki

Markdown pages, two levels of nesting maximum, ordered by front-matter, drag to reorder. Full-text search is the primary navigation, the tree is secondary. `@`-mentions between pages and tickets, with a backlinks section showing the full containing paragraph. Flat tags with a tag index as the cross-cutting escape hatch.

Every page carries agent-facing front-matter:

```yaml
title: Deployment runbook
parent: engineering
owner: @dana
verified_until: 2026-11-01
agent_scope: paths        # always | auto | paths | manual | never
paths: ["infra/**"]
```

`agent_scope` renders as a badge in the tree and on the page header, so you can see at a glance which pages are steering agents. The Agents tab carries a live **context budget meter**: *"Always-on context: 3 pages, 1,840 words, ~2.4k tokens."* An `always` page whose verification expires gets demoted to `auto` and flagged rather than silently steering every run with stale instructions.

After a run, an agent may **propose** a wiki page or an edit, rendered as a diff the human edits, accepts, or dismisses. Never auto-written. This is the only credible answer to why wikis die.

### 6.6 Triggers

Rule rows, not a node canvas. The literal words **WHEN / IF / THEN** as section headers.

Three-level narrowing on the event, borrowed wholesale from GitHub Actions because it is the best-designed vocabulary available: `event → activity types → filters`. `pull_request` with `[opened, synchronize, ready_for_review]` on `branches: [main]` reads as three chips. Distinguishing `opened` from `synchronize` is not pedantry — that distinction is exactly where the runaway loop lives.

V1 event catalogue, deliberately short:

| Event | Activity types |
|---|---|
| `pull_request` | opened · synchronize · ready_for_review · closed |
| `pull_request_review` | submitted (approved / changes_requested / commented) |
| `pull_request_review_comment` | created |
| `check_suite` | completed (success / failure) |
| `issue_comment` | created |
| `ticket` | created · moved to column · delegated |
| `run` | completed · failed · needs_input |
| `schedule` | cron |

Conditions as `field | operator | value` rows with type-prefixed operators, `+ And` inline and `Add Or group` as a visibly heavier separate action. Interpolation-only templating (`{{pr.author}}`, `{{ticket.key}}`) — never control flow inside a string.

Actions in V1: run an agent (with an optional prompt override), create a ticket into triage, move a ticket, post a comment, send a notification.

Two things nobody in this space ships, both cheap and both differentiators:

- **Backtest.** *"This rule would have fired 7 times in the last 7 days"*, listing the actual events. The strongest dry-run available for an event-driven product.
- **Rule health inline on the card.** Fired count with an outcome-class breakdown. And the outcome vocabulary is richer than success/failure: `succeeded · no action (conditions not met) · awaiting approval · errored · debounced · superseded · loop stopped · budget exceeded`. Almost every support burden in this category comes from collapsing "did nothing" into "failed."

### 6.7 Runs

Every agent invocation is a run. It gets a container, a copy of the repo on its own branch, a status, a log, a cost, and a set of outputs.

**Run states:** `queued · provisioning · running · needs input · awaiting approval · completed · failed · timed out · canceled · loop stopped`.

**Run list:** filterable by status, agent, ticket, trigger. Default saved filter is "needs attention." Cost and duration are columns, not buried in a detail panel — put the billing unit where it is scannable.

**Run detail** is three panes: step timeline on the left, detail in the center, context and cost on the right.

- Similar tool calls collapse: *"Read 23 files ▸"*. This is the highest-value single log affordance available.
- Tool-aware rendering. An edit renders as a diff hunk, a bash call as a `$` line with collapsible output, a read as one line. Raw JSON is never the right default.
- Verbosity switch — **Summary / Normal / Verbose** — live-switchable. This is a fleet-management control, not a debugging control: as concurrency rises the default moves toward Summary.
- A mutable single-line current-step string the run writes itself: *"Step 4/9 — running the test suite."* Visible while running, gone when done.
- Failed steps auto-expand. `f` cycles to the next failure. Log lines are permalinkable. Selection state lives in the URL so sharing is copying the URL.
- Cost and tokens on every step, rolled up to the parent, with input/output/reasoning/cache split on hover.
- **Context panel** listing exactly which wiki pages and instruction files were loaded and why (`always` / matched path `infra/deploy.ts` / retrieved for "deployment"). This is the debugging tool for "why did the agent ignore my instructions."

**Intervention:** a steering box below the log that queues a message applied after the current tool call completes — queue, don't interrupt. Stop, which preserves anything already pushed. Take over, which stops the run and hands you the branch with a one-line command to check it out locally. And approval rows that appear inline in the transcript with tick/cross when you're watching, and escalate to the inbox when you're not.

**Failure still produces an artifact.** A failed run pushes its branch with whatever partial work exists and says so. A run that fails and leaves nothing behind is a run that wasted an hour twice.

### 6.8 Cross-project surfaces

- **Home** — project list plus a "Needs you" strip aggregating awaiting-input and failed runs across all projects. For a team of five running a dozen agents, this strip is the app.
- **Notifications** — the delegating human gets pinged on needs-input and failed. One notification per run, updating in place, never stacking. Approvals and errors push; everything else silently updates a badge.
- **Audit log** — who did what, which agent acted, which trigger fired, what it changed.

---

## 7. Gaps in the original outline that V1 has to close

Listed because they were not in the brief and the product does not work without them.

| Gap | Why it bites | V1 answer |
|---|---|---|
| **Agent git identity** | Actor suppression, blame, and "who wrote this line" all depend on agents being distinct actors | Per-agent commit author and token; identity shown on every commit and run |
| **Container secrets and setup** | An agent that can't install dependencies or reach the package registry can't do anything | Per-project setup script + secrets store + explicit network policy (none / allow-list / open) |
| **Cost governance** | Twelve containers running Claude Code will spend real money with no natural stopping point | Per-project daily budget ceiling, per-agent concurrency cap, enforced WIP limit on the running column, cost visible on every run row |
| **Acceptance criteria** | Without a definition of done, an agent's output isn't checkable and the review burden goes up, not down | First-class checklist on every ticket, injected into the run prompt, checked off in the run summary |
| **The review bottleneck** | Measured across 33k agent PRs: 28% get merged within one minute of creation, i.e. unreviewed | Human merge gate (D6), diff size warnings, and a review agent whose findings are severity-tagged and deduplicated before posting |
| **Triage** | Triggers and agents creating tickets straight onto the board produces a firehose in a week | All automated ticket creation lands in triage |
| **Takeover** | Watching an agent go down a wrong path for an hour with no cheap way to intervene is the #1 trust complaint about this whole category | Stop → take over → resume with a change note, as an explicit ritual with a copy-paste command |
| **Notification discipline** | Four concurrent agents produce a notification avalanche within a day | One row per run, updated in place; tiered by event class |

---

## 8. Explicitly out of scope for V1

Cut with confidence: sprints and cycles, story points and velocity, Gantt and timeline views, query-defined swimlanes, sub-grouping, custom ticket fields, multiple boards per project, multiple repos per project, wiki graph view, page-level permissions, unlimited wiki nesting, per-page comments, a visual node-graph automation builder, SSO/SAML, a mobile app, non-GitHub forges, and multi-tenant hosting.

Deferred but likely: multi-repo projects, cross-project saved views, insights and charts, typed custom fields, an initiative layer above projects, GitLab support.

**The node-graph automation builder deserves a specific note.** It is the most tempting thing on this list and the most expensive. GitHub's judgment — roughly six canned automations with a toggle and a field or two, and a real programming environment as the escape hatch — covers the overwhelming majority of need. Zapier, the market leader in this exact space, deliberately did not build a free canvas either. Rule rows plus a webhook escape hatch. Not a canvas.

---

## 9. Risks

**The runaway loop.** The mitigation is D5 and it must ship in V1, not V1.1. A user whose first week produces a $400 bill from a review ping-pong does not come back.

**The review bottleneck moves, it doesn't disappear.** Making agents productive means producing more code than anyone can read. The product's honest position is that it is a sensor array, not a verdict machine: it organizes work for human review, it does not replace the review. Marketing it as autonomous engineering produces durable negative brand value that outlives the product improvements — this is empirically observable in the category already.

**Unpredictability, not incapability, is the trust killer.** Teams can live with an agent that fails; they cannot live with one where they can't tell in advance which tasks will succeed. The counter is verifiability: acceptance criteria on tickets, failure artifacts, the context panel, cost visibility. Make the work checkable and the unpredictability becomes tolerable.

**Container cold start.** Thirty to ninety seconds of blank spinner per run is a real product problem at fleet scale. Cache the dependency layer into the image and show a live provisioning checklist that accepts queued input — dead time becomes legible progress.

**Self-hosting is the moat and the tax.** The team owns Docker, disk, and API spend. Docs and defaults have to be excellent or the first hour kills adoption.

---

## 10. What success looks like at the end of V1

- A new project goes from "connect repo" to "first agent run in flight" in under five minutes, without reading docs.
- A team sets up the six-step dev→review→address chain from §3 in under ten minutes.
- A human can walk away for two hours and come back to a single screen that tells them exactly what needs them and why, with no scrolling through logs.
- No user is ever surprised by an agent run they did not expect, or by a bill they did not expect.
- The wiki has pages in it that nobody sat down to write.
