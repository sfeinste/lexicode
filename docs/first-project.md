# Your first project

From an empty workspace to a reviewed pull request. About ten minutes, most of it reading.

You need Lexicode running ([install.md](install.md)), a Claude credential
([oauth-token.md](oauth-token.md)), and a GitHub repository you can push to.

---

## 1. A GitHub token

Lexicode needs a token for the repository. A **classic personal access token with the `repo`
scope** is the simple answer: github.com → Settings → Developer settings → Personal access
tokens → Tokens (classic) → Generate new token → tick `repo`.

Fine-grained tokens work too; grant the repository **Contents: read and write**, **Pull
requests: read and write**, and **Issues: read** (the bootstrap import reads issues). Lexicode
checks a classic token's advertised scopes at connect time and names the missing one; a
fine-grained token advertises nothing, so the connect probes it and names whichever call failed.

The token is stored encrypted and is never shown again.

## 2. Create the project

**New project** → a key and a name. The key is short and uppercase — `PAY`, `WEB`, `CORE` — and
becomes the prefix of every ticket: `PAY-14`. Pick it deliberately; ticket keys are forever.

You get a board with six columns. Rename and reorder them however you like: every column carries
a fixed **category** (`backlog · ready · running · review · done · canceled`), and automations
key off the category, never the name. Renaming "Needs review" to "QA" breaks nothing.

## 3. Connect the repository

Project settings → **Repository** → owner, name, token. Lexicode verifies it immediately: it
reads the repository, checks what the token can do, and records the default branch and the head
commit. A bad token fails here with the reason, not later inside a container.

Connecting also starts the **poller** for this project. Its first pass is a baseline: it records
every open pull request and emits **nothing**. A repo with forty open PRs must not fire forty
triggers on connect.

## 4. Let it bootstrap you (optional, recommended)

Right after connecting, the project offers a **bootstrap scan**: one read of the repository that
proposes

- open **issues** to import as tickets,
- **docs** it found (`AGENTS.md`, `CLAUDE.md`, `.cursor/rules/*`, `docs/**`) to import as wiki
  pages, each with a proposed agent scope,
- two suggested **triggers** if it sees CI workflows — "Agent PR opened → run Reviewer" and
  "CI failed → run Dev" — created **disabled**, so nothing surprises you,
- two starter **agents**, Dev and Reviewer,
- a draft project **Overview**.

Everything is a checkbox and nothing is created silently. Re-running the scan is idempotent:
what you already imported shows as "Already imported".

## 5. The agent roster

If you skipped bootstrap, create the two agents by hand (project → **Agents** → New).

**Dev** — implementation. Permissions: read files, edit files, run commands, push branches, open
pull requests. Autonomy `auto_gates`: it acts on its own, and asks before anything destructive.

**Reviewer** — review. Permissions: read files, run commands, comment on pull requests, submit
reviews. **Not** edit files, **not** push branches, **not** open pull requests. It reads and it
says what it thinks; it does not change anything.

Neither can approve or merge. No permission unlocks that — it is not a setting, it is a
capability the system does not have.

Two things to know about an agent's configuration:

- The **directive** is the agent's standing instruction, in prose. It is guidance: it shapes what
  the agent tries.
- **Permissions** and **network policy** are enforcement. They are checked before any call
  happens, in the adapter, not in the prompt. An agent without `open_prs` cannot open a pull
  request however you word its directive.

Also worth setting on Dev's directive while the sandbox does not do it for you: *never commit
`.lexicode/` or `.claude/`* — the orchestrator materializes those into the workspace root and a
blanket `git add -A` would sweep them in ([docker.md](docker.md)).

## 6. Write a ticket and delegate it

Board → **New ticket**. Title, a description, and — this is the part that pays off —
**acceptance criteria**. They are a first-class list on the ticket, not bullet points buried in
the description: add them one at a time, reorder them, check them off. They go into the agent's
prompt, and the agent can tick one off itself as it verifies it, with a note saying how.

