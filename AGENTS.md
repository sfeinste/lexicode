# Working in this repository

Two very different agents read this file, and the rules are not the same for both. Find yourself
first.

- **A Lexicode run** — you are a coding agent working inside a Lexicode container on a ticket,
  and something else will review and merge your work. Read
  [Working inside a Lexicode run](#working-inside-a-lexicode-run). It is the section that
  applies to you; the build-time conventions below it are history, not instructions.
- **A build agent** — you were given one story out of `plan/04-implementation-plan.md` by a lead
  agent in a local checkout. Read [Building Lexicode itself](#building-lexicode-itself).

Both of you need [The toolchain and the commands](#the-toolchain-and-the-commands), and both of
you are bound by [Things that bite](#things-that-bite-already-solved).

## Working inside a Lexicode run

**Commit your work. Locally, on the branch the workspace is already on.** That is how your work
leaves the container — it is the only way. Lexicode pushes the branch after your process exits
and opens the pull request from it; the review and the merge are somebody else's job, as they
always were.

Do not try to push. You cannot: the container holds no credential for the repository (`origin` is
tokenless from the moment the clone finishes), so `git push` fails. That is by design, not a
misconfiguration to work around — the container runs as root with open egress, and a token
readable in it is a token that can leave.

Two consequences worth knowing:

- **Ending with uncommitted changes is a bad outcome, not a neutral one.** Lexicode will commit
  what it finds as a single `wip:` commit so nothing is lost, but a machine-written blob is a
  poor substitute for your own commits with your own messages. Commit as you go.
- **Do not `--no-verify` your way past the commit hook.** It appends the `Lexicode-Run:` trailer
  that loop protection reads to attribute a push to the run that caused it. Commits without it
  still land, and Lexicode records a warning naming them — but the guard gets weaker, which is
  the kind of failure nobody notices until it matters.

Nothing else in this file overrides that. In particular, the "do not commit" line under
[Building Lexicode itself](#building-lexicode-itself) is about a lead agent's local checkout and
does not apply to you.

## Building Lexicode itself

The work queue is `plan/04-implementation-plan.md`. Read `plan/README.md` first — its six rules
bind you.

- **Implement exactly the story you were given.** Do not start the next story's work.
- **Do not commit.** The lead agent reviews and commits. This is a convention of the build
  process — one human reviewing a series of story-sized diffs in one checkout — and nothing more.
  It is not a property of the codebase, and it does not travel: an agent running *inside*
  Lexicode must commit (see above).
- **Never claim a command passed that you did not run.** Paste real output.

## The toolchain and the commands

Already installed; do not re-derive.

- Go 1.27 at `/opt/homebrew/bin/go`; `go.mod` declares `go 1.25.0` (the supported floor).
- Node v24.11.1, npm 11.6.2, Docker 29.7.2, `golangci-lint` v2.
- Module path: `github.com/spruce/lexicode`.

Commands:

- `make check` — go build + vet + golangci-lint + go test, then `tsc -b --noEmit` + eslint in
  `web/`. This is the definition of done for every change.
- `make build` — builds `web/dist` then the binary with version ldflags.
- `make web`, `make dev`, `make release`.
- `go test -tags docker ./internal/module/docker/ ./internal/module/claudecode/` — the
  real-daemon tests. Not part of `make check`; run them when you touch the sandbox, the runtime
  adapter, or anything about how a container is provisioned.
- `bash scripts/s24-e2e.sh run` and `bash scripts/s39-acceptance.sh` — the end-to-end
  acceptances, against a real container and a fake GitHub that serves real git. Run them when
  you touch provisioning, the push path, triggers or the poller.

## Things that bite, already solved

- `web/dist` is embedded with `//go:embed all:dist`. `web/dist/.gitkeep` is committed so a clean
  checkout compiles; `web/fallback.html` is served when `dist/index.html` is absent.
- `web/node_modules` is inside the Go module, so `npm ci` runs a postinstall hook that drops a
  `go.mod` into `node_modules` to hide it from `go build ./...`. Do not remove it.
- `internal/module/context` uses `package contextmod` so it never shadows stdlib `context`.
- The kernel may not import a module or a service (architecture §2.1). Where the two sides need
  to agree on a path or a variable name, the string is duplicated with a comment saying so —
  that is deliberate, not a missed refactor.
