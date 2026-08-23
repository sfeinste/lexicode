# Architecture

## 1. Shape of the system

```
                         ┌──────────────────────────── lexicode (one process) ────────────────────────────┐
                         │                                                                                │
  browser ──HTTP/SSE────▶│  api/v1 ──▶ services ──▶ ┌────────────────── KERNEL ──────────────────┐        │
                         │                          │  store(SQLite) · bus · scheduler · guard   │        │
                         │                          │  auth · audit · secrets · registry · ports │        │
                         │                          └───┬──────────┬──────────┬──────────┬───────┘        │
                         │                              │          │          │          │                │
                         │                        EventSource   Forge     Sandbox   AgentRuntime          │
                         │                              │          │          │          │                │
                         │                        ┌─────▼───┐ ┌────▼───┐ ┌────▼────┐ ┌───▼────────┐       │
                         │                        │ github  │ │ github │ │ docker  │ │ claudecode │       │
                         │                        │  poll   │ │  api   │ │         │ │            │       │
                         │                        └─────┬───┘ └────┬───┘ └────┬────┘ └───┬────────┘       │
                         │  mcp server (host) ◀─────────┼──────────┼──────────┼──────────┘                │
                         └────────────────────────────  │          │          │  ───────────────────────  ┘
                                                        │          │          │
                                                   api.github.com  │     Docker daemon
                                                                   │          │
                                                                   │     ┌────▼──────────────────────┐
                                                                   └────▶│ container: workspace +    │
                                                                         │ claude -p --stream-json   │
                                                                         └───────────────────────────┘
```

One process, one database file, N containers. The browser talks only to `api/v1`. Containers talk
only to the Docker daemon's stream (stdio) and the host MCP endpoint.

---

## 2. What the kernel is

The kernel is the part that must not know about GitHub, Docker, Claude, or React. It owns exactly
seven things:

1. **Store** — the SQLite connection, migrations, transactions, and the repository interfaces.
2. **Bus** — persist-then-dispatch event distribution.
3. **Registry** — module lifecycle and the port registries.
4. **Scheduler** — the run queue, concurrency governance, and the run state machine (D-14).
5. **Guard** — the five-layer loop protection and budget enforcement.
6. **Identity** — users, sessions, memberships, and the audit log.
7. **HTTP surface** — the mux, middleware chain, and SSE hub that modules and services register into.

Everything else is either a **service** (domain logic: tickets, board, wiki, agents, triggers,
triage, notifications) or a **module** (an adapter implementing one or more ports).

The test for whether something belongs in the kernel: *if two different adapters would each need
their own copy of it, and they must agree, it is kernel.* Loop depth is kernel. GitHub's pagination
is not.

### 2.1 Dependency rule

```
module ──▶ kernel/ports ──▶ kernel/domain types
service ──▶ kernel                   (services may use ports through the registry)
api ──▶ service                      (api never touches a module directly)
kernel ──▶ nothing above it          (the kernel imports no module, ever)
```

`internal/kernel` must not import `internal/module/*`. Wiring happens exactly once, in
`cmd/lexicode/main.go`. A compile-time check (`go vet` custom rule or a simple import-graph test in
CI, story S02) enforces this.

---

## 3. Modules: registration and lifecycle

A module is a Go type implementing `Module`. All modules are compiled in and registered in one
place; "pluggable" here means *a new module is one file plus one line in `main.go`*, not dynamic
loading. Dynamic plugin loading is explicitly out of scope — it buys nothing for a self-hosted
binary and costs the type safety that makes the ports useful.

```go
type Module interface {
    Name() string
    Init(k *kernel.Kernel) error   // register ports, routes, subscriptions. No I/O.
    Start(ctx context.Context) error // begin background work. Must return promptly.
    Stop(ctx context.Context) error  // drain. Called in reverse registration order.
}
```

Lifecycle: `Init` all → migrate → `Start` all → serve → signal → `Stop` all in reverse with a
30s deadline. `Init` failing aborts boot with a named module in the error. `Start` failing puts the
module in a `degraded` state visible in workspace settings — a broken GitHub token must not prevent
the dashboard from loading.

