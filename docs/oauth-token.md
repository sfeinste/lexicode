# The Claude credential

An agent run is a Claude Code process in a container. It needs a credential, and Lexicode holds
exactly one for the whole workspace.

## The short version

```sh
claude setup-token
```

Copy the whole thing it prints — it starts with `sk-ant-` — and paste it into
**Settings → Credentials → Claude Code OAuth token**. That is the entire setup.

## What that token is

`claude setup-token` is a Claude Code command that mints a long-lived OAuth token against your
Claude subscription. Lexicode stores it encrypted (see below) and injects it into every run's
container as `CLAUDE_CODE_OAUTH_TOKEN`. It is provisioned once and read per run — you are never
asked to authenticate again, and no browser flow happens inside a container.

## Importing an existing login

If you already use Claude Code on this machine and its login sits in a file, Settings →
Credentials offers **Import from `~/.claude/.credentials.json`** — the button appears only when
that file is actually there. On macOS the CLI keeps its login in the system keychain instead,
so there is nothing to import and `claude setup-token` is the path.

## The fallback: the server's own environment

If no token is stored, Lexicode falls back to whatever is in the **server process's**
environment:

```sh
CLAUDE_CODE_OAUTH_TOKEN=sk-ant-... lexicode serve
# or
ANTHROPIC_API_KEY=sk-ant-... lexicode serve
```

This is the development path, and `lexicode doctor` reports it as a warning rather than a pass:
it works, but the credential is then in your shell history and your process table instead of the
encrypted store, and it disappears when someone restarts the server from a different shell. If
both are set, the OAuth token wins, so a container never receives two competing credentials.

## Where it is kept

In the encrypted secret store: a table in `lexicode.db`, encrypted with the key in
`<data-dir>/master.key`. That file is created mode `0600` on first boot, and Lexicode refuses to
start if its permissions are looser — a readable master key is the same as no encryption.

The value is write-only through the API. Nothing reads it back out to a browser: the settings
screen shows *configured / healthy / here is the problem*, never the token. The two places that
read the plaintext in-process are the container's environment assembly and the forge calls, and
both register the value with the run's log redactor first, so it cannot appear in a log line, a
transcript or an error message.

## Checking it

```sh
lexicode doctor
```

```
  ok    Claude token     a stored OAuth token is present and well-formed
```

`doctor` checks that a token exists and looks like `claude setup-token` output. It does not call
Anthropic — a check that spends money or rate limit to answer "probably" is worse than one that
answers "it is there and it is the right shape". A token that has actually expired shows up as a
failed run, with the provider's own message on the run's activity stream.

## Rotating or removing it

Settings → Credentials → paste a new one, or **Clear**. Runs already in flight keep the
credential they started with; the next run picks up the new one.

## What it costs

Every run records its own cost, and the numbers roll up per agent, per project and per day.
Loop protection's fifth layer is a budget ceiling — project/day, agent/day and rule/day — and
the workspace default is $20/day. See [loop-protection.md](loop-protection.md).
