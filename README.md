# Lexicode

**A self-hosted workspace where the tickets, the docs, the agents, the triggers and the run logs
are one thing.**

One binary. You run it on your own box, point it at a GitHub repository, write tickets and a
roster of agents, and wire the output of one agent into the input of the next. Agents work in
disposable Docker containers running Claude Code and hand back a pull request or a wiki page. A
ticket can travel from *written* to *reviewed PR waiting for you* without anyone touching a
keyboard — and every step stays visible and interruptible.

The thesis in one line: **the scarce resource is not agent capability, it is human attention.**
Everything below is downstream of that.

```
   Ticket written ──delegate──▶ Agent run ──▶ PR opened
   (human or agent)             (container)   Wiki page proposed
        ▲                                     Question / blocked ──▶ NEEDS YOU
        │                                              │
        └── Trigger fires ◀── GitHub event ◀───────────┘

                        Human merges. Always.
```

---

## Quickstart

```sh
# 1. install (see docs/install.md for a real release host)
make release && LEXICODE_BASE_URL="file://$PWD/dist" sh scripts/install.sh

# 2. check the machine — Docker, ports, disk, credentials
lexicode doctor

# 3. run it
lexicode serve                 # http://127.0.0.1:7717
```

Create the owner account on the first screen, paste a `claude setup-token` into
Settings → Credentials, connect a repository, write a ticket, press **D**.

Just want to look around?

```sh
lexicode serve --demo --data-dir ~/.lexicode-demo
```

A populated workspace: a board with eleven tickets across all six categories, two agents, three
runs (one parked on a question), wiki pages including an agent proposal, a trigger with history,
and an inbox with two flavours of "needs you". It prints the login it created and only ever
seeds an empty database.

---

## What it does

- **A board** whose columns you rename freely — every column carries a fixed *category*, and
  automations key off the category, never the name.
- **Agents** with a directive (guidance), permissions and a network policy (enforcement, checked
  in the adapter, never in a prompt). Dragging a card never starts a run; starting one is always
  an explicit act.
- **Runs in real containers**: read-only rootfs, non-root, per-run credentials, a provisioning
  checklist instead of a spinner, a live activity stream, and steer / stop / take-over at any
  point. A failed run still leaves an artifact.
- **Triggers** with a generated editor, a backtest that replays against real history before you
  enable anything, and eight named outcomes per firing — including the ones where nothing
  happened.
- **Loop protection** in five layers, on by default, with the causal chain rendered rather than
  a throttling notice. [docs/loop-protection.md](docs/loop-protection.md)
- **A wiki** whose pages are scoped, owned and expiring agent instructions, with a per-run
  context panel that shows exactly which pages steered a run, why each loaded, and what it cost
  in tokens.
- **One inbox**, four distinct reasons a run needs you, never one "waiting" badge.
- **No merge path.** Not a permission that is off — an absent capability. The forge port has no
  merge method, and review submission rejects `APPROVE` unconditionally.

---

## Measured timings

Brief §10 sets two goals. Both are measured by `scripts/s39-acceptance.sh`, which drives the
whole product — real binary, real Docker containers, real git over HTTP, the real MCP server,
the real GitHub poller, the real trigger engine and the real loop guard — through the eight-step
chain and prints the numbers. Last measured on 2026-08-22, Apple Silicon laptop, Docker 29.7.2:

| Goal | Target | Measured | What was measured |
|---|---|---|---|
| Connect repo → first run in flight | < 5 min | **0.55 s** | Wall clock from `POST /projects/{key}/repo` to the run's state reaching `running`, which the scheduler sets only once the container is up and the agent process has started. Includes connecting, writing the ticket and its two acceptance criteria, delegating, admission, container create, clone, branch and launch. |
| Six-step chain configured | < 10 min | **2 ms** | Wall clock of the four `POST /projects/{key}/triggers` calls that create the chain, with the request bodies the trigger editor sends. |
| *(aside)* Whole eight-step chain | — | **42 s** | Ticket → PR → review → address → CI failure → re-review → loop stopped at depth 3. Bounded below by the poller: four events at its 10-second floor. |
| *(aside)* Agent image build | — | **56 s** | `docker build --no-cache` of the embedded Dockerfile, warm Debian base and a fast link. One time per machine per image change. |