### 3.1 The V1 module set

| Module | Ports implemented | Notes |
|---|---|---|
| `module/github` | `ForgeProvider`, `EventSource` | REST via `go-github`. Poller and API client share a rate-limit-aware transport. |
| `module/docker` | `Sandbox` | Docker SDK, embedded Dockerfile, egress proxy. |
| `module/claudecode` | `AgentRuntime` | stream-json parse, steering, permission wiring. |
| `module/actions` | `TriggerAction` ×5 | `run_agent`, `create_ticket`, `move_ticket`, `post_comment`, `notify`. |
| `module/context` | `ContextProvider` ×5 | `project`, `wiki`, `event`, `ticket`, `repofiles`. |
| `module/notify` | `Notifier` | in-app only in V1. |
| `module/testkit` | `Sandbox`, `AgentRuntime`, `EventSource` | fakes, built under a `testkit` tag; the reason the engine is testable without Docker. |

---

## 4. The ports

Full signatures live in [03-contracts.md](03-contracts.md). This is the map of *why each one exists*.

| Port | Extension it buys | V1 impls |
|---|---|---|
| `EventSource` | New trigger inputs: webhooks, GitLab, a filesystem watcher, a cron source | `github.poll`, `schedule.cron` |
| `ForgeProvider` | Another git host | `github` |
| `Sandbox` | Another execution substrate | `docker` |
| `AgentRuntime` | Another agent CLI/SDK | `claude-code` |
| `TriggerAction` | New THEN verbs | 5 actions |
| `ContextProvider` | New sources of agent context | 4 providers |
| `Notifier` | Slack, email, push | `inapp` |
| `CredentialSource` | API key vs OAuth vs vault | `oauth-token`, `env` |

Two rules that keep these honest:

- **A port's method set is the whole contract.** No type assertions to concrete module types
  outside `main.go`. If the scheduler needs to know a runtime supports steering, that is a
  `Capabilities()` field, not a `.(interface{ Steer() })`.
- **Ports return domain types, never adapter types.** The GitHub adapter converts `github.PullRequest`
  into `forge.PullRequest` at its boundary. Nothing above the port ever sees a vendor struct.

---

## 5. Package layout

```
cmd/lexicode/                 main.go — the only wiring site; also `serve`, `migrate`, `version`
internal/kernel/
  kernel.go                   the Kernel struct: store, bus, registry, scheduler, guard, audit
  module.go                   Module interface, registration, lifecycle runner
  registry.go                 port registries (typed maps, lookup by ID)
  store/                      sqlite open, migrate, tx, repositories
  bus/                        persist-then-dispatch, topics, subscriptions
  sched/                      run queue, admission control, state machine, reconciliation
  guard/                      the 5 loop layers + budget ledger
  auth/                       users, sessions, memberships, middleware
  audit/                      audit writer
  secrets/                    AES-GCM store
  httpx/                      mux, middleware, problem+json errors, SSE hub
  ports/                      the port interfaces + their domain types
internal/domain/              pure types shared everywhere: Run, Ticket, Activity, Event, ...
internal/service/
  projects/ agents/ board/ tickets/ triage/ wiki/ triggers/ runs/ notify/ bootstrap/ contextres/
internal/module/
  github/ docker/ claudecode/ actions/ context/ notify/ testkit/
internal/api/v1/              handlers, DTOs, SSE endpoints, openapi.yaml
migrations/                   NNNN_name.up.sql (embedded)
web/                          Vite + React app (built into web/dist, embedded)
docs/                         user-facing docs shipped with the binary
```

---

## 6. The event bus and causality

### 6.1 Shape

```go
type Event struct {
    ID          string            // ULID
    ProjectID   string
    Source      string            // "github.poll" | "internal" | "schedule.cron"
    Kind        string            // "pull_request" | "run" | "ticket" | "check_suite" | ...
    ActivityType string           // "opened" | "synchronize" | "submitted" | "completed" | ...
    Actor       Actor             // {kind: human|agent|external, id, login, email}
    Subject     Subject           // {kind: pr|ticket|run|repo, id, number, branch}
    Payload     json.RawMessage   // normalized, adapter-independent
    OccurredAt  time.Time
    DedupeKey   string            // unique per source; poller idempotency
    CauseRunID  *string           // set when this event was caused by an agent run
}
```

