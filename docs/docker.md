# Docker

Every agent run happens inside a container. Lexicode talks to your local Docker daemon; there is
no other runtime and no "run it on the host" mode.

## What you need

- **Docker Desktop, or Docker Engine.** Lexicode uses the official Docker client with API
  version negotiation, so a reasonably current daemon works; it is developed and tested against
  Engine 29.x. `lexicode doctor` pings the daemon and tells you if it cannot reach it.
- **~5 GB of free disk.** The agent base image is about 1.5 GB, plus a workspace volume per run
  and the image layer cache. `doctor` fails below 5 GiB.
- Nothing else. Lexicode needs no privileged mode, no docker-in-docker, no compose file.

Lexicode finds the daemon the way every Docker client does: `DOCKER_HOST` if it is set, the
platform socket otherwise. Override it with `--docker-host` or `docker_host:` in `config.yaml`
— colima, a remote engine, or a rootless socket all work as long as the endpoint is reachable.

## The agent image

The image is **built by Lexicode, on demand, from a Dockerfile embedded in the binary.** You do
not pull it and you do not maintain it.

It is tagged `lexicode/agent-base:<first 12 of the Dockerfile's sha256>`, so the tag changes
exactly when the Dockerfile does — upgrading Lexicode rebuilds the image only if the image
actually changed, and two versions with the same base can share one build.

Inside: Debian bookworm-slim, git, curl, jq, ripgrep, build-essential, a pinned Node 22, the
`@anthropic-ai/claude-code` CLI, and a non-root `agent` user (uid 1000) whose home for the run is
`/workspace`.

The first run on a machine builds it. That takes a few minutes, it is a normal `docker build`
with normal caching, and it shows up as the "image" line of the run's provisioning checklist —
not as a mystery hang. Later runs reuse it and start in about a second.

### Bringing your own image

A project's repository row carries an optional `image_ref`. When it is set, Lexicode pulls that
image instead of building the built-in one — useful when your agents need a toolchain the base
image does not carry (a JVM, a Python environment, your own CLI).

Whatever you point it at must have `claude` on `PATH`, `git`, and a writable `/workspace`. The
easiest correct way is to derive from the base image:

```dockerfile
FROM lexicode/agent-base:8e1048bd9023
USER root
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv \
    && rm -rf /var/lib/apt/lists/*
USER agent
```

**There is no UI or API for `image_ref` yet** — it is a column on the `repos` row and today it
has to be set in the database. That is a known gap, and the acceptance harness sets it the same
way.

A per-repository **setup script** is the softer version of the same idea: it runs inside the
container after the clone, before the agent starts, with its output streamed into the run's
provisioning checklist. `npm ci`, `go mod download`, `pip install -r requirements.txt`.

## What a run's container looks like

Per run, one container, destroyed when the run ends:

- **Read-only root filesystem**, with `/tmp` as a 1 GB tmpfs and `/workspace` as an anonymous
  volume. Nothing an agent writes outside the workspace survives, and nothing it writes reaches
  your machine.
- **Non-root**: uid 1000, no added capabilities, no privileged mode, no socket mounts. An agent
  in a Lexicode container cannot start another container.
- **A wall-clock limit** (default one hour) and a **step cap** (default 200 actions), both per
  agent; a run that hits either is stopped and says which. The container also accepts CPU,
  memory and pid caps, but nothing sets them yet — today a run inherits the daemon's defaults
  for those three. Known gap.
- **Network** per the project's policy — see [network-policy.md](network-policy.md).
- **Labelled** `lexicode.agent=<agent id>`, with a per-container instance id, and with
  `lexicode.owner=<this workspace>`, which is what lets the sweeper tell its own containers from
  yours — and from another Lexicode's.

The workspace is not a `git clone` — the orchestrator materializes its own files first
(`.claude/settings.json`, `.lexicode/mcp.json`, the prompt, a commit-msg hook) and then does
`git init` + a shallow `fetch` + `checkout`, because `clone` refuses a non-empty directory.

> **Note.** Those two directories sit in the workspace root and are *not* excluded from git. An
> agent that runs `git add -A` will commit them — including `.lexicode/mcp.json`, which holds
> that run's MCP token. Until the sandbox excludes them itself, say so in the agent's directive
> ("never commit `.lexicode/` or `.claude/`") or add them to the repository's `.gitignore`.

## Cleanup

Containers are removed when their run ends. A crash can still leave one behind, so a sweeper
runs at boot and hourly: it lists the containers this workspace created — they carry an owner
label derived from its data directory — looks up the run each belongs to, and removes any whose
run is finished or gone. Containers that are not Lexicode's are never touched, and neither are
another Lexicode's: a second instance on the same machine (an acceptance run, a demo workspace,
a second data directory) has its own owner, and each sweeps only its own containers even though
the other's runs are unknown to it.

Images are not swept. `docker image prune` if old `lexicode/agent-base:*` tags accumulate after
upgrades.

## When Docker is down

Lexicode keeps serving. A module that fails to start degrades rather than aborting boot: the
docker module records the daemon's error as its degraded reason, the dashboard says so, and the
rest of the product — board, tickets, wiki, triggers, history — works normally. What does not
happen is the whole thing refusing to load because a daemon is not running.

A run started while the daemon is down fails at its first provisioning step, with the daemon's
own message on the run. Start Docker and re-run it; `lexicode doctor` is the fastest way to
confirm the daemon is back.
