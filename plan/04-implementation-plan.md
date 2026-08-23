# Implementation Plan

39 stories in 7 phases, strictly sequential. Story N assumes 1…N−1 are merged and green.

**Every story's definition of done, in addition to its own acceptance criteria:**
`make check` passes (go build, go vet, golangci-lint, go test ./..., tsc --noEmit, eslint, vitest);
new behaviour has tests at the level the story names; user-facing strings match the design docs
verbatim where the docs specify them; no new `TODO` without an issue reference.

**Sizing.** S = under a day, M = 1–2 days, L = 3–4 days for a competent agent working alone.

| Phase | Stories | Ships |
|---|---|---|
| 0 · Foundations | S01–S07 | A binary that serves an authenticated, empty, correctly-themed dashboard |
| 1 · Project core | S08–S13 | Projects, board, tickets — the human half of the product, fully usable |
| 2 · Forge | S14–S15 | A connected repo that bootstraps itself |
| 3 · Execution | S16–S24 | **The spine: delegate a ticket, watch a container work, get a PR** |
| 4 · Automation | S25–S32 | Triggers, loop protection, triage — the chain runs itself |
| 5 · Knowledge | S33–S35 | The wiki, and agent context made visible |
| 6 · Attention & release | S36–S39 | Inbox, governance, polish, and the §3 acceptance run |

The first end-to-end moment is **S24**. Everything before it is scaffolding for that moment;
everything after makes it repeatable, safe, and observable.

---

# Phase 0 — Foundations

## S01 · Repository scaffold and the binary — M

**Goal:** `go build ./cmd/lexicode && ./lexicode serve` opens a browser tab showing an app shell.

**Build:**
- Module layout exactly as [01-architecture.md](01-architecture.md) §5. Go 1.23+.
- `cmd/lexicode` with `serve`, `migrate`, `version` subcommands (stdlib `flag`, no CLI framework).
- Config loader: `~/.lexicode/config.yaml` + `LEXICODE_*` env overrides + flags, in that precedence.
  Fields: `host`, `port` (default 7717), `data_dir`, `docker_host`, `log_level`, `open_browser`.
- `web/` Vite + React + TS scaffold; `make web` builds to `web/dist`; `go:embed` serves it with SPA
  fallback (any unmatched non-`/api` path returns `index.html`).
- `Makefile`: `check`, `web`, `build`, `dev` (air/vite concurrently), `release` (cross-compile
  darwin/linux × amd64/arm64).
- `slog` JSON to file + human-readable to stderr. Graceful shutdown on SIGINT/SIGTERM.
- CI workflow running `make check` on Linux and macOS.

**Acceptance:** binary runs with no dependencies present; `--version` prints an ldflags-injected
version; killing it with SIGINT exits 0 within 2s; the embedded SPA loads at `/`.

---

## S02 · Kernel skeleton, module lifecycle, port registries — M

**Depends:** S01

**Build:**
- `kernel.Kernel` and `kernel.Module` exactly as [03-contracts.md](03-contracts.md) §1.
- Lifecycle runner: Init all → Start all → serve → Stop in reverse with a 30s deadline. Init error
  aborts boot naming the module; Start error marks the module `degraded` and continues.
- Typed port registries with duplicate-ID detection at registration.
- `GET /api/v1/system/modules` returning name, state, and degradation reason.
- **Import-graph test**: a test that walks the package graph and fails if `internal/kernel/...`
  imports `internal/module/...` or `internal/api/...`. This test is the architecture's only real
  defence; write it now.
- A `noop` demo module in tests exercising the full lifecycle including a Start failure.

**Acceptance:** registering two modules with the same port ID aborts boot with both names in the
error; a module whose Start fails leaves the server running and reports `degraded`.

---

## S03 · Store: SQLite, migrations, repositories — L

**Depends:** S02

**Build:**
- `kernel/store`: open with WAL, `foreign_keys=ON`, `busy_timeout=5000`, one write connection,
  read pool. `modernc.org/sqlite` (pure Go — keeps cross-compilation free; do not use cgo).
- Embedded forward-only migrations `migrations/0001_init.up.sql`…, applied on boot inside a
  transaction, with a `schema_migrations` table and a startup log line naming applied versions.
- **Migration 0001 creates the complete schema from [02-data-model.md](02-data-model.md).** All of
  it, now. Later stories add columns only when the model genuinely changed; they do not each ship
  their slice of the base schema.
- Repository pattern: one file per aggregate, hand-written SQL (no ORM), typed structs, JSON columns
  marshalled through typed Go types. `store.Tx(ctx, func(*Tx) error)` with panic-safe rollback.
- ULID generation, RFC3339 UTC helpers, fractional-position helper for drag ordering.
- A `store/seed` package producing a realistic fixture set (used by tests and `--demo`).

**Acceptance:** migrations apply to an empty file and are idempotent on restart; `go test` runs
against a temp file; foreign key violations surface as typed errors; the fixture set loads.

---

## S04 · Event bus — M

**Depends:** S03

**Build:**
- `kernel/bus`: `Publish(ctx, Event)` = insert (idempotent on `dedupe_key`, ON CONFLICT → drop and
  return `ErrDuplicate`) then fan out.
- Subscriptions by kind and by topic pattern; per-subscriber buffered goroutine with a slow-consumer
  policy of *block, log, and expose lag in module status* (never silently drop — a dropped trigger
  event is a missing agent run).
- Ack tracking: `dispatch_state` flips to `done` when every subscriber returns nil; a subscriber
  error marks `failed` and logs.
- Boot recovery: re-dispatch `pending` rows created before process start.
- Internal event emission helper used by services (`ticket.created`, `run.completed`, …) that sets
  `cause_run_id` from context.

**Acceptance:** publishing the same `dedupe_key` twice inserts once; a subscriber panicking does not
take down the bus; killing the process mid-dispatch and restarting re-delivers exactly the pending
events. Tests: unit + a restart-recovery test.

---

## S05 · Auth: users, sessions, invites, memberships — M

**Depends:** S03

**Build:**
- Argon2id hashing (`golang.org/x/crypto/argon2`) with tuned params in one place.
- First-run: with zero users, every API call except `POST /auth/setup` returns
  `problem: setup_required`; the SPA routes to a setup screen. Setup creates the `owner`.
- Login/logout, session cookie (`HttpOnly`, `SameSite=Lax`, `Secure` when TLS), sessions in DB with
  expiry and revocation, sliding refresh.
