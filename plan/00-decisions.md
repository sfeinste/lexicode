# Technical Decisions

Product decisions D1–D7 live in [../design/product-brief.md](../design/product-brief.md) §5 and are
treated here as requirements, not choices. What follows are the *technical* decisions. Each has an
ID; the rest of the plan cites them.

---

## D-1 — Go binary with an embedded React SPA

`cmd/lexicode` builds to one static binary. The frontend is React + TypeScript + Vite, built at
compile time into `web/dist` and embedded with `go:embed`. No runtime asset dependency, no Node on
the target machine.

**Why Go:** first-class Docker SDK, the concurrency model matches a supervisor that babysits N
containers and multiplexes their output streams, single-binary cross-compilation is free, and the
long-lived-process story (graceful shutdown, reattach after restart) is boring in the good way.

**Why a real SPA and not server-rendered HTML:** the spec's load-bearing screens — the three-pane
run detail with live-switchable verbosity, the board with drag and `group_by`, the command palette,
live cost and token meters — are stateful client surfaces. Server-rendered HTML fights all four.

**Frontend libraries:** React 19, Vite, TanStack Router (typed routes; brief requires selection
state in the URL), TanStack Query (server cache + optimistic mutations), Zustand (ephemeral UI
state: rail collapse, verbosity, palette). Styling is plain CSS Modules over a `tokens.css` that is
a literal transcription of UI spec §3 — no utility framework, because the spec prescribes exact
tokens and a utility layer would let them drift. No component library; the spec's component list
(§7) is the component library.

## D-2 — SQLite, WAL mode, single file

`~/.lexicode/lexicode.db`. WAL, `foreign_keys=ON`, `busy_timeout=5000`, one writer connection and
a pool of readers. Migrations are numbered, embedded, forward-only, applied on boot.

A self-hosted single-box product with 2–8 users has no workload that justifies operating a second
process. All schema stays ANSI-ish so a future Postgres adapter is a store implementation, not a
rewrite: no SQLite-specific types in domain code, timestamps stored as RFC3339 UTC text, JSON
columns accessed through typed Go structs rather than SQL JSON functions in business logic.

## D-3 — GitHub events by polling; the event *source* is a port

V1 ships one `EventSource`: a GitHub poller. It diffs API state (pull requests, reviews, review
comments, issue comments, check suites) on an interval and emits normalized events onto the kernel
bus — the same event shapes a webhook receiver would emit, including activity types
(`opened` vs `synchronize` etc., which loop protection depends on).

**Why polling:** a binary on a laptop has no public ingress. Requiring a tunnel makes the first
hour of the product a networking exercise, and brief §9 names the first hour as an adoption risk.
Latency cost is bounded (default 30s) and irrelevant to a workflow measured in minutes.

A webhook receiver is a second implementation of the same port, later, with no engine changes.

**Consequence to respect:** polling cannot see events that leave no API trace, and it must
dedupe. Every emitted event carries a deterministic `dedupe_key`; the poller persists a per-source
cursor. See [01-architecture.md](01-architecture.md) §7.

## D-4 — Claude Code CLI, headless, stream-json, in a Docker container

The container runs:

```
claude -p --output-format stream-json --input-format stream-json --verbose
```

The orchestrator writes user messages to stdin (steering, elicitation answers) and parses NDJSON
from stdout into typed activities. This is the runtime's *only* interface; the adapter converting
that stream into domain activities is the single place that knows Claude Code's wire format.

**Why the CLI and not the Agent SDK in-process:** the container boundary is a hard product
requirement (real isolation, real network policy, per-run workspace). Running the SDK in-process
would put agent execution in the orchestrator's address space and make the sandbox decorative.

## D-5 — OAuth subscription credentials, provisioned once, injected per run

The user runs `claude setup-token` once and pastes the long-lived token into Lexicode settings. It
is stored in the encrypted secrets store and injected into each container as
`CLAUDE_CODE_OAUTH_TOKEN`. On Linux, reading `~/.claude/.credentials.json` is offered as a
fallback path at setup time only — never read per run.

Credential resolution is a small interface (`CredentialSource`) so an API-key mode is a later
addition without touching the runtime adapter.

**Cost reporting:** the `result` message from the CLI carries `total_cost_usd` and full usage
(input / output / cache-read / cache-creation). Those numbers are recorded and displayed. Because
subscription usage is not billed per token, every dollar figure in the UI renders with an
"estimate" affordance — the number is honest about what it is. Token counts are exact.

