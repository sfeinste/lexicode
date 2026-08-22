# Loop protection

Triggers turn one agent's output into another agent's input. That is the point of the product,
and it is also how you build an infinite loop by accident:

```
PR opened → Reviewer reviews → Dev addresses → Dev pushes → PR updated → Reviewer reviews → …
```

Every automation product in this category either bans cascades outright or lets you discover the
loop on your bill. Lexicode's third option: five layers, all on by default, and a chain view
that shows you the cycle you built.

## The five layers

They run in order. The first one to say no ends the firing, and the reason is recorded on the
rule's history in words.

**0 — the escape hatch.** A skip token anywhere in the pull request body, the triggering comment
or review body, or the head commit message short-circuits everything before layer 1. Spellings:
`skip-agents`, `skip-agent`, `[skip agents]`, case-insensitive. Put one in a PR body and no rule
will touch that pull request.

**1 — actor suppression.** An event caused by an agent does not re-trigger *that same agent*.
This is what stops the simplest cycle: an agent's own comment coming back as an event and firing
the rule that wrote it. It works because every write Lexicode makes on your behalf carries an
invisible marker naming the agent and the run, so a comment, review or pull request is
attributable when the poller reads it back. Recorded as `no_action`.

**2 — debounce.** A second firing of the same rule on the same subject within the window is
absorbed by the run that is already going. Default 90 seconds. It is a database probe, not a
timer, so it survives a restart. Recorded as `debounced`, linked to the run that absorbed it.

**3 — cancel in progress.** If a run for this rule and subject is still going when a newer event
arrives, the new run supersedes it — the old one is cancelled with a reason naming its
replacement. Three pushes in a minute produce one review of the final state, not three reviews
of three intermediate states. Recorded as `superseded`.

**4 — the depth counter.** The one that catches genuine cycles. Lexicode walks the causal chain
backwards from the triggering event — event → the run that caused it → the event that caused
*that* run — counting hops on the same subject. At the limit (default **3**) the firing stops.

Two things reset the walk, both deliberately: a **human action on the subject** newer than the
hop being counted, and a hop whose run a **human requested directly**. Answer a question, push a
commit, comment on the PR, and the budget starts again — a human in the loop is the signal that
this is not a runaway.

**5 — the budget ceiling.** Three scopes checked against the day's spend: project/day,
agent/day, rule/day. The workspace default is $20/day per project. Recorded as
`budget_exceeded`, and unlike the others it is an answer rather than a wait: the run does not
start later, it does not start.

## What a stopped loop looks like

A depth-limit stop **creates a run row** rather than suppressing one. It has no container, no
prompt and no cost; its state is `loop_stopped` and its reason says exactly what happened:

```
loop stopped: depth 3 reached the limit of 3 on pr:1
```

That row exists so the chain has something to hang the explanation on. Open it and you get the
causal chain, rendered:

```
  run #1   Dev       depth=0  subject=ticket:PAY-1 completed
   └─ event pull_request/opened on pr:1, actor agent
  run #2   Reviewer  depth=0  subject=pr:1     completed
   └─ event pull_request_review/submitted on pr:1, actor agent
  run #3   Dev       depth=1  subject=pr:1     completed
   └─ event pull_request_review/submitted on pr:1, actor agent
  run #4   Reviewer  depth=2  subject=pr:1     completed
   └─ event pull_request_review/submitted on pr:1, actor agent
* run #6   Dev       depth=3  subject=pr:1     loop_stopped
```

Nothing there is denormalized. The chain is reconstructed from the two causality edges the
database stores, every time, for any run — so it is available for a healthy chain as well, and
it cannot drift from the rows it describes.

*(That is real output from `scripts/s39-acceptance.sh`, which builds exactly this cycle on
purpose and asserts that the guard stops it.)*

## Tuning it

Per rule, in the trigger editor's loop-protection panel:

```jsonc
{
  "actor_suppression": true,
  "debounce_seconds": 90,
  "cancel_in_progress": true,
  "depth_limit": 3,
  "daily_budget_cents": null   // null inherits the project ceiling
}
```

Turn a layer off by setting it `false` or `0`. A malformed stored config falls back to the
defaults whole — protection does not switch off because a row got corrupted.

### When you have to turn one off

One case comes up in practice. **"CI failed → run Dev to fix it"** does not fire with actor
suppression on: CI runs on the agent's own branch, so the poller attributes the check-suite
event to that agent, and layer 1 correctly refuses to re-run the agent its own event names.
The fix is to set `actor_suppression: false` on that one rule — the depth counter and the budget
still bound it, and they are the layers that matter for a CI-repair loop.

## Reading the rule's health

Every firing lands in the rule's history with one of eight outcomes:

`succeeded` · `no_action` · `awaiting_approval` · `errored` · `debounced` · `superseded` ·
`loop_stopped` · `budget_exceeded`

"The rule fired and nothing happened" having a *name* is the point. A rule that never fires and
a rule that fires and is suppressed every time look identical in every product that only counts
runs; here they are two different sparklines.

## Before you enable a rule

Use **backtest**. It replays the rule against the events already in the database and shows what
would have fired, what the guard would have stopped, and roughly what it would have cost — all
without starting anything.
