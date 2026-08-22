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
`@anthropic-ai/claude-code` CLI, and a non-root `agent` user (uid 1000). The image declares
`USER agent`, but Lexicode overrides it at container-create time and runs as root — see
[what a run's container looks like](#what-a-runs-container-looks-like).

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
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv \
    && rm -rf /var/lib/apt/lists/*
```

This is the *cache-it-once* version of the same idea, and it is worth doing for a heavy
toolchain. It is no longer the only way to get one: the container is writable and runs as root,
so a setup script or the agent itself can `apt-get install` or download a tarball. That costs the
time on every run, which a derived image does not.

**There is no UI or API for `image_ref` yet** — it is a column on the `repos` row and today it
has to be set in the database. That is a known gap, and the acceptance harness sets it the same
way.

A per-repository **setup script** is the softer version of the same idea: it runs inside the
container after the clone, before the agent starts, with its output streamed into the run's
provisioning checklist. `npm ci`, `go mod download`, `pip install -r requirements.txt`.

## What a run's container looks like

Per run, one container, destroyed when the run ends:

- **Writable root filesystem**, running as **root** (uid 0), with `/workspace` as an anonymous
  volume and `$HOME` at `/root`. A run can `apt-get install`, `npm install -g`, or drop a
  toolchain into `/usr/local` — which is the point. Nothing outside the container is touched:
  no host path is bind-mounted, no capabilities are added, no privileged mode, no socket
  mounts, so an agent still cannot start another container or see your filesystem. Everything
  it writes dies with the container.
- **A wall-clock limit** (default one hour) and a **step cap** (default 200 actions), both per
  agent; a run that hits either is stopped and says which.
- **CPU, memory and pid caps**: 2 CPUs, 4 GiB and 512 pids by default. These are stability
  limits, not security ones — a runaway agent should not take your laptop down with it — and
  they stay in place regardless of anything else on this list.
- **Network** per the project's policy, defaulting to `open` — see
  [network-policy.md](network-policy.md).
- **Labelled** `lexicode.agent=<agent id>` and with the orchestrator's instance id, which is what
  lets the sweeper tell its own containers from yours.

The workspace is not a `git clone` — the orchestrator materializes its own files first
(`.claude/settings.json`, `.lexicode/mcp.json`, the prompt, a commit-msg hook) and then does
`git init` + a shallow `fetch` + `checkout`, because `clone` refuses a non-empty directory.

Those two directories sit in the workspace root, and `.lexicode/mcp.json` holds the run's live
MCP token, so the clone step writes both into `.git/info/exclude` before anything else runs. A
`git add -A` — including the one on the failure-artifact path — cannot stage either of them, and
because it is `exclude` rather than `.gitignore` it never shows up in a diff or a pull request.
This is a correctness control, not a posture setting: it stays whatever else changes.

## Where the repository credential is — and is not

**The container never holds your GitHub token.** Not in an environment variable, not in
`.git/config`, not in `git remote -v`. This matters more than it used to: the container runs as
root with unrestricted egress, so anything readable inside it is effectively exfiltratable.

Reading a private repository does need the token, so the clone fetches with a URL of the form
`https://x-access-token:<token>@…`. Three things bound its life:

1. The URL is passed in the **clone command's own environment**, not the container's. Every later
   process — the agent included — starts with an environment that never had it.
2. As soon as the fetch succeeds, and in the same step, `origin` is repointed at the **tokenless**
   URL. Nothing persists in `.git/config`.
3. Before that repoint, the step fetches remote-tracking refs for every branch, so a run whose job
   is "address the review on branch X" has X locally without needing the network again.

The consequence is that **an agent cannot push, or reach the remote at all, after provisioning.**
It commits locally, on the branch its workspace is already on; the run's prompt says so. When the
agent process exits, Lexicode runs one command inside the container that commits anything still
uncommitted, pushes the branch, and opens the pull request from it. That command — and only that
command — gets the token, through git's config-via-environment
(`GIT_CONFIG_KEY_n=http.extraheader`), so it is never in a command line `/proc` would expose and
never written back into the repository.

Two things follow that are worth knowing:

- **Committing is how work leaves the container.** A run that ends with uncommitted changes has
  Lexicode commit them as a single `wip:` commit so nothing is lost, but your agent's own commits
  are what a reviewer reads.
- **The run says what actually happened.** A push that failed is reported as a push that failed,
  with the error, and no "partial work pushed" claim is made unless a branch really moved.

The agent's model credential (`CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`) and the run's MCP
token are a different story: those are *in* the container, because the agent has to use them. They
are scoped to the run and revoked at teardown.

### On the container being unrestricted

This is a deliberate choice for a local proof-of-concept, and it is a real reduction in
isolation: the hardened version ran non-root on a read-only rootfs, so nothing an agent wrote
outside `/workspace` and `/tmp` survived and it could not modify a binary or a library anywhere.
It also could not install anything, which made a missing toolchain a dead end rather than a
`setup_script` line — so the hardening went.

Two things to keep in mind before relaxing anything further. Root inside a container is root on
any host path bind-mounted into it, so a future feature that mounts a host directory would be
handing the agent write access as root to those files. And container isolation was never the
only control: agent tool permissions, autonomy gates, the absent merge capability, loop
protection and budgets all sit above the container and are untouched.

The posture is also why the repository credential left the container entirely (see [where the
repository credential is](#where-the-repository-credential-is--and-is-not)). A secret is only as
confined as the process that can read it, and under this posture that is "not at all" — so the
answer was to stop putting it there.

The full record — what was removed, what it protected against, and how to put each piece back —
is the "Container posture" block in `internal/module/docker/sandbox.go`, with the decision itself
noted under D-7 and D-10 in `plan/00-decisions.md`.

## Cleanup

Containers are removed when their run ends. A crash can still leave one behind, so a sweeper
runs at boot and hourly: it lists containers labelled with this orchestrator, looks up the run
each belongs to, and removes any whose run is finished or gone. Containers that are not
Lexicode's are never touched.

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