Then start it. There are exactly two ways to do that by hand: press **D** on the board and pick
Dev — which records Dev as the delegate *and* queues a run — or, on the ticket itself, set the
delegate in the sidebar and press **▶ Run delegate now**. Either way the answer is a run id and
its real state: a queued run says in words what is holding it ("waiting: Dev is at its 1-run
limit"), and a refusal says why nothing started.

The sidebar's **Delegate** dropdown on its own only sets the field — who *would* run. That is
what makes trigger-driven runs and auto-run columns meaningful, and it is why it does not start
anything by itself. Dragging a card never starts a run either, unless the destination column is
explicitly marked *⚡ auto-runs delegate*.

A ticket has an **assignee** (the human accountable for it) and a **delegate** (the agent doing
the work). Two fields, never one. Notifications from the run route to whoever delegated it.

## 7. Watch it, or don't

The run opens on a provisioning checklist — image, container, clone, branch, setup script — with
a line per step, because "provisioning…" with a spinner tells you nothing when it takes four
minutes. Then the activity stream: what the agent is thinking, what it is running, what came
back.

You can interrupt at any point. **Steer** (queue a message, applied after the current step),
**Stop** (the branch is preserved and recorded as partial work — a failed run always leaves an
artifact), or **Take over** (the run stops, the branch is yours).

If the agent needs a decision it asks, and the run parks in *needs input* — not a mystery, one
of four distinct states with its own colour and its own filter: needs input, plan approval,
review ready, failed. Unanswered for a minute, it becomes a notification for you specifically.

When it finishes with a pushed branch, the **orchestrator** opens the pull request — the agent
never holds a GitHub credential. The ticket moves to the review-category column on its own.

## 8. Wire the chain

This is the part that makes it a product rather than a nicer terminal. Project → **Triggers**.

Three rules give you the canonical loop:

| When | If | Then |
|---|---|---|
| Pull request **opened** | the actor is an agent | run **Reviewer**, prompt: `Review PR #{{pr.number}} on {{pr.branch}}. Post severity-tagged findings.` |
| Review **submitted** | `review.state is changes_requested` | run **Dev**, prompt: `Address the findings on PR #{{pr.number}}, branch {{pr.branch}}.` |
| Check suite **completed** | `check.conclusion is failure` | run **Dev**, prompt: `The {{check.name}} suite failed on PR #{{pr.number}}. Fix it on {{pr.branch}}.` |

The editor is generated from the event source's catalog, so the WHEN options, the IF fields and
their operators are always exactly what the poller can actually produce. `{{...}}` fields
interpolate from the event.

Two notes from building this chain for real:

- **The CI rule needs actor suppression off.** CI runs on the agent's own branch, so its event
  is attributed to that agent, and loop-protection layer 1 refuses to re-run an agent on its own
  event. Turn that one layer off on that one rule; the depth counter and the budget still bound
  it. See [loop-protection.md](loop-protection.md).
- **A follow-up run does not inherit the branch.** Every run gets a fresh branch, so a rule that
  says "address the findings" must name `{{pr.branch}}` in its prompt and the agent checks it out.
  Say so explicitly, as the table above does.

Before enabling a rule, **backtest** it: it replays against events already in the database and
shows what would have fired, what the guard would have stopped, and roughly what it would have
cost. Nothing runs.

## 9. Walk away

That is the whole promise. Come back to `/inbox`: one screen of what needs you and why, grouped
by reason rather than a single "waiting" badge.

Then you read the pull request and you merge it. **Always you.** Lexicode has no merge path —
not a permission that is off, an absent capability. The forge port it talks to GitHub through
has no merge method, and its review submission rejects `APPROVE` unconditionally.

---

## What to do next

- Put a page in the **wiki** and set its agent scope to `always`. It goes into every run's
  prompt for this project, and the run's context panel shows you exactly which pages loaded, why
  each one did, and what it cost in tokens.
- Give pages an **owner** and a `verified_until` date. When it lapses the page is demoted
  automatically — a stale document that steers machines is a live defect, not an annoyance.
- Watch each rule's **health** for a week. Eight outcomes are tracked, including the ones where
  nothing happened; a rule that fires and is suppressed every time looks nothing like a rule
  that never fires.