`Payload` is **normalized**, not raw GitHub JSON. The condition evaluator and the interpolation
templating both read from it, so its field names are user-visible (`pr.author`, `pr.files_changed`,
`review.state`, `check.conclusion`, `ticket.key`, `run.status`). Adapters map into this shape; the
shape is documented in [03-contracts.md](03-contracts.md) §4 and is a compatibility surface.

### 6.2 Persist then dispatch (D-13)

```
source ──▶ bus.Publish(evt)
              │
              ├─▶ INSERT INTO events (idempotent on dedupe_key)  ── if conflict: drop, return
              ├─▶ mark dispatch_state='pending'
              └─▶ fan out to subscribers (in-process, buffered, per-subscriber goroutine)
                      └─▶ on all-acked: dispatch_state='done'
```

On boot, any `pending` rows older than the process start are re-dispatched. Subscribers must
therefore be **idempotent** — the trigger engine's protection is the `trigger_firings` unique index
on `(trigger_id, event_id)`.

### 6.3 Causality — how the loop chain view is possible

Two foreign keys carry the entire causal graph:

- `runs.cause_event_id` — the event that spawned this run (null if manual)
- `events.cause_run_id` — the run whose action produced this event

Which yields, for free:

```
event(pr.opened) ──▶ run #481 ──▶ event(pr.synchronize, cause_run=481) ──▶ run #487 ──▶ ... ──▶ ⊗
```

`events.cause_run_id` is set by two mechanisms: internal events emitted by an action carry it
directly; external events get it by **actor attribution** — the poller resolves the marker
comment / commit author / branch name (D-9) to an agent, then to that agent's most recent run
touching that subject. Attribution that cannot resolve a specific run still resolves the *agent*,
which is enough for actor suppression; only the depth counter needs the run, and it falls back to
"any run by this agent on this subject" when the precise run is ambiguous.

---

## 7. Event ingestion: the GitHub poller

One goroutine per connected project, interval from settings (default 30s, floor 10s).

Each tick, for the project's repo:

| API call | Emits |
|---|---|
| `GET /pulls?state=all&sort=updated` | `pull_request` · `opened`, `synchronize`, `ready_for_review`, `closed` |
| `GET /pulls/comments?sort=updated` | `pull_request_review_comment` · `created` |
| `GET /pulls/{n}/reviews` for PRs touched since cursor | `pull_request_review` · `submitted` (+ `approved`/`changes_requested`/`commented`) |
| `GET /issues/comments?sort=updated` | `issue_comment` · `created` |
| `GET /commits/{sha}/check-suites` for head shas of open PRs | `check_suite` · `completed` (+ `success`/`failure`) |

**Cursoring.** A `poll_cursors` row per (project, resource) holds the last `updated_at` seen and an
etag. Requests use `If-None-Match`; a 304 costs no rate limit. Rate limit headers are respected with
exponential backoff and surfaced as module `degraded` state.

**Activity type derivation.** GitHub's list endpoints do not hand you activity types; the poller
derives them by diffing against `poll_pr_state` (per PR: head sha, state, draft, updated_at):

- unseen PR → `opened` (or `ready_for_review` if not draft and previously draft)
- head sha changed → `synchronize`
- draft→ready → `ready_for_review`
- state open→closed → `closed` (payload carries `merged`)

This diff is the reason `opened` and `synchronize` are distinguishable at all under polling, and
brief §6.6 is explicit that this distinction is where the runaway loop lives.

**Dedupe key.** `sha256(project|resource|id|discriminator)` where discriminator is the head sha for
`synchronize`, the review id for reviews, the comment id for comments, the check suite id + status
for checks.

**Cold start.** On first connect, the poller records current state as the baseline and emits
nothing. A repo with 40 open PRs must not fire 40 triggers on connect. The baseline pass is logged
and visible in the trigger's history as *"baseline — no events emitted."*

