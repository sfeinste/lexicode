# Network policy

An agent in a container has network access. How much is a per-repository setting, enforced by
Docker and an egress proxy — not by asking the agent nicely. (Guidance and enforcement are
different mechanisms: a directive that says "don't call the internet" is a suggestion; a network
with no route out is not.)

Set it in **project settings → Network**. The workspace default is `allowlist`.

## The three policies

| Policy | The container can reach |
|---|---|
| **None** | Only what the agent itself needs: `api.anthropic.com` and `claude.ai`, plus your git remote. Nothing else. |
| **Allowlist** | The above, plus the domains you list. |
| **Open** | Everything the host can reach. |

`none` is deliberately not "no network at all". An agent that cannot reach the model cannot
think, so the setting would be a trap. It means *nothing beyond what the agent itself needs*.

Telemetry hosts are not on the built-in list. Their denials are harmless and visible in the
run's activity stream, which is better than a silent allowance.

## How it is enforced

`open` containers sit on Docker's default bridge and are unrestricted.

`none` and `allowlist` containers join **`lexicode-internal`**, an internal Docker network with
no route out at all. The only path outward is an authenticated HTTP proxy the orchestrator runs
on the host (`--proxy-port`, default 7718):

```
agent container ── lexicode-internal ──▶ lexicode-egress relay ── bridge ──▶ host proxy ──▶ internet
```

The relay exists because a container on an internal Docker network cannot reach the host
directly on Docker Desktop — `host.docker.internal` does not resolve there. It is a dumb TCP
forwarder pinned to the proxy's address; it enforces nothing and can reach nothing else.

The proxy is where the policy lives:

- Every request must carry `Proxy-Authorization` with a **live run token**. No token, no
  connection — 407 before anything is dialed. The proxy binds all interfaces (containers reach
  it through Docker's NAT), so it is closed by authentication rather than by binding.
- Tokens are per run and are revoked when the run ends. A leaked one is useless afterwards.
- HTTPS is **tunneled, never intercepted**. A `CONNECT` is allowed or denied on its target
  hostname, and the proxy dials that same hostname — a client cannot check one host and tunnel
  to another. Lexicode does not hold a CA and does not see inside your TLS.
- Plain HTTP is proxied the same way, checked on the URL's host.

## What it looks like in a run

Every allow and deny is an activity on the run's stream — not a silent failure and not a wall of
noise: the same (run, host, outcome) inside a 30-second window is logged once, carrying a count
of what it absorbed. An `npm install` against a blocked registry is one row saying so, plus a
number, instead of five hundred rows.

That stream is the debugging tool. When a setup script fails under `allowlist`, the run itself
tells you which host was refused, and you add it.

## Choosing one

- **A repository whose build fetches dependencies** — `allowlist`, with your registries on it:
  `registry.npmjs.org`, `proxy.golang.org`, `pypi.org`, `files.pythonhosted.org`, your internal
  artifact host.
- **A repository with vendored dependencies** — `none`. Cheap, and the strictest thing that
  still works.
- **Debugging, or an agent that genuinely needs the web** — `open`, knowingly. This is also what
  the end-to-end fixtures use, because a fixture GitHub on the host has to be reachable.

Allowlist entries are hostnames, matched exactly. A `*.` prefix covers subdomains and the bare
domain both — `*.example.com` matches `example.com` and `api.example.com`.

## The git remote is not special

Cloning and pushing go through the same proxy under `none` and `allowlist`, using the same
per-run credential. Your git host does not need to be on the allowlist — the policy always
permits the repository the run was provisioned for.

## The proxy port

`--proxy-port` (default 7718) binds all interfaces because containers must reach it through
Docker's NAT. The same listener also serves the Lexicode MCP endpoint for sandboxed containers,
which is why runs under `none` can still ask you a question. Both are gated by the same run
token.

If that port is taken, `lexicode doctor` says so and names the flag.