- Invite links: `POST /invites` returns a one-time URL with the token in the fragment-free path;
  redeem creates a `member`. No email delivery (D-8).
- Middleware: `RequireAuth`, `RequireOwner`, `RequireProjectMember`, and a `Actor` context value
  that the audit writer reads.
- CSRF: same-site cookie plus an `Origin` check on unsafe methods.

**Acceptance:** setup is refused once a user exists (409); an expired session returns 401 with
`problem: session_expired`; a member cannot reach an owner-only route; sessions survive restart.

---

## S06 · HTTP framework, SSE hub, audit writer — M

**Depends:** S04, S05

**Build:**
- `kernel/httpx`: mux over `net/http` 1.22 routing patterns; middleware chain (request id, logging,
  recovery, auth, CORS off by default); `problem+json` writer with stable `type` slugs; typed
  decode/validate helpers.
- SSE hub: per-connection topic subscription from the query string, fan-out from the bus,
  heartbeat comment every 20s, `Last-Event-ID` resume, backpressure that closes a stuck client
  rather than blocking the bus.
- `kernel/audit`: `Write(ctx, action, target, before, after)` taking the actor from context;
  helper wrappers used by every service mutation.
- `GET /api/v1/audit` with filters.

**Acceptance:** two browser tabs subscribed to different topics each receive only their own frames;
reconnecting with `Last-Event-ID` yields no gap and no duplicate; every mutation in later stories
that lacks an audit row fails a lint test that greps service methods for the audit call.

---

## S07 · Frontend shell — L

**Depends:** S06

**Build:**
- `styles/tokens.css`: a literal transcription of UI spec §3 — surfaces, semantic colours, type
  scale, the three animations. Theme handling exactly as specified (`:root`, the
  `prefers-color-scheme` block guarded with `:root:not([data-theme="light"])`, and
  `:root[data-theme="dark"]`), plus an in-app toggle persisted to user prefs.