### 7.1 The cron source

`schedule.cron` is a second, trivial `EventSource` that emits `schedule` · `cron` events from the
trigger rows that use it. It exists in V1 because brief §6.6 lists `schedule` in the event
catalogue, and because it proves the port with a second implementation.

---

## 8. The trigger engine

A trigger is data, evaluated in four stages. Every stage can terminate with an outcome class, and
**every terminal outcome writes a `trigger_firings` row** — including the ones that do nothing.
"The rule fired but nothing happened" having a name is UI spec §4.2's whole point.

```
event ──▶ (1) MATCH ──▶ (2) CONDITIONS ──▶ (3) GUARD ──▶ (4) ACTIONS
              │              │                  │             │
         no match       no action         debounced /      succeeded
        (no firing      (firing row)      superseded /     errored
         row at all)                      loop stopped /   awaiting approval
                                          budget exceeded
```

**(1) Match** — `event.Kind == trigger.Event` AND `event.ActivityType ∈ trigger.ActivityTypes` AND
every filter passes (branch glob, path glob, label). A non-match writes nothing; it is not a firing.

**(2) Conditions** — a tree: `{all: [...]} | {any: [...]} | {field, op, value}`. Fields address the
normalized payload by dotted path. Operators are type-tagged (`text.contains`, `number.gt`,
`enum.is`, `bool.is`, `set.includes`, `glob.matches`) — the type prefix in the UI dropdown teaches
compatibility without a type system. Evaluation is total: an unknown path yields `nil`, and every
operator has defined `nil` behaviour (all false except `text.is_empty`). No user expression
language, no eval, no control flow.

**(3) Guard** — §9 below. Kernel-owned.

**(4) Actions** — executed in order, each a `TriggerAction` from the registry. `run_agent` does not
start a run; it *enqueues* one through the scheduler (D-14). Prompt overrides interpolate
`{{...}}` paths against the same normalized payload — **interpolation only, never control flow in a
string** (brief §6.6). Unknown paths render as the empty string and add a warning to the firing row.

### 8.1 Backtest

Because events are persisted (D-13), backtest is:

```go
events := store.EventsSince(projectID, now.Add(-7*24*time.Hour))
for _, e := range events {
    if match(rule, e) && conditions(rule, e) {
        results = append(results, Backtest{Event: e, WouldDo: describeActions(rule, e)})
    }
}
```

Stages 1 and 2 only — the guard and actions are deliberately not simulated, and the UI says so:
*"7 events matched. Loop protection and budget are evaluated live and may reduce this."* Simulating
debounce against historical timestamps is possible but would be a lie about ordering, and an
over-claimed dry run is worse than an honest partial one.

---

## 9. Loop protection (brief D5)

Five layers, kernel-owned, evaluated in order, each defaulting on, each with its own outcome class
and colour. Configured per trigger, with project defaults.

| # | Layer | Key | Terminal outcome |
|---|---|---|---|
| 1 | **Actor suppression** | `event.Actor` resolves to the agent this rule would run | `no action` (reason: `actor suppressed`) |
| 2 | **Debounce** | `(trigger_id, subject)` window, default 90s | `debounced` (+ link to absorbing run) |
| 3 | **Cancel in progress** | `(trigger_id, subject)` | previous run → `canceled` reason `superseded`; new run proceeds |
| 4 | **Depth counter** | causal chain on `subject`, default max 3 | `loop stopped` |
| 5 | **Budget** | project/day, agent/day, rule/day | `budget exceeded` |

Plus the **escape hatch**: `skip-agents` (also `skip-agent`, `[skip agents]`) anywhere in the PR
body, the triggering comment, or the head commit message short-circuits everything with outcome
`no action`, reason `skip token`.

`subject` is the PR number when one exists, else the ticket key, else the repo. This is the key that
makes "collapse a burst of pushes into one review" mean the right thing.

**Depth computation.** Walk `run.cause_event_id → event.cause_run_id` upward, counting hops where
the run was agent-caused and the subject is unchanged. Depth resets when a human acts on the subject
(a human comment, a human push, a manual run) — otherwise a human's intervention on a stalled chain
inherits the chain's exhausted budget, which reads as the product being broken.