**Read these honestly.** They measure the *product's* side of each goal, not a human's. The
five-minute goal is about ceremony — how much the product makes you do, and how long it takes
once you have done it — and the product's share of it is sub-second; the rest is you typing.
The ten-minute goal is about configuring four rules; the API calls take milliseconds, and what
the goal is really asking is whether the trigger editor lets a person express the chain in ten
minutes, which an API-call timer cannot answer. The end-to-end 42 seconds is the number with the
most content in it: it is a real chain of six runs in six containers, gated by a real poller.

The agent in the acceptance run is a scripted stand-in, not a language model. It does real work
through real seams — it clones, commits and pushes over git, calls the real MCP server, submits
reviews through the real forge adapter — but it does not think, so run durations here are floor
values for the orchestration, not estimates of a real agent's work.

---

## Documentation

| Doc | What is in it |
|---|---|
| [docs/install.md](docs/install.md) | Install, configure, upgrade. Where data lives; every flag. |
| [docs/first-project.md](docs/first-project.md) | Empty workspace → reviewed pull request, including the six-step chain. |
| [docs/oauth-token.md](docs/oauth-token.md) | The Claude credential: one command, one paste, and how it is stored. |
| [docs/docker.md](docs/docker.md) | Requirements, the agent image, what a run's container looks like, cleanup. |
| [docs/network-policy.md](docs/network-policy.md) | `none` / `allowlist` / `open`, and the egress proxy that enforces them. |
| [docs/loop-protection.md](docs/loop-protection.md) | The five layers, the escape hatch, and how to read a stopped loop. |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Docker unreachable, token expired, rate limited, image build failure. |

---

## Architecture

The design and the plan live in [`design/`](design) and [`plan/`](plan). Read them in this
order:

| Doc | What it is |
|---|---|
| [design/product-brief.md](design/product-brief.md) | What gets built and why. The seven decisions everything hangs off. |
| [plan/00-decisions.md](plan/00-decisions.md) | Every technical decision, with rationale. |
| [plan/01-architecture.md](plan/01-architecture.md) | Kernel/module design, ports, execution lifecycle, loop protection. |
| [plan/02-data-model.md](plan/02-data-model.md) | The SQLite schema and the invariants it enforces. |
| [plan/03-contracts.md](plan/03-contracts.md) | Port interfaces, the container protocol, the MCP tool surface, the HTTP API. |
| [plan/04-implementation-plan.md](plan/04-implementation-plan.md) | The 39 stories that built it, and the traceability table. |

In one paragraph: a **kernel** owns the store, the event bus, the run scheduler and the
loop-protection guard, and knows nothing about GitHub, Docker or Claude. Everything external is
a **module** behind a frozen **port** — `ForgeProvider`, `Sandbox`, `AgentRuntime`,
`EventSource`, `TriggerAction`, `ContextProvider`, `Notifier`, `CredentialSource` — and
`cmd/lexicode` is the only place that knows which modules exist. Enforcement lives in the
adapters, never in a prompt. The kernel may not import a layer above it, and a test walks the
real import graph to prove it.

---

## Development

```sh
make check     # build, vet, lint, test, frontend typecheck, frontend tests — the definition of done
make build     # web/dist + ./lexicode with the version injected
make dev       # Go server + Vite dev server together
make release   # darwin/linux x amd64/arm64 into dist/, with SHA256SUMS
```

End-to-end acceptance, both against a full fixture stack (real Docker, a scripted `claude` baked
into a derived image, a local fake GitHub serving REST *and* git smart-HTTP):

```sh
scripts/s24-e2e.sh          # the first end-to-end milestone: delegate → container → PR, plus intervention
scripts/s24-e2e.sh hold     # the same stack, left running for a browser walkthrough
scripts/s39-acceptance.sh   # the brief §3 chain, all eight steps, with the timings above
```

Both need Docker and a few minutes. Shared fixture code is in [`e2e/harness`](e2e/harness).

### CI

[`.github/workflows/check.yml`](.github/workflows/check.yml) runs the whole test suite on every
pull request — `make check` on Linux and macOS, plus the `-tags docker` sandbox tests on Linux —
and funnels the lot into one job, **`tests passed`**, which is red unless every other job is
green.

That job is the check branch protection requires, so a pull request with a failing test cannot be
merged. The rule lives in repository settings rather than in the tree; apply it, or re-apply it
after an edit, with:

```sh
scripts/setup-branch-protection.sh          # needs `gh`, authenticated with admin on the repo
```

It is idempotent, and the rule it applies is [`.github/rulesets/require-tests-to-merge.json`](.github/rulesets/require-tests-to-merge.json),
which can also be imported by hand under *Settings → Rules → Rulesets → Import a ruleset*.
