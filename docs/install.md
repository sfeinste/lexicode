# Install

Lexicode is one binary. It serves its own dashboard, stores everything in one directory, and
runs agents in Docker containers on the same machine.

**You need:** a Mac or Linux box, Docker running, and a Claude Code subscription or API key.
Nothing else — no database to install, no reverse proxy, no Node.

---

## 1. Get the binary

```sh
curl -fsSL https://<your-release-host>/install.sh | sh
```

The script detects your platform, downloads the matching binary, **verifies its SHA256** against
the published `SHA256SUMS`, and installs it to `/usr/local/bin` (or `~/.local/bin` if that is not
writable).

There is no public release host yet. Until there is, build and install from a checkout:

```sh
make release                                            # dist/ + SHA256SUMS
LEXICODE_BASE_URL="file://$PWD/dist" sh scripts/install.sh
```

`LEXICODE_BASE_URL` works for any host that serves the files `make release` produced, so an
internal artifact store or an S3 bucket needs no other change:

```sh
curl -fsSL https://artifacts.example.com/lexicode/install.sh \
  | LEXICODE_BASE_URL=https://artifacts.example.com/lexicode sh
```

Other knobs: `LEXICODE_VERSION` pins the version segment of the filename, `LEXICODE_BIN_DIR`
chooses the install directory.

### Or build it yourself

```sh
git clone <this repo> && cd lexicode
make build          # builds the web UI, then ./lexicode with the UI embedded
./lexicode version
```

Go 1.25+ and Node 20.19+ (Vite 7) are needed to build; neither is needed to run.

---

## 2. Check the machine

```sh
lexicode doctor
```

```
lexicode doctor — 1.0.0

  ok    Data dir         /Users/you/.lexicode exists and is writable
  ok    Disk space       58.2 GiB free on /Users/you/.lexicode
  ok    Port 7717        free
  ok    Proxy port 7718  free
  ok    Docker           daemon reachable
  warn  Agent image      lexicode/agent-base:8e1048bd9023 not built yet
                         fix: nothing to do — the first run builds it from the embedded
                              Dockerfile (a few minutes)
  warn  Claude token     no database yet at /Users/you/.lexicode/lexicode.db
                         fix: run `lexicode serve` once; then paste `claude setup-token`
                              output into Settings → Credentials
```

`doctor` exits non-zero if any check **fails**; warnings are things that resolve themselves.
Every failure prints the fix on the next line. See [troubleshooting.md](troubleshooting.md).

---

## 3. Start it

```sh
lexicode serve
```

```
Lexicode is listening on http://127.0.0.1:7717/
```

A browser tab opens on first boot (`--open-browser=false` to stop that). The first screen asks
you to create the owner account: email, name, password. There is no hosted anything and no
account to sign up for — that user lives in your own database.

Want to look around before connecting a real repository?

```sh
lexicode serve --demo --data-dir ~/.lexicode-demo
```

`--demo` seeds a populated workspace — a board with eleven tickets, two agents, three runs (one
of them parked on a question), wiki pages including an agent proposal, a trigger and an inbox —
and prints the login it created. It only ever touches an **empty** database; pointed at real
data it prints a note and changes nothing.

---

## 4. Give it a Claude credential

Agents cannot run without one. See [oauth-token.md](oauth-token.md) — it takes one command and
one paste.

---

## 5. Connect a repository

See [first-project.md](first-project.md) for the whole path from empty workspace to a merged
pull request.

---

## Where things live

Everything is under the data directory, `~/.lexicode` by default:

| Path | What it is |
|---|---|
| `lexicode.db` | SQLite. Tickets, runs, wiki, triggers, audit — all of it. |
| `master.key` | Encrypts the secret store. Mode `0600`, and Lexicode refuses to boot if it is looser. |
| `logs/lexicode.log` | The server log. |
| `config.yaml` | Optional; see below. |

Back up the data directory and you have backed up Lexicode. Delete it and you are back to a
fresh install.

## Configuration

Three sources, in order of precedence: **command-line flags**, then `LEXICODE_*` environment
variables, then `config.yaml` in the data directory.

| Flag | Env | Default | What it does |
|---|---|---|---|
| `--host` | `LEXICODE_HOST` | `127.0.0.1` | Interface the dashboard binds. |
| `--port` | `LEXICODE_PORT` | `7717` | Dashboard and API port. |
| `--proxy-port` | `LEXICODE_PROXY_PORT` | `7718` | Egress proxy for sandboxed containers ([network-policy.md](network-policy.md)). |
| `--data-dir` | `LEXICODE_DATA_DIR` | `~/.lexicode` | Everything above. |
| `--docker-host` | `LEXICODE_DOCKER_HOST` | from the environment | Docker endpoint override. |
| `--github-base-url` | `LEXICODE_GITHUB_BASE_URL` | `api.github.com` | GitHub Enterprise. |
| `--log-level` | `LEXICODE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `--open-browser` | `LEXICODE_OPEN_BROWSER` | `true` | Open a tab when ready. |
| `--config` | `LEXICODE_CONFIG` | `<data-dir>/config.yaml` | Config file path. |

`config.yaml` uses the same names in snake_case:

```yaml
host: 0.0.0.0
port: 8080
proxy_port: 8081
data_dir: /srv/lexicode
log_level: info
```

Binding `0.0.0.0` puts the dashboard on your network. Sessions are cookie-based and there is no
TLS in the binary: put it behind a reverse proxy you trust, or leave it on loopback.

## Commands

```
lexicode serve      run the HTTP server and the orchestrator
lexicode doctor     check Docker, credentials, ports and disk, and print the fix for each failure
lexicode migrate    apply pending database migrations and exit
lexicode version    print the version and exit
```

Migrations run automatically at boot; `lexicode migrate` exists for the case where you want to
apply them separately, before starting.

## Upgrading

Replace the binary and restart. Migrations apply at boot and are forward-only. A new binary may
carry a new agent base image — the first run after an upgrade rebuilds it, which takes a few
minutes and shows up as the "image" step in the run's provisioning checklist.