- App chrome per UI spec §2.1: 40px fixed top bar, 208px left rail with project list and the
  permanent **Needs you** block (capped at 5 + "+N more"), collapsible at `⌘\` and below 1100px,
  persisted per user. Project header + tabs beneath, with count badges that show only actionable
  counts.
- Router with every route in UI spec §1 rendering a placeholder; 404 and error boundaries.
- API client generated from `openapi.yaml`; TanStack Query defaults; SSE client hook with reconnect
  and a single cache-application reducer.
- Keyboard registry with stacking scopes; `?` cheatsheet reading from the registry; `⌘K` palette
  shell (commands registered by later stories); `G`-prefixed navigation chords.
- Base components: `StatusDot` (the whole §4 vocabulary, colour **and** glyph), `EmptyState`,
  `CostChip` (with the D-5 estimate affordance), relative-time and duration formatters.
- Auth screens: setup, login, invite redemption. Density preference (32/28px rows).

**Acceptance:** rail collapse persists across reload; the cheatsheet lists every registered binding
with no hardcoded duplicate list; both themes pass the UI spec §10 contrast requirements (assert
with a token-contrast unit test); no page body scrolls horizontally at 1024px.

---

# Phase 1 — Project core

## S08 · Projects, workspace settings, the inheritance pattern — M

**Depends:** S07

**Build:**
- Project CRUD; key validation and uniqueness; colour; owner; archive.
- Workspace settings screen (owner-only) writing the single `workspace_settings` row.
- **The inheritance control**: a shared `<InheritedField>` component that renders the value plus
  *"Inherited from workspace: `main`. Override."* and, once overridden, *"Reset to workspace
  default."* Backed by nullable project columns (never copied defaults). Used by every project
  setting with a workspace default, from here on.
- Home `/`: the projects table (name, repo, open tickets, running agents, needs-you count, spend
  today vs ceiling, last activity). The Needs-you strip renders its empty state for now.
- Project Overview `/p/:key` About card with the fields that exist yet; the rest fill in as later
  stories land.
- Project settings shell with the §5.11 section rail and autosave + inline saved indicator (no
  global Save button).

**Acceptance:** creating a project with a duplicate key gives a field-level error; clearing an
override reverts to the live workspace value (change the workspace value and watch the project
follow); autosave shows "Saved" and survives navigation.

---

## S09 · Board columns and categories — S

**Depends:** S08

**Build:**
- Default column set on project creation: Backlog(`backlog`) · Ready(`ready`) · In Progress(`running`)
  · In Review(`review`) · Done(`done`) · Canceled(`canceled`).
- Column settings UI: rename, reorder, add, delete, set category, WIP limit, `auto_start_delegate`
  toggle with an explicit warning that it spends money.
- Guardrails: cannot delete the last column of category `backlog`/`running`/`done`; deleting a
  column requires choosing a destination for its tickets.
- **Category, never name**, everywhere in code. Add a lint test that fails on string comparisons
  against column names in `internal/`.

**Acceptance:** renaming a column changes nothing functional; a project always has the three
required categories; the auto-start toggle requires a confirm.

---

## S10 · Tickets, criteria, labels, sub-tickets — L

**Depends:** S09

**Build:**
- Ticket CRUD with atomic key allocation from `projects.ticket_seq` inside the insert transaction.
- Acceptance criteria: ordered, add/edit/reorder/delete/check, with `checked_by_run_id` support.
- Labels with colours; project-scoped.
- Sub-tickets: one level, enforced; `POST /tickets/{id}/subtickets` accepting an array of strings
  (the backing endpoint for selection→sub-tickets).
- Move endpoint writing column + fractional position; **a move never starts a run** (D3) except
  where the destination column has `auto_start_delegate` and the ticket has a delegate — and that
  path goes through the scheduler, is audited, and is a no-op until S22.
- Assignee (human) and delegate (agent) as separate fields from the start.
- `ticket_stream` writes for every status/field change, with actor attribution.
- Archive with the D-15 semantics (cancel active runs, keep history).

**Acceptance:** two concurrent creates never collide on a key (test with parallel goroutines);
a sub-ticket cannot be given a child; archiving is reversible from the audit log; every mutation
appears in the ticket stream with the right actor.

---

## S11 · Board and list UI — L

**Depends:** S10

**Build:**
- One view, two layouts (`⌘B`), a `group_by` picker (status / assignee / delegate / priority /
  label) — the board is `group_by` rendered horizontally, not a second data structure.
- Card anatomy exactly per UI spec §5.3, badges only when earned.
- Column headers: name, count, WIP as `3/4` (amber at limit, red over), the `⚡ auto-runs delegate`
  marker, and — on `running` — `4/4 · queued: 2` once S22 lands.
- Drag between and within columns with optimistic update and fractional positions.
- Filter chips, display-properties menu (`⇧V`), search.
- The **Needs you pinned lane** above the columns: full-width, amber left border, non-collapsible
  when non-empty, non-reorderable. Renders empty until S22; the component and its query land now.
- Keyboard: `C`, `J`/`K`, `Space` peek, `Enter`, `X` select, `S`/`P`/`A`/`D`/`L`/`E`/`R`.
  `D` opens the delegate picker (which enqueues from S22 onward).

**Acceptance:** dragging a card never produces a run in the audit log; the WIP limit colours at the
right thresholds; `group_by=delegate` and the board layout show the same tickets in the same groups.

---

## S12 · Ticket detail and the shared editor — L

**Depends:** S11

**Build:**
- `Editor` component used here and by the wiki, comments and directives: markdown, slash commands,
  `@` mentions of users, agents, wiki pages and tickets, paste handling. One component, four
  placements — do not fork it later.
- Ticket detail per UI spec §5.4: title (inline, `R`), description, acceptance-criteria checklist as
  a first-class block, sub-tickets, the **unified stream**, composer at the bottom.
- Selection in the description + `⌘⇧O` → sub-tickets, with a preview of what will be created.
- Sidebar (`⌘I`): status, priority, **assignee and delegate as visibly distinct rows with different
  iconography**, labels, linked PR with check status, branch with a copy-command button, timestamps.
- `RunSessionCard` component rendering in the stream (collapsed: agent, status, elapsed, cost,
  current step; expanded: activities inline; "open full run" link). Renders with fixture data now,
  live from S23.
- Mentioning an agent in the composer stages a run (enqueues from S22).

**Acceptance:** there is no Comments/Activity tab anywhere; converting a 4-line selection creates
exactly 4 sub-tickets with the right titles; the editor behaves identically in all four placements
(a shared test suite runs against each mount).

---

## S13 · Secrets store — S

**Depends:** S08

**Build:**
- `kernel/secrets`: AES-256-GCM, key at `~/.lexicode/master.key` (0600, generated on first run,
  refuse to start if the mode is wider), per-secret nonce.
- Set / rename / delete / list-names. **No read path in the API** (D-16). The only reader is
  `Get(ctx, id)` inside the process, used by the forge adapter and container env building.
- UI: project settings → Secrets, showing name, `set · 4 days ago`, Replace and Delete. Values are
  write-only fields.

**Acceptance:** a test asserts no `internal/api` file references the plaintext or ciphertext field;
rotating the master key file with existing secrets fails loudly rather than corrupting; a
world-readable key file prevents boot with an actionable message.

---

# Phase 2 — Forge

## S14 · ForgeProvider port and the GitHub adapter — L

**Depends:** S13

**Build:**
- The port exactly as [03-contracts.md](03-contracts.md) §2.2, in `kernel/ports`. Domain types
  (`PullRequest`, `Review`, `Comment`, `CheckSuite`, `Issue`, `Actor`, `RepoRef`) live in
  `internal/domain` — **no `go-github` type crosses the port**.
- `module/github`: `go-github/v6x` with a transport that respects `X-RateLimit-*`, retries 5xx with
  jittered backoff, and reports rate-limit exhaustion as module `degraded`.
- Token from the project's `repos.token_secret_id`; `Verify` checks scopes and returns a specific
  error naming a missing scope.
- Write methods enforce, in order: (1) the agent's `permissions` grant, returning
  `ErrPermissionDenied` with the grant named; (2) marker injection (D-9); (3) `run_outputs` + audit.
- `ReviewSpec.Event` rejects `APPROVE` with `ErrSelfApprovalForbidden`. **There is no merge method.**
- `CloneURL` returns an `x-access-token` URL; it must never be logged — add a redacting logger
  wrapper and a test that greps captured logs for the token.

**Acceptance:** unit tests against a recorded-fixture HTTP transport (go-vcr style) for every read
and write method; a permission-denied write produces no network call; the APPROVE rejection is
tested; token redaction is tested.

---

## S15 · Repo connect and project bootstrap — L

**Depends:** S14

**Goal:** brief §6.3 — *the empty state becomes a loading state.*

**Build:**
- Connect flow: owner/name + PAT → `Verify` → persist `repos` row + secret → record head sha and
  message for the About card.
- `POST /bootstrap/preview` returns, in one payload:
  - **Open issues** as importable tickets (title, body, labels, author) with checkboxes.
  - **Detected instruction docs**: `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/*`, `.github/copilot-instructions.md`,
    `README.md`, `docs/**` (depth 2) — each with a *proposed* `agent_scope` (`AGENTS.md`/`CLAUDE.md`
    → `always`; `.cursor/rules` with globs → `paths`; `docs/**` → `auto`; `README` → `auto`).
  - **Detected CI**: `.github/workflows/*` → two pre-filled triggers, **toggles off** (brief §6.3).
  - **Suggested agents** from detected stack (a Dev and a Reviewer with starter directives).
  - **Overview draft** generated from the README's first section.
- `POST /bootstrap/apply` applies **only the checked subset**. Nothing is created silently.
- UI: a single-gate connect screen followed by a checklist review screen with per-section select-all,
  then a progress view. Re-runnable later from project settings (`Re-scan repository`).

**Acceptance:** connecting a repo with 12 open issues offers 12 checked-by-default tickets and
creates exactly the checked ones; the suggested triggers are created disabled; re-running the scan
does not duplicate previously imported items (match on issue number / doc path).

---

# Phase 3 — Execution

## S16 · Agents — L

**Depends:** S15

**Build:**
- Agent CRUD; roster cards (avatar/colour, name, role, model, autonomy, runs this week, success
  rate, spend, enable toggle).
- Agent detail sections exactly per UI spec §5.8: Identity · Directive · Model & effort ·
  Permissions · Autonomy · Limits · Context preview (the last renders a placeholder until S34).
- **Identity** with git author name/email, defaulted from the agent name
  (`Reviewer <reviewer@agents.lexicode.local>`), and the explanatory line *"Events caused by this
  identity won't re-trigger this agent."*
- **Directive** in a monospace editor, versioned on every save (`agent_directives` append-only),
  with a version list and a diff view, plus a live token estimate.
- **Permissions** as checkboxes with a lock icon, visually distinct from the directive textarea
  (D7). Include the sentence explaining that unchecking `edit files` makes editing *impossible*,
  not discouraged.
- **Autonomy** as a four-stop dial ordered by increasing risk, with a confirm on the top rung.
- Limits: concurrency cap, daily spend cap, max wall clock, max steps.
- Starter roster action creating Dev and Reviewer with sensible directives and permissions
  (Reviewer: `edit_files:false`, `push_branches:false`, `submit_reviews:true`).

**Acceptance:** saving an unchanged directive creates no new version; two agents in one project
cannot share a name; disabling an agent removes it from delegate pickers but leaves history intact;
the permission checkboxes and the directive are unmistakably different controls in a screenshot test.

---

## S17 · Sandbox port and the Docker adapter — L

**Depends:** S16

**Build:**
- The port per contracts §2.3, plus `module/docker`.
- **Embedded Dockerfile** (D-7): Debian slim, git, curl, jq, ripgrep, build-essential, Node LTS,
  `@anthropic-ai/claude-code`, a non-root `agent` user, `/workspace` owned by it. Image tag is
  `lexicode/agent-base:<sha256 of the Dockerfile, first 12>`.
- Build-on-demand with progress lines fed to `ProvisionSink`; concurrent runs needing the same image
  share one build (singleflight).
- Container create/start with labels `lexicode.run`, `lexicode.instance`, `lexicode.project`;
  resource limits (cpu, memory, pids); `--init`; read-only root with writable `/workspace` and
  `/tmp`. **Superseded post-S39** — the POC ships a writable rootfs and runs as root; the
  resource limits stay. See the D-7 amendment in [00-decisions.md](00-decisions.md).
- `Exec` returning attached stdin/stdout/stderr and a `Wait`; `ReadFile`; `Destroy` (remove
  container + anonymous volume).
- `Reattach(InstanceRef)` for crash recovery, reading the container's log stream from an offset.
- Custom `image_ref` validation: `command -v git claude` inside, else a named error.
- Orphan sweeper: on boot and hourly, remove containers labelled with this instance that have no
  live non-terminal run.

**Acceptance (behind `-tags docker`, run in CI on Linux):** build → create → exec `echo` → destroy;
reattach after killing and restarting the test harness returns the same stream; destroying twice is
idempotent; a container with a wide-open image ref that lacks `claude` fails with the named error.

---

## S18 · Network policy and the egress proxy — M

**Depends:** S17

**Build:**
- A CONNECT proxy in the orchestrator (own port, loopback-bound, per-run auth via a header the
  container's `HTTP_PROXY` URL carries).
- Policy resolution: `none` → Anthropic API host + the repo's git host only; `allowlist` → plus
  `repos.network_allowlist`; `open` → no proxy, default bridge network.
- `none`/`allowlist` containers join an internal network with no default route; egress happens only
  through the proxy.
- Every allow/deny decision emits a `system` activity at level 2 with host and outcome, so
  *"the install failed because of the network policy"* is a visible fact.
- Project settings UI: three radio options with the honest wording from D-10, plus a domain list
  editor for `allowlist`.

**Acceptance (docker tag):** under `none`, `curl https://registry.npmjs.org` fails and the denial
appears in the run's verbose activities; under `allowlist` with that host added, it succeeds; under
all policies the Anthropic API is reachable.

---

## S19 · Workspace preparation and credentials — M

**Depends:** S18

**Build:**
- `CredentialSource` port + `oauth-token` implementation (D-5): a workspace setting where the user
  pastes the output of `claude setup-token`, stored as a secret; `Health()` surfaces validity in
  settings. Linux fallback: an explicit "Import from ~/.claude/.credentials.json" button — read at
  setup time only, never per run.
- Setup screen copy that tells the user exactly which command to run.
- Clone spec: shallow clone of the base branch, create the run branch from
  `repos.branch_template` (`{agent}/{ticket-key}-{slug}`, slug from the ticket title, collision
  suffix `-2`), configure `user.name`/`user.email` from the agent identity, and set the commit
  trailer template.
- Setup script execution with its output streamed into provisioning steps and a non-zero exit
  failing the run before the agent starts (with the script's output in the failure).
- Container file materialisation: `.claude/settings.json` from the agent's permissions (D7),
  `.lexicode/mcp.json` with the run-token endpoint, `.lexicode/prompt.md`.
- Env assembly: `CLAUDE_CODE_OAUTH_TOKEN`, project secrets, `GIT_*`, `HTTP(S)_PROXY`, `LEXICODE_RUN_ID`.

**Acceptance:** a run branch name is deterministic and collision-safe; a failing setup script
produces a `failed` run whose error contains the script output; the OAuth token never appears in
any activity, log line, or API response (asserted by a grep test over captured output).

---

## S20 · AgentRuntime port and the Claude Code adapter — L

**Depends:** S19

**Build:**
- The port per contracts §2.4, plus `module/claudecode`.
- Launch the CLI with the exact command line in contracts §3.1; deliver the prompt as the first
  stdin message.
- **NDJSON parser → activities** with the mapping table in contracts §3.2, including the per-tool
  title formatters and payload shapes for `Read`, `Edit`/`Write` (diff hunks), `Bash` (argv, exit,
  captured output with truncation), `Grep`/`Glob`, `TodoWrite`, and a compact honest fallback for
  unknown tools.
- Level assignment (0/1/2) at ingest; `group_key` set to the tool name so consecutive calls collapse.
- Tool-result correlation back onto the originating `action` activity (sets `ok`, `duration_ms`,
  result payload, `attempt` on retries).
- Usage and `total_cost_usd` from `result` → `Usage`; per-step token/cost attribution where the
  stream provides it, rolled up to the run.
- Timing capture split into queued / model / tool for the timing gutter.
- `Steer` writing to stdin **only between tool calls** (contracts §3.4); `Stop` sending SIGTERM then
  SIGKILL after a grace period; `Wait` returning the result.
- A `scripted` runtime in `module/testkit` that replays a fixture NDJSON file — this is what makes
  every later story testable without an API call.

**Acceptance:** a golden-file test parses a recorded stream-json session into an expected activity
list; a `Bash` failure produces `ok=0`; steering during a tool call is buffered and delivered after
it; a malformed line is recorded as a level-2 `system` activity and does not kill the run.

---

## S21 · The Lexicode MCP server — L

**Depends:** S20

**Build:**
- MCP server over HTTP at `/mcp/{run_token}`; tokens minted per run, scoped to that run, revoked on
  terminal state. Reachable from containers via `host.docker.internal` (Linux: `--add-host`).
- Tools exactly per contracts §3.3: `ask_human`, `set_step`, `propose_wiki_page`, `check_criterion`,
  `request_approval`.
- `ask_human` and `request_approval` **block** until answered: create an `elicitations` row, emit an
  `elicitation` activity, transition the run to `needs_input` / `awaiting_approval`, wait on a
  channel (with the run's wall-clock limit as the ceiling), return the response as the tool result.
- Autonomy short-circuit table from contracts §3.3, evaluated **after** `agent_permission_rules`.
- `request_approval` enriches the payload with the six fields an approval card must show (action,
  scope, impact, reason, alternatives, recovery) — the UI cannot render them if the server does not
  produce them.
- `propose_wiki_page` and `check_criterion` write through the wiki/ticket services (stubbed to
  persist rows now; UI in S35 and S12 respectively).
- Durability: a pending elicitation survives an orchestrator restart *if the container survives*;
  if it does not, the run terminates with a specific reason rather than hanging.

**Acceptance:** with the scripted runtime, an `ask_human` call parks the run in `needs_input` and
answering it resumes execution with the answer visible in the next activity; a `suggest`-autonomy
agent has every mutating approval denied with the specified message; an `agent_permission_rules`
row short-circuits before autonomy is consulted.

---

## S22 · The run scheduler — L

**Depends:** S21

**Goal:** the kernel-owned centre of the product (D-14). Nothing else may start a run.

**Build:**
- `Enqueue(ctx, RunRequest)` — the only entry point. Callers: manual delegate, `@agent` mention,
  column auto-start, the `run_agent` trigger action.
- Admission control per [01-architecture.md](01-architecture.md) §10.2, in order, with
  `hold_reason` written in words (*"waiting: Dev is at its 1-run limit"*, *"waiting: In Progress is
  at 4/4"*) — never a bare spinner.
- The state machine as a transition table; **only the scheduler writes `runs.state`**; every
  transition writes audit + publishes a `run` bus event.
- Prompt assembly: directive (snapshotted) + project guidance + context items (S34 wires the wiki;
  for now `project` + `ticket` providers) + ticket description + **acceptance criteria** + trigger
  prompt override. Store the rendered prompt on the run.
- Provisioning orchestration with the live checklist (§10.3), steering composer enabled throughout.
- Execution supervision: wall-clock timeout → `timed_out`; step cap → `failed`; usage rollup into
  `runs` and `budget_ledger`.
- **The failure-artifact rule** (§10.5): on every terminal state, commit-and-push whatever exists,
  record a `partial_work` output, and name the branch in the failure message.
- Teardown, token revocation, container destroy.
- **Crash reconciliation** on boot (§10.6), all four cases.
- Ticket coupling: on run start move the ticket to the `running`-category column; on PR opened move
  it to `review`; these are category lookups, never names.

**Acceptance:** with the fake sandbox and scripted runtime — concurrency caps hold under 20
simultaneous enqueues; a WIP-limited column queues rather than exceeding; a killed process with a
running container reattaches on restart and the activity stream continues without duplicates; a
forced failure leaves a pushed branch and a `partial_work` output; a `queued` run displays which
limit is holding it.

---

## S23 · Run list and run detail (read) — L

**Depends:** S22

**Build:**
- Run list per UI spec §5.7: status dot + label, agent, ticket, trigger-or-manual, duration, **cost
  and duration as columns**, started. Filter chips (individually removable), saved views, default
  view **Needs attention**. Distinct copy for filtered-empty vs never-had-any.
- Run detail three panes:
  - **Left — step timeline**: grouped tool calls (`Read 23 files ▸`), per-step timing gutter split
    queued/model/tool, per-step cost, failed steps auto-expanded, `f` jumps to next failure.
  - **Centre — detail**: tool-aware renderers (diff hunk for edits, `$` line + collapsible output +
    exit code for bash, one line for reads, honest compact fallback for unknown tools).
  - **Right — Context & cost**: `run_context_items` with the reason for each, token split
    (in/out/cache) and cost with the hover breakdown.
- Verbosity switch Summary / Normal / Verbose, live, client-side over `level`; default drops to
  Summary when ≥4 runs are in flight.
- Current-step line at the bottom while running, replaced by the outcome summary when done.
- Live streaming over SSE with the 10s first-`thought` stall warning from UI spec §7.
- Permalinks: `?step=`, `?line=`, verbosity and filters in the URL (interaction rule 12).
- `RunSessionCard` in the ticket stream now renders live.

**Acceptance:** a 500-step run scrolls at 60fps (virtualised list); switching verbosity is instant
and does not refetch; copying the URL and opening it in another tab lands on the same step with the
same verbosity; a failed step is expanded on load without a click.

---

## S24 · Intervention: steering, stop, takeover, approvals — M

**Depends:** S23

**Goal:** the first end-to-end milestone. After this story, delegate a ticket and get a PR.

**Build:**
- Steering composer: posts to `run_messages`, shows the queued message inline with *"Applied after
  the current step,"* marks it delivered when the adapter writes it.
- **Stop**: terminal `canceled`, artifact push preserved, reason recorded.
- **Take over**: stop + the copy-paste checkout command + a note field stored on the run and
  injected as the first message of the next run on that ticket.
- **Inline approval rows** in the timeline (never a modal) with tick/cross and the four responses:
  Approve · Approve with edits · Respond · Deny. "Always allow" writes one
  `agent_permission_rules` row and links to it in agent settings (interaction rule 8).
- **Answer-a-question** UI for `ask_human`: renders `header` chips, option cards with
  label + description, and an "Other" free-text choice that submits the typed text as the answer.
- Escalation: an unanswered elicitation older than 60s creates a notification for the delegating
  human (interaction rule 11 — inline when watched, inbox when not).
- Board/ticket/home surfaces now show real needs-you rows with the flavor in words.

**Acceptance — the story's real test:** on a scratch repo, create a ticket with acceptance criteria,
press `D`, delegate to Dev, and get: a provisioning checklist, a live activity stream, a question
answered from the run detail, a pushed branch, an opened PR, the ticket moved to the `review`
column, and a cost figure. Then repeat it, stop the run midway, and confirm the branch still exists.

---

# Phase 4 — Automation

## S25 · EventSource port and the GitHub poller — L

**Depends:** S24

**Build:**
- The port per contracts §2.1, including `Catalog()` — the trigger editor is generated from it.
- `module/github` poller per [01-architecture.md](01-architecture.md) §7: one goroutine per
  connected project, `poll_cursors` with etags, rate-limit-aware backoff.
- **Activity-type derivation** by diffing `poll_pr_state` (opened / synchronize / ready_for_review /
  closed). This is the story's hardest and most important part — `opened` vs `synchronize` is where
  the runaway loop lives.
- Reviews, review comments, issue comments, check suites, each with its deterministic `dedupe_key`.
- Normalization into the payload shape in contracts §4. Nothing downstream ever sees GitHub JSON.
- **Baseline pass** on first connect: record state, emit nothing, log it.
- **Actor attribution** (D-9): resolve marker comment → agent; commit author email → agent; branch
  prefix → agent. Then resolve the causing run where possible. Sets `events.cause_run_id`.
- Internal event emission for `ticket` and `run` event kinds from the respective services.

**Acceptance:** against recorded fixtures, a sequence of API snapshots produces exactly the expected
event stream with correct activity types; connecting a repo with 40 open PRs emits zero events;
replaying the same snapshot twice emits nothing the second time; an event caused by an agent's push
carries `cause_run_id`.

---

## S26 · The trigger engine — L

**Depends:** S25

**Build:**
- Trigger CRUD and the four-stage pipeline in §8: match → conditions → guard (S27 stub returning
  pass) → actions.
- Matcher: kind, activity types, filters (branch glob, path glob, label).
- Condition evaluator over the condition tree with every operator in contracts §4.1, total
  evaluation, defined `nil` behaviour, and no expression language.
- `{{...}}` interpolation: path lookup only, unknown → `""` + warning. A test asserts that no
  construct resembling control flow is accepted.
- `trigger_firings` written for **every** terminal outcome including `no_action`, with the reason in
  words. Idempotent on `(trigger_id, event_id)`.
- Bus subscription with per-project serialization so ordering within a subject is deterministic.

**Acceptance:** a rule with `IF author is an agent AND files_changed < 400` produces `succeeded` and
`no_action` firings for the right events with reasons; re-dispatching the same event creates no
second firing; an unknown interpolation path yields an empty string and a visible warning.

---

## S27 · Loop protection — L

**Depends:** S26

**Goal:** brief D5 and §9 risk #1. This story is not optional and does not ship later.

**Build:**
- All five layers per [01-architecture.md](01-architecture.md) §9, in `kernel/guard`, each with its
  own outcome class, each defaulting on.
- `subject_key` derivation (`pr:219` / `ticket:PAY-14` / `repo`) from the event descriptor template.
- Debounce with a timer per `(trigger, subject)` and an `absorbed_by_run_id` link on the debounced
  firing.
- Cancel-in-progress: previous run → `canceled` with reason `superseded by run #N`.
- Depth counter walking the causal chain, with the **human-action reset** rule.
- Budget checks against `budget_ledger` at three scopes (project/day, agent/day, rule/day).
- `skip-agents` token scanning of PR body, triggering comment, and head commit message.
- **A `loop stopped` run row is created, not suppressed** — with no container, no cost, and a
  `chain` payload for the visualisation.
- `GET /runs/{id}/chain` returning the causal chain.

**Acceptance:** a synthetic ping-pong (review → push → review) stops at depth 3 with a `loop stopped`
run whose chain lists the exact sequence; a burst of five pushes within 90s yields one run and four
`debounced` firings all pointing at it; an agent's own push does not re-trigger its own rule; a human
comment on a depth-3 subject resets the counter; `skip-agents` in a PR body suppresses with reason
`skip token`.

---

## S28 · Trigger actions — M

**Depends:** S27

**Build:** five `TriggerAction` implementations in `module/actions`:
- `run_agent` — agent, optional prompt override with interpolation, output destination.
  **Calls `Scheduler().Enqueue` and nothing else** (D-14).
- `create_ticket` — always into **triage**, never onto the board, with provenance text
  (*"Created by trigger `CI failed → file a ticket` from run #482"*).
- `move_ticket` — **by category**, with a named error if the project has no column of that category.
- `post_comment` — through the forge port with the agent's marker, so it cannot re-trigger.
- `notify` — through the `Notifier` port to the delegating human.

Each ships a `Schema()` that drives the THEN form and a `Describe()` used by rule cards and backtest.

**Acceptance:** `create_ticket` output is invisible on the board until accepted in triage;
`move_ticket` configured for a renamed column still works after the rename; a `post_comment` action's
own comment produces an event that actor-suppression drops.

---

## S29 · Trigger UI — L

**Depends:** S28

**Build:**
- **List**: rule cards that read as prose (the exact layout in UI spec §5.9), with the outcome
  sparkline coloured by class, the `14 ok · 3 no action · 1 loop` breakdown, the actor-suppression
  line, last-fired, and an enable toggle.
- **Editor** with literal `WHEN` / `IF` / `THEN` section headers:
  - WHEN: event picker → activity-type chips → filters, all generated from
    `GET /trigger-catalog`. `opened` vs `synchronize` visually distinct with the helper text that
    says why.
  - IF: `field | operator | value` rows; operators type-prefixed in the dropdown; `+ And` as an
    inline link; `Add Or group` as a separate, heavier button.
  - THEN: action picker rendering each action's `Schema()`; prompt override with an interpolation
    field picker.
- **Loop protection panel**, always visible, defaults on, one row per layer with a toggle and the
  plain-language description from UI spec §5.9's table.
- **Loop chain view** on stopped runs and in trigger history, rendered vertically with the repeating
  element highlighted.

**Acceptance:** adding a new event to an `EventSource`'s `Catalog()` makes it appear in the editor
with no frontend change (verify with the cron source in S32); the loop panel cannot be fully
disabled without an explicit per-layer action; the rule card renders correctly with zero firings.

---

## S30 · Backtest — M

**Depends:** S29

**Build:**
- `POST /triggers/{id}/backtest?days=7` replaying stored events through stages 1–2 only.
- Result: count, the actual matching events with timestamps and subjects, and each action's
  `Describe()` output for what it would have done.
- UI: a button in the editor rendering *"This rule would have fired 7 times in the last 7 days"*
  with the event list, plus the honest caveat that guard and budget are evaluated live.
- Works on an unsaved draft rule (post the draft body, not just the id).

**Acceptance:** editing a condition and re-running backtest changes the count without saving the
rule; a project with no stored events shows a distinct empty state explaining that history builds up
from repo connection.

---

## S31 · Triage — M

**Depends:** S28

**Build:**
- Triage list per UI spec §5.5: single column, provenance line on every row, `J`/`K`, `Space` peek.
- `1` accept (moves to the default `backlog`-category column) · `2` mark duplicate (merge, transfer
  attachments and links, redirect) · `3` decline (cancel with optional reason) · `H` snooze (until a
  time **or until new activity** — the latter stored as `state='snoozed'` with a null `snooze_until`
  and un-snoozed by the next event on the subject).
- Tab badge counting untriaged only.
- Board and all ticket queries exclude pending-triage tickets (the invariant from
  [02-data-model.md](02-data-model.md) §10.7).

**Acceptance:** a trigger-created ticket never appears on the board before acceptance; `2` on a
duplicate leaves one ticket with both provenances; a snoozed-until-activity item reappears when its
PR gets a comment.

---

## S32 · The cron event source — S

**Depends:** S29

**Build:** `schedule.cron` as a second `EventSource` — validates cron expressions, fires
`schedule`·`cron` events for triggers that use it, catches up at most one missed firing after a
restart (never a storm), and contributes its own `Catalog()`.

**Acceptance:** the trigger editor offers the schedule event and its fields with no frontend change
— this is the story that proves the port boundary is real. A restart after 3 missed firings emits
exactly one.

---

# Phase 5 — Knowledge

## S33 · Wiki — L

**Depends:** S24

**Build:**
- Pages with the full front-matter set (title, parent, owner, `verified_until`, `agent_scope`,
  `paths`, tags); two-level tree; drag to reorder (fractional positions); versions on every save.
- FTS5 search as the primary navigation, `/` focuses it; the tree is secondary.
- `@`-mentions between pages, tickets and agents, populating `mentions` with the **full containing
  paragraph**; a backlinks pane rendering those paragraphs, plus a collapsed *Unlinked mentions (3)*
  disclosure with one-click linking.
- Flat tags with a tag index page.
- `ScopeBadge` in the tree and the page header for all five scope values, `ALWAYS` in amber because
  it costs context on every run.
- Page header: title, owner avatar, `verified until …` turning red past due, scope badge with an
  edit affordance, tags.
- Reuses the `Editor` from S12 unchanged.

**Acceptance:** a page cannot be nested three deep; search finds a page by body text within 100ms on
500 pages; renaming a page updates inbound links and keeps backlink paragraphs correct; a page past
`verified_until` renders red before any demotion job runs.

---

## S34 · Context resolution — L

**Depends:** S33

**Goal:** brief §4's first differentiator. One resolver, three surfaces.

**Build:**
- The `ContextProvider` port per contracts §2.6 and all four implementations in `module/context`:
  `project` (10) · `wiki` (20) · `ticket` (30) · `repofiles` (40).
  - `wiki` yields `always` pages, `paths` pages whose globs match the run's changed paths, and
    `auto` pages matched by title/tag keywords against the task summary — each with the exact
    `reason` string the panel renders.
  - `repofiles` **lists without injecting** (D-11) and marks items `repo file`.
- Resolver called by the scheduler during prompt assembly; writes `run_context_items`.
- Context budget enforcement: if `always` items exceed the project threshold, the run proceeds, a
  warning activity is emitted, and the meter flags amber with the advice inline (*cut what the agent
  can read from the code; keep pitfalls, rationale, and conventions that differ from defaults*).
- `ContextMeter` component on the wiki tree, the Agents tab, and the run detail — one component.
- Agent detail **Context preview** (`Dry: true` resolve): *"what every run of this agent sees"* with
  the resolved stack and a total token count.
- Run detail **Context panel** now renders real data with real reasons.
- Daily `verified_until` job: demote expired `always` pages to `auto`, set `demoted_at`/`demoted_from`,
  audit, notify the owner. Runs on boot and every 24h.

**Acceptance:** a run's Context panel lists exactly the pages that were injected, each with a reason
that names *why* (`always`, `matched path infra/deploy.ts`, `retrieved for "deployment"`); the agent
preview and an actual run agree token-for-token when nothing changed between them; an expired
`always` page is demoted and stops appearing in the next run's context.

---

## S35 · Wiki import and agent proposals — M

**Depends:** S34

**Build:**
- Import from detected repo instruction files (D-11), re-runnable, idempotent on `imported_from`,
  with a preview and per-file checkboxes and proposed scopes. Reuses the S15 detection.
- Agent proposals (`propose_wiki_page` from S21) rendered in the tree with a dashed border and a
  `PROPOSED` chip; the page renders as a **diff** with Accept / Edit / Dismiss. Never auto-written
  (interaction rule 10).
- Accepting a proposal that edits an existing page performs a three-way check against
  `proposed_base_version` and surfaces a conflict rather than clobbering.
- Proposals appear in the needs-you surfaces with flavor *review output*.

**Acceptance:** an agent proposing a page produces a reviewable diff and zero live pages; dismissing
leaves an audit row; importing twice does not duplicate; accepting a stale edit proposal shows a
conflict.

---

# Phase 6 — Attention and release

## S36 · Notifications, inbox, and the needs-you surfaces — L

**Depends:** S35

**Build:**
- Notification service with the D1 routing rule (delegating human → ticket delegate's requester →
  assignee → project owner; never "everyone") and the `UNIQUE(user_id, run_id)` in-place update.
- Tiering: `needs input` / `awaiting approval` / `failed` push (browser Notification API, permission
  requested at the first occurrence, never on page load); `completed` silently updates a badge.
- `/inbox`: one row per blocked item, grouped by project, approvals sorted to the top always, flavor
  in words, inline action, `J`/`K`/`Enter`/`A`/`X`.
- Home needs-you strip: horizontally scrolling cards sorted question → approve → failed → review,
  each with an inline primary action. **Answering a question works from the card without
  navigating** — that is the whole point of the surface.
- One service method with a scope parameter backing all three surfaces plus the rail block and the
  board lane.

**Acceptance:** four concurrent runs entering `needs input` produce four rows and never a fifth on
re-entry; answering from the home card resumes the run without a navigation; a notification for a
run that later completes updates in place rather than stacking; the strip is keyboard-navigable.

---

## S37 · Governance and audit surfaces — M

**Depends:** S36

**Build:**
- Spend surfaces: project header chip (today vs ceiling), Overview About card, run list column,
  per-step cost with the input/output/reasoning/cache hover split — all via `CostChip` with the
  D-5 estimate affordance.
- Budget configuration: project daily ceiling, per-agent daily cap, per-rule cap, each with the
  inheritance control from S08. Exhaustion produces `budget exceeded` runs and a distinct banner.
- Audit log UI with actor/action/target filters and before/after diffs.
- Danger zone: archive project, delete project (typed confirmation naming the counts of tickets,
  runs and pages that will go), rotate repo token, revoke sessions.
- Diff-size warning on agent PRs above a configurable threshold (brief §7's review-bottleneck row).

**Acceptance:** hitting the daily ceiling stops new runs with the right status and a banner that
names the ceiling and when it resets; the audit log shows an agent's PR comment attributed to the
agent, not to the token owner; project deletion requires typing the key.

---

## S38 · Polish: empty states, activation, accessibility, responsive — L

**Depends:** S37

**Build:**
- Every empty state from UI spec §8, verbatim, with one primary CTA each and the detected-content
  variants (*"Import 12 open issues"*, *"Import AGENTS.md (detected)"*).
- The two special moments: **first completed run** (mark it, then teach the next action — review the
  diff, or turn the feedback into a wiki page) and **first `needs input`** (unmissable treatment,
  notification routed to the delegating human).
- Command palette completion: every registered command, `⌘J` as the separate ask-an-agent palette.
- Accessibility per UI spec §10: focus rings never removed, glyph alongside colour everywhere (a
  test asserts every `StatusDot` usage passes a label), live regions announcing state transitions
  and step boundaries only — never every log line, contrast checks in both themes.
- Responsive per UI spec §10: three-pane ≥1400, right pane collapses 1100–1400, rail collapses and
  run detail stacks <1100. **The inbox and the approve/answer actions must work on a phone.**
- Every wide element scrolls in its own container; the body never scrolls horizontally.

**Acceptance:** an axe-core pass with zero critical violations on every route; the inbox is usable at
390px wide and a question can be answered there; every empty state matches the spec's copy exactly
(snapshot test).

---

## S39 · End-to-end acceptance, docs, and release — M

**Depends:** S38

**Build:**
- **The brief §3 acceptance run, automated** against a scratch GitHub repo, as a tagged integration
  test and as a documented manual script:
  1. Write a ticket with acceptance criteria, delegate to Dev.
  2. Dev runs in a container, opens a PR, ticket moves to In Review.
  3. Trigger *PR opened by an agent* spawns Reviewer.
  4. Reviewer posts a severity-tagged review.
  5. Trigger *changes requested* spawns Dev on the same branch.
  6. Trigger *CI failed* spawns Dev to fix.
  7. The loop guard stops the cycle at depth 3 and renders the chain.
  8. A human merges. Nothing else can.
- **The two timing goals from brief §10**, measured and recorded in the README: connect-repo to
  first run in flight under five minutes; the six-step chain configured in under ten.
- `docs/`: install, first project, the OAuth token step, Docker requirements, network policy,
  loop protection explained, troubleshooting (Docker unreachable, token expired, rate limited,
  image build failure).
- `lexicode doctor`: checks Docker reachability, image presence, OAuth token validity, GitHub token
  scopes, disk space, port availability — and prints the fix for each failure.
- Release: `make release` cross-compiling darwin/linux × amd64/arm64 with checksums; a one-line
  install script; `--demo` seeding a populated demo project.

**Acceptance:** a fresh machine with only Docker installed reaches a first completed agent run by
following `docs/install.md` alone, with no other guidance. That is the V1 bar.

---

# Dependency map

```
S01 ─ S02 ─ S03 ─┬─ S04 ─┐
                 └─ S05 ─┴─ S06 ─ S07 ─ S08 ─┬─ S09 ─ S10 ─ S11 ─ S12 ─┐
                                             └─ S13 ────────────────────┴─ S14 ─ S15 ─ S16 ─
   S17 ─ S18 ─ S19 ─ S20 ─ S21 ─ S22 ─ S23 ─ S24 ─┬─ S25 ─ S26 ─ S27 ─ S28 ─┬─ S29 ─┬─ S30
                                                  │                         │       └─ S32
                                                  │                         └─ S31
                                                  └─ S33 ─ S34 ─ S35 ─ S36 ─ S37 ─ S38 ─ S39
```

Two places admit parallelism if more than one agent is working: **S13 alongside S09–S12**, and
**S33–S35 alongside S25–S32** (both branch from S24 and only rejoin at S36). Everything else is a
genuine chain.

# Traceability

Every load-bearing product decision, and where it is actually enforced.

| Requirement | Enforced in |
|---|---|
| D1 assignee ≠ delegate | Schema §4 (two columns), S10, S12 sidebar, S36 routing |
| D2 columns carry categories | Schema §4, S09 lint test, S28 `move_ticket` |
| D3 drag never starts a run | S10 (no enqueue on move), S11 acceptance test |
| D4 four flavors of needs-you | S24, S36, `StatusDot` + flavor computation |
| D5 loop protection, 5 layers | S27 in `kernel/guard`, S29 panel, S27 chain view |
| D6 the human merges | S14 — no merge method on the port; APPROVE rejected |
| D7 guidance ≠ enforcement | S16 (checkbox vs textarea), S19 (settings.json), S18 (network) |
| Context panel / budget meter | S34, one resolver, three surfaces |
| `verified_until` demotion | S34 daily job — a data change, not a display rule |
| Backtest | S30, made cheap by D-13 |
| Rule health with 8 outcome classes | S26 firings, S29 sparkline |
| Failure leaves an artifact | S22 §10.5, tested |
| Triage as the pressure valve | S31 + the board-query invariant |
| Notification never stacks | `UNIQUE(user_id, run_id)` + S36 |
| "Always allow" is a scoped rule | S24 → `agent_permission_rules`, shown in agent settings |
| Provisioning is a checklist | S22 §10.3, steering enabled during it |