**Rendering.** When layer 4 trips, the run is created in state `loop stopped` rather than not
created at all. This matters: a suppressed run that does not exist cannot be inspected, and the
loop chain view (UI spec §5.9) needs a row to hang the explanation on. The run has no container, no
cost, and a `chain` payload the UI renders vertically with the repeating element highlighted.

---

## 10. The run lifecycle

### 10.1 State machine

```
                     ┌──────────────────────────────── cancel ────────────────────────┐
                     │                                                                ▼
  queued ──▶ provisioning ──▶ running ──┬──▶ needs input ──────▶ running          canceled
     │            │              │      ├──▶ awaiting approval ─▶ running
     │            │              │      └──▶ completed
     │            │              └──────────▶ failed / timed out
     │            └─────────────────────────▶ failed          (provisioning error)
     └──────────────────────────────────────▶ loop stopped    (created terminal, guard layer 4)
```

Legal transitions are a table in `kernel/sched`, and the only writer of `runs.state` is the
scheduler. Every transition writes an audit row and publishes a `run` event onto the bus (which is
what lets `run · completed|failed|needs_input` be a trigger event).

**Derived vs stored.** UI spec §7 says run state is *derived from the last emitted activity*. The
implementation stores state explicitly but derives it from activities at the point of ingest: an
`elicitation` activity with no response transitions the run to `needs input` / `awaiting approval`;
a response transitions it back to `running`. Storing it makes queries fast; deriving it at ingest
makes the two impossible to disagree.

### 10.2 Admission control

A run leaves `queued` only when all of these hold:

1. Agent's `concurrency_cap` not reached.
2. The project's `running`-category column WIP limit not reached — **enforcing**, not advisory
   (brief §6.4). The column header renders `4/4 · queued: 2` from these same numbers.
3. Global container cap (workspace setting, default 6) not reached.
4. Project daily budget not exhausted; agent daily cap not exhausted.

Failing 1–3 leaves it queued (and the UI says which limit is holding it, by name — never a bare
spinner). Failing 4 terminates it as `budget exceeded`.

### 10.3 Provisioning — a checklist, never a spinner

Interaction rule 7 in the UI spec is non-negotiable. `Sandbox.Prepare` reports discrete steps
through a `ProvisionSink`, each becoming an activity row the user watches fill in:

```
✓ image ready (cached)            ✓ image built 41s        ← only one of these appears
✓ container created
✓ repo cloned  main@a1b2c3d
✓ branch created  dev/PAY-14-idempotency-keys
● setup script  (npm ci — 12s)
○ agent starting
```

The steering composer is **enabled during provisioning** and queues input, per the same rule.

### 10.4 Execution

