# Working conventions for implementing agents

The work queue is `plan/04-implementation-plan.md`. Read `plan/README.md` first — its six rules bind you.

## Toolchain (already installed; do not re-derive)
- Go 1.27 at `/opt/homebrew/bin/go`; `go.mod` declares `go 1.25.0` (the supported floor).
- Node v24.11.1, npm 11.6.2, Docker 29.7.2, `golangci-lint` v2.
- Module path: `github.com/spruce/lexicode`.

## Commands
- `make check` — go build + vet + golangci-lint + go test, then `tsc -b --noEmit` + eslint in `web/`.
  This is the definition of done for every story.
- `make build` — builds `web/dist` then the binary with version ldflags.
- `make web`, `make dev`, `make release`.

## Things that bite, already solved
- `web/dist` is embedded with `//go:embed all:dist`. `web/dist/.gitkeep` is committed so a clean
  checkout compiles; `web/fallback.html` is served when `dist/index.html` is absent.
- `web/node_modules` is inside the Go module, so `npm ci` runs a postinstall hook that drops a
  `go.mod` into `node_modules` to hide it from `go build ./...`. Do not remove it.
- `internal/module/context` uses `package contextmod` so it never shadows stdlib `context`.

## Rules of engagement
- Implement exactly the story you were given. Do not start the next story's work.
- Do not commit; the lead agent reviews and commits.
- Never claim a command passed that you did not run. Paste real output.
