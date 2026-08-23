# Lexicode — Architecture & Implementation Plan

This directory turns `../design/` (product brief + UI/UX spec + research) into something other
agents can build. It is the contract between the design pass and the implementation pass.

**Target for V1:** a single binary, `lexicode`, that you run on your own machine. It serves the
dashboard, orchestrates agent runs in real Docker containers running Claude Code, polls GitHub,
fires triggers, and enforces loop protection. Functional end to end — the six-step chain in
brief §3 must actually work.

## Read in this order

| Doc | What it is |
|---|---|
| [00-decisions.md](00-decisions.md) | Every technical decision, with rationale. Read first; the rest assume these. |
| [01-architecture.md](01-architecture.md) | Kernel/module design, ports, runtime topology, execution lifecycle, loop protection. |
| [02-data-model.md](02-data-model.md) | The complete SQLite schema and the domain invariants it enforces. |
| [03-contracts.md](03-contracts.md) | Go port interfaces, the container protocol, the Lexicode MCP tool surface, the HTTP/SSE API. |
| [04-implementation-plan.md](04-implementation-plan.md) | 39 sequential stories with acceptance criteria. This is the work queue. |
| [05-quality-guardrails.md](05-quality-guardrails.md) | Research pass (Aug 2026) on guardrails that raise agent code quality. Not V1 scope — a ranked, evidenced backlog for after it. |

## Rules for implementing agents

1. **Stories are sequential.** Story N may assume every story < N is merged and green. Do not
   start N+1's work inside N.
2. **The ports in `03-contracts.md` are frozen interfaces.** If a story forces a change to a port
   signature, that is a design escalation — say so in the PR rather than widening the interface
   quietly. Adding a *new* port is fine; changing an existing one affects other agents' work.
3. **Never reference a board column by name in code.** Always by `category`. (Brief D2.)
4. **Never let an agent merge, force-push a protected branch, or self-approve.** (Brief D6.) This
   is enforced in the forge adapter, not by prompt.
5. **Guidance and enforcement are different mechanisms.** Directives and wiki pages go in the
   prompt. Tool permissions and network policy go in CLI flags, settings files and Docker config.
   Never implement an enforcement control as prompt text. (Brief D7.)
6. Every story's definition of done includes tests at the level named in the story, plus
   `make check` (build, vet, lint, test, frontend typecheck) passing.

## Scope

Everything in brief §6, including trigger backtest. Explicitly excluded: everything in brief §8,
plus wiki→repo export (see [00-decisions.md](00-decisions.md) D-11 — import-only for V1).
