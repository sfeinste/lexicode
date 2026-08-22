# Troubleshooting

Start here:

```sh
lexicode doctor
```

It checks the data directory, disk space, both ports, the Docker daemon, the agent image, the
Claude credential and every connected repository's GitHub token — and prints the fix under every
failure. It exits non-zero if anything failed, so it works in a script.

```
lexicode doctor — 1.0.0
data dir: /Users/you/.lexicode

  ok    Data dir         /Users/you/.lexicode exists and is writable
  ok    Disk space       58.2 GiB free on /Users/you/.lexicode
  ok    Port 7717        free
  ok    Proxy port 7718  free
  FAIL  Docker           docker daemon unreachable: Cannot connect to the Docker daemon at
                         unix:///var/run/docker.sock. Is the docker daemon running?
                         fix: start Docker Desktop (or `sudo systemctl start docker`), then re-run.
                              If the daemon runs somewhere else, set docker_host in config.yaml
                              or DOCKER_HOST.
```

The rest of this page is the four failures that actually happen.

---

## Docker unreachable

**Looks like:** `doctor` fails on Docker. Runs fail at the very first provisioning step with
"docker daemon unreachable". The dashboard shows the docker module degraded. Everything that is
not a run keeps working.

**Fix:**

1. Start it. `open -a Docker` on a Mac, `sudo systemctl start docker` on Linux. Wait for the
   daemon to actually be up, then `lexicode doctor` again.
2. If your daemon is not at the default socket — colima, Rancher Desktop, a remote engine,
   rootless Docker — tell Lexicode where it is:
   ```sh
   lexicode serve --docker-host unix:///Users/you/.colima/default/docker.sock
   # or: docker_host: unix://... in config.yaml, or DOCKER_HOST in the environment
   ```
   `docker context ls` prints the endpoint your CLI is using; that is the value you want.
3. Permission denied on the socket, on Linux: your user is not in the `docker` group.
   `sudo usermod -aG docker $USER`, then log out and back in.

Runs that failed this way are safe to re-run. Nothing was created.

---

## Token expired

Two different tokens expire, and they fail differently.

### The Claude credential

**Looks like:** runs fail early with an authentication error from the provider, on the run's
activity stream. `doctor` says the token is present and well-formed, because it does not spend
an API call to check — the shape is right, the token is just no longer valid.

**Fix:** `claude setup-token` again and paste the new value into the avatar menu → Workspace
settings → Credentials. Runs in flight keep the credential they started with; the next run
picks up the new one. See [oauth-token.md](oauth-token.md).

### The GitHub token

**Looks like:**

```
  FAIL  GitHub acme/payments   the token was rejected (401): ...
                               fix: the token expired or was revoked — issue a new one and
                                    reconnect the repository
```

Or, at connect time: *the token is missing the "repo" scope*. Or, in a run: the clone fails
authentication, or the pull request cannot be opened.

**Fix:** issue a fresh token ([first-project.md](first-project.md) §1), then reconnect the
repository with it in project settings → Repository. Reconnecting rotates the stored token and
touches nothing else — your tickets, runs, wiki and history stay exactly as they are.

Note that a **fine-grained token that never had the right permission** looks the same as an
expired one from the outside. `doctor` names the failing probe, which usually tells you which
permission is missing.

---

## Rate limited

**Looks like:** the poller stops producing events, the log carries `forge rate limit exhausted;
resets at ...`, the github module goes degraded, and `doctor` reports the repository as a
warning rather than a failure.

**What Lexicode does on its own:** backs off exponentially, sleeps past the reset when the reset
is close enough to be worth waiting for, and keeps serving. Listing requests carry an ETag, so
an unchanged resource costs no rate limit at all. Nothing is lost — polling is cursor-based, so
it resumes exactly where it stopped.

**Fix, if it keeps happening:**

- Raise the poll interval. Workspace settings → poll interval; the floor is 10 seconds and the
  default is 30. On a busy repo, 60 is plenty.
- Check whether one token is doing several jobs. GitHub's rate limit is per token, so a PAT
  shared between Lexicode, CI and a script is three consumers of one budget.
- Disconnect projects you are not using. Each connected repository is one poll worker.

---

## Image build failure

**Looks like:** the first run on a machine sits on the "image" step of the provisioning
checklist and then fails; the checklist row carries the build output. Common causes: no network
to `nodejs.org` or the Debian mirrors, a full disk, or an apt mirror having a bad day.

**Fix:**

1. `lexicode doctor` — the disk check is the usual answer. The build needs a few GB of headroom
   for layers.
2. Build it yourself to see the real output:
   ```sh
   docker build -t lexicode/agent-base:<tag> -f internal/module/docker/Dockerfile internal/module/docker
   ```
   `doctor` prints the exact tag it is looking for. Once the tag exists locally, Lexicode uses
   it and stops trying to build.
3. Behind a corporate proxy or a TLS-inspecting middlebox, Docker's own build needs the proxy
   configured — `~/.docker/config.json` `proxies`, or `HTTP_PROXY`/`HTTPS_PROXY` in the daemon's
   environment. This is Docker configuration, not Lexicode configuration; Lexicode's network
   policy governs the *agent's* traffic, not the build's.
4. If you cannot build in this environment, build the image elsewhere, push it to a registry
   your machine can reach, and set the repository's `image_ref` to it (see
   [docker.md](docker.md)).

---

## Other things

**Port already in use.** `doctor` distinguishes "held by a Lexicode server that is already
running" from "held by something else" and names the flag to move it: `--port`, `--proxy-port`.

**"Setup required" every time.** Your data directory changed between boots — a different
`--data-dir`, or a `config.yaml` you did not expect. The first log line names the data directory
and the config file it read.

**Lexicode refuses to boot: master key permissions.** The secret store's key file must be mode
`0600`. `chmod 600 ~/.lexicode/master.key`.

**A run is stuck in "provisioning".** Look at the checklist — one of the steps is running and
its output is streaming. Image builds are minutes; a setup script can be too. If the
orchestrator was restarted mid-run, boot recovery either reattaches to the surviving container
or fails the run with "orchestrator restarted", preserving whatever the branch already had.

**A trigger does not fire.** Its history explains itself. Eight outcomes are recorded, including
`no_action` ("conditions not met", "actor suppressed"), `debounced`, `superseded`,
`loop_stopped` and `budget_exceeded`. If nothing appears in the history at all, the event never
matched the WHEN — check the rule's event kind and activity type, and remember that the poller
emits **nothing** on its first pass after a repository is connected.

**Everything is fine but nothing runs.** Check the run's hold reason on the queue: concurrency
caps (per agent, and workspace-wide), the running column's WIP limit, and the daily budget
ceiling all hold runs queued, and each says which limit in words.

**Where the log is.** `<data-dir>/logs/lexicode.log`. `--log-level debug` for more. Credentials
are registered with a redactor before they are ever used, so tokens do not appear there.