## D-6 — Sandbox and AgentRuntime are two ports, not one

`Sandbox` owns *where* code executes (image, container, workspace, network, exec, streams).
`AgentRuntime` owns *what* executes and how its output is interpreted (the claude command line,
the stream-json parse, the steering protocol).

They are composed at run time: `claude-code` runtime + `docker` sandbox. Keeping them separate is
what makes "add another agent runtime" (Codex, Aider, a shell script) and "add another sandbox"
(remote Docker host, Kubernetes, a plain local process for tests) independent changes. It also
gives the test suite a `fake` sandbox and a `scripted` runtime so the entire scheduler, trigger
engine and loop guard can be tested without Docker.

## D-7 — Container image built locally from an embedded Dockerfile

The binary embeds a Dockerfile (Debian slim + git + Node LTS + `@anthropic-ai/claude-code` + curl,
jq, ripgrep, build-essential). On first run it builds `lexicode/agent-base:<content-hash>` and
streams build progress into the provisioning checklist. The tag is the hash of the Dockerfile, so
upgrading the binary rebuilds exactly when the Dockerfile changed.

Projects may override with `repo.image_ref`; the orchestrator then validates that `git` and
`claude` are on `PATH` inside it and refuses the run with a specific error if not.

No registry dependency, no publishing pipeline, and dependency caching (brief §9 "container cold
start") is a layer in an image the user owns.

> **POC amendment (owner's decision, post-S39).** The container ships **unrestricted**: writable
> root filesystem and uid 0, overriding the image's `USER agent` at container-create time. The
> Dockerfile is unchanged, so the image itself still defaults to non-root.
>
> Why: the hardened container made the "bring your own image" escape hatch the *only* way to add
> a toolchain. `apt-get install` and `npm install -g` both failed on permissions, and `$HOME` was
> not writable, so a run could not install Go, a JVM or a Python environment even with the
> network open — and `image_ref` has no UI, so the workaround was editing the database. On a
> local single-owner proof-of-concept that cost more than it bought.
>
> What it costs: nothing an agent writes is confined any more — it can modify binaries and
> libraries in the image layer, and it holds root inside the container. Lexicode bind-mounts no
> host path in normal operation (the workspace is an anonymous volume), but a root container that
> *is* given a host bind writes to the host as root, which is the sharp edge to remember before
> adding one.
>
> Kept, because they are not isolation controls: the CPU / memory / pid limits (2 CPUs, 4 GiB,
> 512 pids — stability, so a runaway agent cannot take the laptop down) and the
> `.git/info/exclude` entries for `.lexicode/` and `.claude/` (they keep the run's live MCP token
> out of the user's repository, which is a correctness bug at any posture). Everything above the
> container — tool permissions, autonomy gates, the absent merge capability, loop protection,
> budgets — is untouched.
>
> The full record, including how to reinstate each piece, is the "Container posture" block in
> `internal/module/docker/sandbox.go`.

## D-8 — Human accounts with password auth and session cookies

A `users` table, first-run creates the owner, further members join by an invite link (copyable —
no email delivery in V1). Argon2id password hashing, `HttpOnly; SameSite=Lax; Secure` when served
over TLS, sessions in the database with server-side revocation.

Assignee, delegating-human notification routing (D1), audit attribution and the entire §6.8
cross-project surface are meaningless without real identity. Single-user-no-auth would make four
shipped features decorative and would be expensive to un-ship.

Out of scope, restated: SSO, SAML, email delivery, password reset flows (owner can reset a member
from settings), per-object permissions. Roles are `owner | member` at the workspace level and
membership is per project.

## D-9 — Agents share one repo token and are distinguished by git identity

One GitHub PAT per project. Each agent carries its own `git_author_name` / `git_author_email`
(e.g. `Reviewer <reviewer@agents.lexicode.local>`), which is what commits are attributed to, plus
a machine-readable trailer in every commit body and an HTML-comment marker in every PR/comment/review
body it authors:

```
<!-- lexicode:actor=agent:<agent_id> run=<run_id> -->
```

**Actor suppression (D5 layer 1) therefore keys on three signals, in order:** the marker (exact,
covers comments and reviews), the commit author/committer email (covers pushes), and the branch
naming template (covers PR-open events). Any hit means the event was caused by that agent.

A `agents.forge_token_secret_id` column exists and is nullable from day one, so upgrading a single
agent to a real bot account is a settings change, not a migration.

### Amendment — the push is orchestrator-owned, and the token never coexists with the agent

The original shape had the container hold the project's PAT for the whole run: the clone URL is
`https://x-access-token:<PAT>@host/owner/repo.git`, and `git remote add origin` wrote it into
`.git/config`, where it stayed. That was survivable while the container was non-root on a read-only
rootfs with an allowlist egress policy. It is not survivable under the POC posture (root, writable
rootfs, `open` network): a compromised or merely confused agent can read `.git/config` and send the
project's write credential anywhere.

So the credential and the agent no longer coexist:

- The clone still fetches with the tokenized URL — a private repository cannot be read without
  one — but the same step ends with `git remote set-url origin <tokenless URL>`. It also fetches
  remote-tracking refs first, while the credential is live, so a run that has to work on another
  branch still can. The tokenized URL is supplied in the clone exec's own environment; it is never
  in the container's `Config.Env`, so no later exec — the agent's included — inherits it.
- **The orchestrator pushes.** After the agent process exits, the scheduler runs one exec that
  commits anything uncommitted, pushes the branch, and reports what it did. The credential rides
  in that exec's environment as git's config-via-environment
  (`GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n` carrying
  `http.extraheader=AUTHORIZATION: basic <base64("x-access-token:<PAT>")>`) — never in argv, where
  `/proc/<pid>/cmdline` would expose it, and never back into `.git/config`, where it would outlive
  the command. This is the same mechanism S19 already used to carry `commit.template` and
  `core.hooksPath` into the container.
- The agent therefore cannot push, by construction. The prompt says so plainly — guidance about a
  mechanism, which is what prompts are for; the enforcement is the absent credential (brief D7).
- `push_branches` still gates the push, now on the orchestrator's side: an agent without the grant
  gets no branch on the remote, whatever it committed.

Owning the push buys the D-9 attribution check the hooks could never guarantee. The commit-msg hook
and the repository-local git identity both live *inside* the container, where a root agent defeats
them with `--no-verify` or a `git config` call — and the poller reads the `Lexicode-Run:` trailer
for depth attribution, so a missing trailer silently weakens loop protection. At push time the
orchestrator therefore:

- sets the trailer and the agent's identity **authoritatively** on the commit it makes itself (its
  own environment, hooks disabled — it will not execute a script the agent could have edited);
- **verifies and records** for agent-authored commits: a level-1 warning naming every commit being
  pushed that lacks the trailer or was authored under another identity. It does not rewrite the
  agent's history — that would change what the human reviews, and a wrong guess is unrecoverable.

Residual: an agent can still author commits without the trailer. The guarantee is that this is
*visible on the run*, not that it cannot happen.

## D-10 — Network policy is enforced by an orchestrator-run egress proxy

Three policies, per project:

| Policy | Implementation |
|---|---|
| `none` | Container on an internal Docker network. `HTTP_PROXY`/`HTTPS_PROXY` point at the orchestrator's CONNECT proxy, which allows only the Anthropic API and the project's git remote host. |
| `allowlist` | As above plus user-specified domains (e.g. `registry.npmjs.org`). |
| `open` | Default bridge network, no proxy. |

`none` is labelled in the UI as *"nothing beyond what the agent itself needs"* rather than
literally nothing, because a container with zero egress cannot reach the model and the setting
would be a trap. The proxy logs every allowed and denied CONNECT into the run's activity stream at
verbose level, which makes "the install failed because the network policy blocked it" a visible
fact instead of a mystery.

> **POC amendment (owner's decision, post-S39).** The **workspace default is `open`**, not
> `allowlist` (migration `0005_open_network_default.up.sql`).
>
> Why: `allowlist` is only as useful as the list, and `repos.network_allowlist` starts empty with
> no reason for a new project to have filled it in. The shipped default therefore resolved to
> "Anthropic, claude.ai and the git remote" — no package registry, no docs, no toolchain
> download — and presented itself as a proxy denial rather than as a setting to change.
>
> What it costs: an `open` container reaches every host the laptop reaches. That removes the
> bound on both directions — exfiltration (a prompt injection has somewhere to send the checkout)
> and supply-chain pull (an unvetted registry has a way in).
>
> **The feature is not removed.** `none`, `allowlist`, the CONNECT proxy, the egress relay and the
> per-decision activity logging all still work and still enforce whenever a project selects them,
> per project or as a new workspace default. Only the default moved, and moving it back is one
> migration. The MCP endpoint is reachable on both paths and both are proved by docker-tagged
> tests (`TestS21MCPReachableUnderPolicyNone`, `TestS21MCPReachableUnderPolicyOpen`): proxied runs
> dial the relay, `open` runs dial `host.docker.internal:<proxy port>`.

## D-11 — Wiki sync is import-only

Repo instruction files (`AGENTS.md`, `CLAUDE.md`, `.cursor/rules/*`, `.github/copilot-instructions.md`,
`README.md`, `docs/**`) are detected at repo connect and offered as seed wiki pages with proposed
`agent_scope` values, re-runnable later. Nothing is ever written back to the repo.

Consequence to handle honestly: Claude Code inside the container will *also* read the repo's own
instruction files off the checkout. That is not a conflict to suppress — it is context the user
must be able to see. The `repofiles` ContextProvider enumerates them and lists them in the run's
Context panel alongside wiki pages, marked `repo file`. When an imported wiki page and a live repo
file both steer a run, the Context panel says so.

## D-12 — Elicitation, approvals, steering and progress all ride one mechanism: a Lexicode MCP server

The orchestrator hosts an MCP server over HTTP on the host; containers reach it at
`http://host.docker.internal:<port>/mcp/<run-token>`. It exposes:

| Tool | Purpose |
|---|---|
| `ask_human` | Structured clarification (question, 2–4 options, multiSelect). Parks the run in `needs input`. |
| `set_step` | Writes the mutable current-step line (UI spec §5.7). |
| `propose_wiki_page` | Agent proposes a page or edit. Never auto-written (brief §6.5). |
| `check_criterion` | Marks an acceptance criterion met/unmet with a note. |
| `request_approval` | Backing tool for `--permission-prompt-tool`; renders as an inline approval row. |

Every call blocks until a human (or an autonomy-level rule) responds, which is exactly the
semantics the run state machine needs: an unanswered `ask_human` *is* the `needs input` state.
Run tokens are per-run, single-use-scoped, and revoked when the run ends.

Steering is the reverse direction: queued messages are written to the CLI's stdin between tool
calls, matching the brief's "queue, don't interrupt."

## D-13 — Events are persisted before they are dispatched

Every external and internal event is written to the `events` table first, then published on the
in-process bus. This single decision makes three separately-expensive features nearly free:

- **Backtest** (brief §6.6) is replaying stored events through a rule's matcher with actions
  disabled.
- **The audit log** and the trigger firing history read the same rows.
- **Crash recovery** can re-dispatch events that were persisted but not yet processed.

## D-14 — The run scheduler is kernel-owned, not module-owned

Concurrency caps, the enforcing WIP governor on the `running` column, budget ceilings, the run
state machine and the five-layer loop guard are cross-cutting invariants. If a module could spawn a
run directly, every one of them becomes advisory. Modules request runs; the kernel decides.

## D-15 — Resolutions of the design's open questions (spec §12)

These had to be settled before the schema could be written.

| Question | Resolution |
|---|---|
| Can a run be free-floating? | Yes. `runs.ticket_id` is nullable. The run list is the canonical home; the ticket stream is a filtered view. A trigger may run an agent with no ticket. |
| What happens to a run when its ticket is deleted? | Tickets soft-delete (`archived_at`). Deleting a ticket with active runs cancels them with reason `ticket archived`, after a typed confirmation naming the count. Historical runs keep `ticket_id` and render "archived ticket". |
| Multi-agent on one ticket | Not modeled in V1 (spec agrees). One `delegate_agent_id`. Runs already carry `agent_id`, so an "attempts" feature later is additive. |
| Wiki ↔ repo sync | Import-only. See D-11. |
| Where does human code review live? | GitHub. Lexicode links out, renders PR check status and diff *stat*, and does not build an in-app review surface. Agent-authored reviews are posted to GitHub via the forge port. |

## D-16 — Secrets at rest

AES-256-GCM with a key from `~/.lexicode/master.key` (0600, generated on first run). Secret values
are write-only through the API: they can be set and cleared, never read back. The UI shows
`set · 4 days ago` and a Replace button. Secret *names* are listed; values never leave the process
except into a container's environment.