1. Kernel resolves the context stack (§11) and renders the prompt: agent directive + project
   guidance + `always`-scoped wiki pages + ticket title/description/**acceptance criteria** + the
   trigger's prompt override, in that order, each in a labelled section.
2. Kernel writes container-side config: `.claude/settings.json` with the agent's tool permissions
   as allow/deny rules (D7 enforcement), the MCP server registration with the run token, and env
   including `CLAUDE_CODE_OAUTH_TOKEN`, git identity, and project secrets.
3. `AgentRuntime.Launch` starts the CLI and returns a `RunHandle`.
4. The adapter parses stdout NDJSON → `Activity` values with a verbosity `level`, and appends them
   through the kernel (which streams them to SSE subscribers and rolls up cost/tokens).
5. `RunHandle.Steer` / `.Respond` write to stdin between tool calls.
6. On `result`, outputs are collected: pushed branch, opened PR, posted comment/review, proposed
   wiki pages, criteria checked.

### 10.5 Teardown, and the failure-artifact rule

Interaction rule 5: **a failed run never leaves nothing behind.** On any terminal state, before the
container is destroyed, the orchestrator commits anything uncommitted and pushes the branch:

```
git add -A && git commit --no-verify -m "wip: <run summary> [lexicode run <id>]"
git push origin HEAD:refs/heads/<branch>
```

Two amendments to the original shape, both recorded under D-9 in
[00-decisions.md](00-decisions.md):

- **The orchestrator runs this, for every outcome — not just failures.** The container holds no
  repository credential (the clone step points `origin` at a tokenless URL as soon as the fetch is
  done), so a completed run's branch also reaches the remote here, and the pull request is opened
  from it. The credential is supplied in this one exec's environment.
- **Neither command is `|| true`.** The old version swallowed both failures and then reported
  *"Partial work pushed"* whatever happened. The step now reports which of three things occurred —
  committed and pushed, committed but the push failed (with the error), or nothing to commit — and
  the run's `partial_work` output row is written only in the first case, so the row and the message
  cannot disagree. An exec that could not start at all (a container the agent killed from inside)
  is recorded on the run as a level-1 warning rather than vanishing.

A successful push is recorded as a run output and named in the failure message: *"Failed after 6
steps. Partial work pushed to `dev/PAY-14-idempotency-keys`."* Then the container is removed and
the workspace volume dropped.

### 10.6 Crash recovery

Containers are labelled `lexicode.run=<run_id>` and `lexicode.instance=<instance_id>`. On boot the
scheduler reconciles:

- Runs in `provisioning`/`running` with a live container → **reattach** to its stream and continue.
  (Activities already persisted are not re-emitted; the adapter resumes from the container's log
  offset recorded in `runs.log_offset`.)
- Runs in those states with no container → terminate as `failed`, reason `orchestrator restarted`,
  and run the §10.5 artifact push if the branch exists remotely.
- Containers labelled with this instance but no matching non-terminal run → remove (orphans).
- Runs in `needs input` / `awaiting approval` → unchanged. Elicitations are durable by design;
  a human returning after a restart answers a question asked before it, and the run resumes.

### 10.7 Take over

`Stop(reason: takeover)` performs the artifact push, destroys the container, sets state `canceled`,
and returns a copy-paste block:

```
git fetch origin && git checkout dev/PAY-14-idempotency-keys
```

plus a note field whose contents are stored on the run and injected as the first message of any
subsequent run on the same ticket: *"A human took over and changed X."*

---

## 11. Context resolution

`ContextProvider`s are asked, in priority order, for items relevant to a run. Each item carries
`source`, `reason`, `tokens`, and content. The resolver:

1. Calls every registered provider with the `ContextRequest` (project, agent, ticket, changed paths
   if known, free-text task summary).
2. Concatenates in provider priority order, recording each item in `run_context_items`.
3. Enforces the project's context budget: if `always`-scoped items alone exceed the threshold, the
   run still proceeds but emits a warning activity and flags the project's context meter amber.

| Provider | Priority | Yields | `reason` shown in the panel |
|---|---|---|---|
| `project` | 10 | Project-wide agent guidance from settings | `project guidance` |
| `wiki` | 20 | `always` pages; `paths` pages whose globs match; `auto` pages by title/tag keyword match against the task | `always` / `matched path infra/deploy.ts` / `retrieved for "deployment"` |
| `event` | 25 | The occurrence that caused a trigger-spawned run — what happened, to what subject, by which actor — rendered from the normalized payload (contracts §4) | `the pull_request opened event that started this run (pr #219)` |
| `ticket` | 30 | Title, description, acceptance criteria, parent/sub-tickets | `ticket PAY-14` |
| `repofiles` | 40 | Enumerates `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/*` present in the checkout — **listed, not injected** (Claude Code reads them itself) | `repo file` |

The `run_context_items` rows are exactly what the run detail's Context panel renders, and the same
resolver runs in "dry" mode to produce the agent detail page's *"what every run of this agent sees"*
preview (UI spec §5.8). One code path, three surfaces — this is the brief's #1 differentiator and it
must not be reimplemented per surface.

**`verified_until` enforcement.** A daily job demotes `always` pages past their verification date to
`auto`, writes an audit row, and notifies the page owner. The demotion is a real data change, not a
display rule, because brief §6.5 requires stale pages to stop steering runs.

---

## 12. Attention: notifications and the needs-you surfaces

One notification row per run, **updated in place** (interaction rule 3). The unique index
`(user_id, run_id)` makes stacking impossible at the schema level rather than by convention.

Routing (brief D1): the **delegating human** — `runs.requested_by_user_id`, or for trigger-spawned
runs, the ticket's delegating human, falling back to the assignee, falling back to the project
owner. Never "everyone."

Tiering: `needs input` and `awaiting approval` push (browser Notification API, permission requested
at the first occurrence, never on load); `failed` pushes; `completed` updates a badge silently.

The three needs-you surfaces — home strip, board pinned lane, `/inbox` — are three renderings of one
query: non-terminal runs in `needs input` / `awaiting approval`, plus terminal runs in `failed` /
`loop stopped` unacknowledged, plus outputs awaiting review (open agent PRs, pending wiki
proposals). It is a single service method with a scope parameter. The *flavor* — question / approval
/ review / failure — is a computed field on the row, and every renderer prints it in words
(interaction rule 1).

---

## 13. Frontend architecture

```
web/src/
  app/          router, providers, shell (top bar, left rail, project header + tabs)
  routes/       one folder per route in UI spec §1, colocated loaders
  components/   the UI spec §7 component list, one folder each
  lib/          api client (generated types from openapi.yaml), sse client, keyboard registry,
                formatters (cost, duration, tokens, relative time)
  styles/       tokens.css (a literal transcription of UI spec §3), reset.css
```

**Live data.** One multiplexed SSE connection per tab: `GET /api/v1/stream?topics=project:PAY,run:01H…`.
Topics are managed by a hook; components declare what they need. Events carry `{topic, type, payload}`
and are applied to the TanStack Query cache by a single reducer, so there is exactly one place where
"a run activity arrived" becomes "the timeline re-renders". Reconnect with backoff and a
`Last-Event-ID` resume so a dropped connection never loses log lines.

**Keyboard.** A global registry maps `(scope, chord) → command`. Scopes stack (`global` < `route` <
`focused list` < `modal`). The command palette (`⌘K`) and the cheatsheet (`?`) both read from this
registry — the map in UI spec §6 is data, not fifteen scattered `keydown` handlers, which is also
what makes "every context menu displays its own shortcut" cheap.

**Rendering rules that are load-bearing, not cosmetic:**
- `StatusDot` is the *only* place status colour and glyph are chosen. Every status anywhere renders
  through it. (Guarantees the a11y rule that colour is never the sole carrier.)
- `ToolCallRow` groups consecutive same-tool activities client-side over the normalized stream, so
  the verbosity switch is instant and the grouping survives filtering.
- Cost is always rendered by `CostChip`, which owns the "estimate" affordance from D-5.
- Selection state (`?step=`, `?line=`, filters, verbosity) lives in the URL (interaction rule 12).

---

## 14. Cross-cutting

**Errors.** `application/problem+json` everywhere, with a stable `type` slug the frontend can switch
on. User-facing messages name the specific thing that failed and the specific next action.

**Config.** `~/.lexicode/config.yaml` for host, port, data dir, docker host, log level. Everything
else is in the database and editable in the UI — a settings screen that requires editing a YAML file
is a settings screen that does not exist.

**Audit.** Every mutation through a service writes `audit_log`. The writer takes the actor from
context (`human:<user_id>` | `agent:<agent_id>` | `trigger:<trigger_id>` | `system`), so attribution
is impossible to forget.

**Time.** All timestamps UTC RFC3339. Relative rendering is client-side only.

**Testing.**
- Unit: condition evaluator, interpolation, guard layers, state machine transitions, activity-type
  derivation from polled state.
- Integration with `module/testkit`: the full path event → trigger → guard → scheduler → fake
  runtime → activities → outputs, with no Docker and no network. This is the safety net for every
  later change.
- Docker-tagged integration (`-tags docker`): real container, real clone, a scripted fake `claude`
  binary in the image. Run in CI on Linux; runnable locally on demand.
- E2E (Playwright): the brief §3 chain against a seeded database with the testkit modules.
