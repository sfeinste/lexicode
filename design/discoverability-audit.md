# Discoverability audit — every route, every action

**Date:** August 2026 · **Ticket:** LEXI-13 · **Scope:** every route in
[`web/src/app/router.tsx`](../web/src/app/router.tsx), as the app actually behaves today.

The question asked of every row: **could someone who has never seen Lexicode, and has not been
told anything, find this action?** Not "is it in the code", not "is it in the keyboard map" —
could they *find* it.

## Verdicts

| Verdict | Means |
|---|---|
| **Visible** | A control with a readable label (or a conventional icon carrying an accessible name) is on screen, in context, at the moment the action is possible. |
| **Weak** | There is a control, but a newcomer has to guess: an unexplained glyph, a raw field name, a clickable thing that does not look clickable, or an interaction (drag, double-click) with no affordance. Reachable, but not *findable*. |
| **Hidden** | No in-context control at all. One of three kinds: **keyboard-only** (a chord and nothing else), **palette-only** (in `⌘K`, so mouse-reachable but unfindable by looking), or **URL-only** (you must type the address). |

Every **Hidden** row names the control that would replace it. That column is the actual work list.

## The count

| | Actions | Share |
|---|---|---|
| Visible | 129 | 85% |
| Weak | 8 | 5% |
| **Hidden** | **15** | **10%** |
| **Total** | **152** | |

Plus **two capabilities with no user interface at all** (§ *Capabilities with no way in*), and
**three screens the spec describes that render placeholders** (§ *Specified but not built*).

One of the fifteen — `f`, next failure, on the run detail — is **fixed in this branch**, as part of
the Material UI proof of concept. Fourteen remain.

The shape of the finding is not "the keyboard map is too big". The keyboard map is good, and UI
spec §6 is a genuine asset. The shape is: **ten of the fourteen remaining hidden actions are on one
screen — the board — and eight of those ten are things you do to a card**: status, priority,
assignee, delegate, labels, rename, multi-select, open. The board is where Lexicode's thesis lives
(`D` for delegate sitting beside `A` for assign, spec §6), and it is the screen with no mouse
path to any of it.

---

## Global chrome — on every signed-in route

Rendered by `AppShell` / `TopBar` / `LeftRail` / `ProjectLayout`.

| Action | How you reach it | Verdict | Replacement if hidden |
|---|---|---|---|
| Go home | Wordmark, and the "Home" link | Visible | |
| Go to the inbox | "Inbox" link, with an actionable-count badge | Visible | |
| Open notifications | The `◔` button (accessible name "Notifications") | **Weak** — `◔` is not a bell and reads as nothing | |
| Open the command palette | Top-bar button reading "⌘K search…" | Visible | |
| Open the keyboard cheatsheet | The `?` button | Visible | |
| Open the account menu | The avatar | Visible | |
| Workspace settings | Account menu → "Workspace settings" | Visible | *(this was the bug that motivated the ticket; it is fixed and pinned by `reachability.test.tsx`)* |
| Change theme | Account menu → Theme | Visible | |
| Change density | Account menu → Density | Visible | |
| Sign out | Account menu → "Sign out" | Visible | |
| Switch project | Left rail project list | Visible | |
| Open a needs-you item | Left rail "Needs you" rows | Visible | |
| See the rest of the needs-you queue | "+N more" → `/inbox` | Visible | |
| Open a project tab (7 tabs) | Tab bar | Visible | |
| Project settings | The `⚙` tab, and the `⚙` in the project header | **Weak** — two identical gears, one of them the eighth tab; conventional enough to survive, ambiguous enough to notice | |
| **Collapse the left rail** | `⌘\` only (also in `⌘K`) | **Hidden — palette-only** | A chevron `IconButton` pinned to the rail's bottom edge, `aria-label="Collapse sidebar"`, with the chord in its tooltip. This is the single most conventional control in the category and the app has none. |
| **Ask an agent** | `⌘J` only (also in `⌘K`) | **Hidden — palette-only** | A second top-bar button beside the search one, reading "⌘J ask an agent". Two palettes that are deliberately different (spec §6) need two visible doors, or the split is invisible and the feature is lore. |

*`G`-prefixed navigation (`G B`, `G W`, `G R`, `G A`, `G T`, `G G`, `G S`, `G H`, `G I`) is not
counted as hidden: every destination is a visible tab or link. Those chords are accelerators for a
visible path, which is exactly what a shortcut should be.*

**17 actions · 15 visible/weak · 2 hidden**

---

## `/setup`, `/login`, `/invite/$token`

| Action | How you reach it | Verdict |
|---|---|---|
| Create the first owner account | The form | Visible |
| Sign in | The form | Visible |
| Accept an invite | The form | Visible |

These three routes are URL-only *by design* and are listed with their reasons in
`reachability.test.tsx`'s `URL_ONLY` map: `/setup` exists only while the database has no users and
the API redirects to it; `/login` is where a 401 sends you; an invite token in the URL *is* the
address. Correct as they are.

**3 actions · 3 visible · 0 hidden**

---

## `/` — Home (spec §5.1)

| Action | How you reach it | Verdict | Replacement if hidden |
|---|---|---|---|
| Create a project | "Create project" (empty state) / "+ New project" | Visible | |
| Open a project | Clicking a row of the projects table | **Weak** — the row is a `<tr>` with an `onClick` and `tabIndex`, not a link: no underline, no cursor change, nothing to right-click or middle-click | |
| Answer a question from the needs-you card | "Answer" on the card; the form opens *on the card* | Visible | |
| Approve a plan from the card | "Approve" on the card | Visible | |
| Review a PR from the card | "View diff" → GitHub | Visible | |
| Open the blocked ticket or run | The subject link on the card | Visible | |

The needs-you strip is the best-designed surface in the app: flavour in words, one inline primary
action, answering without navigating. It is the model the rest should be held to.

**6 actions · 6 visible/weak · 0 hidden**

---

## `/inbox` — (spec §5.10)

| Action | How you reach it | Verdict |
|---|---|---|
| Answer / approve inline | The row's primary button (also `A`) | Visible |
| Dismiss a failure | "Dismiss" (also `X`) | Visible |
| Open the run | "View run" (also `Enter`) | Visible |

`J`/`K` move a cursor that only exists once you have started using the keyboard; they add no
capability. Nothing hidden here — this screen already follows the rule the ticket is asking for.

**3 actions · 3 visible · 0 hidden**

---

## `/settings` — Workspace settings

| Action | How you reach it | Verdict |
|---|---|---|
| Edit the workspace defaults (9 fields: branch, branch template, network policy, daily budget, context threshold, verification period, max containers, poll interval, PR-size warning) | Labelled fields, autosaved, with an inline "Saved" | Visible |
| Revoke a member's sessions | "Revoke sessions" per member | Visible |
| Set or replace the Claude credential | The Credentials section | Visible |
| Import the credential from `~/.claude` | Offered when detected | Visible |
| Add / replace / delete a workspace secret | The Secrets panel | Visible |
| Open the audit log | "Audit log →" | Visible |

Owner-only, and non-owners get a sentence saying so rather than dead controls — the right pattern.

**6 actions · 6 visible · 0 hidden**

---

## `/settings/audit` — Audit log

| Action | How you reach it | Verdict |
|---|---|---|
| Filter by actor / actor id / action / target kind / project / since / until, then Apply | Labelled form + "Apply" | Visible |
| Expand an entry to its before/after diff | The row is a button with `aria-expanded` | Visible |
| Load older entries | "Load older entries" | Visible |

**3 actions · 3 visible · 0 hidden**

---

## `/p/:key` — Project overview (spec §5.2)

| Action | How you reach it | Verdict |
|---|---|---|
| Connect a repository | The connect gate, shown when no repo is linked | Visible |

The About card is orientation, not action. The spec's three columns below it — recent runs, pinned
wiki pages, activity feed — render as placeholder copy; see *Specified but not built*.

**1 action · 1 visible · 0 hidden**

---

## `/p/:key/bootstrap` — Repository bootstrap

| Action | How you reach it | Verdict |
|---|---|---|
| Choose what to import (issues, docs, CI triggers, agents, overview) | Checkboxes + "Select all" | Visible |
| Run the import | The import button | Visible |
| Retry a failed scan | "Retry scan" | Visible |
| Go to the project overview when done | "Go to project overview" | Visible |

**4 actions · 4 visible · 0 hidden**

---

## `/p/:key/board` — Board / list (spec §5.3)

**The screen this audit exists for.**

| Action | How you reach it | Verdict | Replacement if hidden |
|---|---|---|---|
| New ticket | "+ New ticket" (also `C`) | Visible | |
| Switch board ⇄ list | "Board" / "List" buttons (also `⌘B`) | Visible | |
| Change grouping | A `<select>` labelled **`group_by`** | **Weak** — the visible label is the API field name. A newcomer reads a snake_case identifier where the app should say "Group by" | |
| Filter by assignee / delegate / label / priority | Four labelled selects | Visible (4 actions) | |
| Search tickets | The search box | Visible | |
| Choose displayed properties | "Display ⇧V" button | Visible | |
| Remove an active filter | The filter chips | Visible | |
| Move a ticket between columns | Drag | **Weak** — no grab handle, no cursor hint; conventional on a board, invisible to someone who has not used one | |
| Open a ticket | `Enter`, or double-click the card | **Weak** — a single click *selects* and produces almost no visible change, so the natural first attempt appears to do nothing | |
| **Peek at a ticket without opening it** | `Space` only | **Hidden — keyboard-only** | The peek panel should be what a *single* click does — single click currently has no visible outcome, so the affordance is free. |
| **Change a ticket's status** | `S` only | **Hidden — keyboard-only** | A `⋯` overflow `IconButton` on the card (visible on hover and always on focus) opening a menu: Status · Priority · Assignee · Delegate · Labels · Rename · Open. Every item shows its own chord, which is how spec §6's grammar is supposed to be taught ("every context menu displays its own shortcut"). One control retires six hidden actions. |
| **Change priority** | `P` only | **Hidden — keyboard-only** | Same card menu. |
| **Assign a human** | `A` only | **Hidden — keyboard-only** | Same card menu. |
| **Delegate to an agent** | `D` only | **Hidden — keyboard-only** | Same card menu — and this is the worst one in the app. `D` is the product's whole thesis (spec §6: "`D` for delegate sitting beside `A` for assign is the keyboard-level expression of the two-field model"). A user who never presses `D` never encounters the idea. |
| **Set labels** | `L` only | **Hidden — keyboard-only** | Same card menu. |
| **Rename a ticket** | `R` only | **Hidden — keyboard-only** | Same card menu. |
| **Select several tickets** | `X` only | **Hidden — keyboard-only** | A checkbox in the card's corner, appearing on hover and always present on focus, plus a selection toolbar naming the count and the bulk actions. |
| **Edit a ticket** | `E` only | **Hidden — keyboard-only** | "Open" in the card menu. `E` and `Enter` do the same thing today; one of them should have a label. |
| **The backlog view** (`?view=backlog`) | Typing the URL | **Hidden — URL-only** | Spec §1 lists `/p/:key/board?view=backlog` as a route of the product. Nothing anywhere links to it. It should be a segmented control beside the layout toggle: **All · Backlog**. *(The route-reachability test cannot catch this: it dedupes crawled URLs by pathname, so a query-string-only view is invisible to it — a real gap in the guard, noted in the migration plan.)* |

**22 actions · 12 visible/weak · 10 hidden**

---

## `/p/:key/triage` — Intake queue (spec §5.5)

| Action | How you reach it | Verdict | Replacement if hidden |
|---|---|---|---|
| Accept | "Accept" (also `1`) | Visible | |
| Mark duplicate | "Duplicate" (also `2`) | Visible | |
| Decline | "Decline" (also `3`) | Visible | |
| Snooze (until a time, or until new activity) | "Snooze" menu (also `H`) | Visible | |
| Open the ticket | "Open ticket ↵" | Visible | |
| **Peek without opening** | `Space` only | **Hidden — keyboard-only** | Same fix as the board: peek is what a single click should do. Triage rows are read-then-decide, so the preview is the main event. |

This screen is otherwise a model of the ticket's ask: four keyboard actions, four labelled buttons,
the chord shown on the control.

**6 actions · 5 visible · 1 hidden**

---

## `/p/:key/t/:ticket` — Ticket detail (spec §5.4)

| Action | How you reach it | Verdict | Replacement if hidden |
|---|---|---|---|
| **Reach this screen at all** | Activating a board card (`Enter` / double-click), a triage row, or an inbox row | **Hidden — activation-only** | The ticket **key** on every card and row should be a real `<a>`. Today the whole page has no anchor pointing at it — `reachability.test.tsx` exempts it in `URL_ONLY` with exactly that reasoning, which documents the gap honestly but does not close it. A link costs nothing and buys middle-click, right-click → copy address, and "this looks clickable". |
| Rename | "Rename (R)" | Visible | |
| Edit the description | The description *is* an always-live editor (`E` just focuses it) | Visible | |
| Add / reorder / delete acceptance criteria | Labelled buttons per row | Visible | |
| Create sub-tickets (or convert a selection) | "+ Sub-ticket ⌘⇧O" | Visible | |
| Comment, and `@`-mention people, agents, pages, tickets | The composer, with its behaviour in the placeholder | Visible | |
| Change status / priority / assignee / delegate / labels | Sidebar rows, each labelled, assignee and delegate visibly separate | Visible (5 actions) | |
| Start a run | The "Run" button beside Delegate | Visible | |
| Copy the checkout command | "Copy checkout command" | Visible | |
| Toggle the properties sidebar | "Sidebar ⌘I" | Visible | |

**14 actions · 13 visible · 1 hidden**

---

## `/p/:key/wiki` — Wiki index (spec §5.6)

| Action | How you reach it | Verdict |
|---|---|---|
| Search the wiki | The search box, placeholder "Search wiki… /" | Visible |
| New page | "New page" | Visible |
| Import from the repository | "Import from repository" | Visible |
| Filter by tag | The tag list | Visible |
| Open a page | Tree rows | Visible |

`/` focuses a search box that is already on screen — an accelerator for a visible control, not a
hidden action.

**5 actions · 5 visible · 0 hidden**

---

## `/p/:key/wiki/:slug` — Wiki page

| Action | How you reach it | Verdict |
|---|---|---|
| Rename | "Rename" | Visible |
| Edit / Save / Cancel | "Edit", then "Save" / "Cancel" | Visible |
| Change the agent scope | "Edit agent scope" | Visible |
| Add a tag | "+ tag" | Visible |
| Accept / Edit / Dismiss an agent proposal | Three buttons on the proposal view | Visible (3 actions) |
| Link an unlinked mention | "Link" per mention | Visible |
| Open the proposing run | "View the proposing run" | Visible |

**9 actions · 9 visible · 0 hidden**

---

## `/p/:key/runs` — Run list (spec §5.7)

| Action | How you reach it | Verdict |
|---|---|---|
| Switch saved view | The view tabs (Needs attention · All · saved) | Visible |
| Save the current filters as a view | "Save view…" | Visible |
| Add a state filter / an agent filter | Two selects | Visible (2 actions) |
| Remove a filter | The chips, each with its own remove label | Visible |
| Open a run | The status cell is the link | **Weak** — only the first cell is clickable; the obvious target (the whole row) is not |
| Clear filters from the filtered-empty state | "Clear filters" | Visible |

**7 actions · 7 visible/weak · 0 hidden**

---

## `/p/:key/runs/:id` — Run detail (spec §5.7)

**Converted to Material UI in this branch.**

| Action | How you reach it | Verdict |
|---|---|---|
| Select a step | Timeline rows (`ListItemButton`) | Visible |
| Expand a grouped tool call | The group row, with `aria-expanded` | Visible |
| Change verbosity | A three-option `ToggleButtonGroup` | Visible |
| ~~Jump to the next failure~~ | ~~`f` only~~ → **"Next failure" button** beside the verbosity switch, with `f` in its tooltip; disabled with the reason when the run has no failures | **Fixed** (was Hidden — keyboard-only) |
| Permalink a log line | Clicking a line | **Weak** — line numbers are visible, their clickability is not |
| Answer a question | The inline answer form | Visible |
| Approve / Approve with edits / Respond / Deny | Four labelled buttons, inline in the timeline, never a modal | Visible (4 actions) |
| "Always allow" as a scoped rule | A checkbox that names the rule it will write | Visible |
| Steer the run | The composer, "Applied after the current step." as helper text | Visible |
| Stop the run | "Stop" → "Confirm stop" / "Keep running" | Visible |
| Take over | "Take over" → a real dialog | Visible |
| Copy the checkout command | "Copy" | Visible |
| Show the context pane below 1400px | "Context ▸" | Visible |

**16 actions · 16 visible/weak · 0 hidden** *(was 1 hidden)*

---

## `/p/:key/agents` — Agent roster (spec §5.8)

| Action | How you reach it | Verdict |
|---|---|---|
| Create an agent | "Create agent" | Visible |
| Open an agent | The roster card | Visible |
| Enable / disable an agent | A checkbox on the card | Visible |

**3 actions · 3 visible · 0 hidden**

---

## `/p/:key/agents/:id` — Agent configuration (spec §5.8)

| Action | How you reach it | Verdict |
|---|---|---|
| Edit identity (name, role, colour, git author name and email) | Labelled fields | Visible |
| Edit the directive, with a version note | A labelled monospace editor + live token count | Visible |
| Browse directive versions and diff them | "Directive versions" | Visible |
| Change model and thinking effort | Two selects | Visible |
| Change permissions | Checkboxes, visually distinct from the directive textarea (spec §9 rule 9) | Visible |
| Delete a permission rule | "Delete" per rule | Visible |
| Change the autonomy level | A four-stop control; the dangerous rung needs "Confirm Auto" | Visible |
| Change limits (concurrency, daily cap, wall clock, max steps) | Labelled fields | Visible |

**8 actions · 8 visible · 0 hidden**

---

## `/p/:key/triggers` — Trigger list (spec §5.9)

| Action | How you reach it | Verdict |
|---|---|---|
| New trigger | "New trigger" | Visible |
| Open a trigger | The card | Visible |
| Enable / disable | The card's toggle | Visible |

**3 actions · 3 visible · 0 hidden**

---

## `/p/:key/triggers/:id` — Trigger editor (spec §5.9)

| Action | How you reach it | Verdict |
|---|---|---|
| Edit WHEN (event, activity types, filters) | The labelled WHEN section | Visible |
| Edit IF (field / operator / value rows) | Labelled selects, "+ And", "Add Or group" | Visible |
| Edit THEN (action, agent, prompt override) | The labelled THEN section | Visible |
| Configure loop protection (5 layers) | A row per layer with a toggle and plain-language description | Visible |
| Run a backtest | "Backtest" + window picker | Visible |
| Read the firing history and loop chains | "Firing history" | Visible |

The best screen in the app for the ticket's stated goal: it reads as prose, every control is
labelled in words, and the loop-protection panel explains itself.

**6 actions · 6 visible · 0 hidden**

---

## `/p/:key/settings/*` — Project settings (spec §5.11)

| Action | How you reach it | Verdict |
|---|---|---|
| Edit general settings (name, description, colour, agent guidance) | Labelled fields, autosaved | Visible |
| Manage board columns (add, delete, WIP limit, auto-start delegate, category) | Labelled controls per column | Visible |
| Change the repository (default branch, view head commit, token) | The Repository section | Visible |
| Re-scan the repository | "Re-scan repository" | Visible |
| Disconnect the repository | "Disconnect…" with confirmation | Visible |
| Edit the setup script | A labelled editor that says when it runs and that nothing is cached | Visible |
| Manage project secrets | The Secrets panel | Visible |
| Set the network policy and the allow-list, with inherit / override / reset | Labelled radios + the "Inherited from workspace / Override / Reset to workspace default" line | Visible |
| Rotate the repository token | "Rotate repository token" | Visible |
| Delete the project | Danger zone, typed confirmation | Visible |

Two notes. The `settings/$` splat means **every** unknown settings path renders the same page, so a
mistyped section silently shows the index rather than a 404 — harmless, but it means the section
rail is the only real navigation. And the *setup script* — which the ticket lists as having no UI —
**does** have one now; it is the *container image* that still does not (below).

**10 actions · 10 visible · 0 hidden**

---

## Capabilities with no way in

Not hidden actions — actions that were never given a surface. The pattern the ticket names
("capability exists, the way in does not") in its purest form.

### 1. The container image (`image_ref`)

`internal/domain/repo.go` carries `ImageRef`. `internal/module/docker/image.go` pulls it.
`internal/module/docker/sandbox.go` validates that a custom image has `git` and `claude` on PATH
and returns a typed `ImageMissingToolsError` if not. `internal/kernel/store/repos.go` reads and
writes the column.

**There is no API field.** `internal/api/v1/openapi.yaml` never mentions it, and
`internal/service/bootstrap/reposettings.go:13` says so out loud: *"image_ref is deliberately NOT
here: it has its own story to be reported separately."* So the only way to set a custom container
image is to write the SQLite row by hand.

**Replacement:** a field in project settings → Repository, beside the setup script, with the
tool-validation error surfaced inline (the backend already produces exactly the right message).
Needs an API change, so it is a story of its own — flagged here, not fixed here.

### 2. Module health / the degraded GitHub poller

`GET /api/v1/system/modules` exists and is in the generated client
(`web/src/lib/api/client.ts` → `systemApi.modules`). The SSE stream carries `module.degraded`, and
`web/src/lib/sse/applyEvent.ts:166` invalidates the `["system","modules"]` query when one arrives.

**Nothing renders it.** `systemApi.modules` has no caller anywhere in `web/src`. The cache
invalidation refreshes a query no screen subscribes to. When the poller degrades — a bad token, a
rate limit, GitHub down — the reason exists only for someone running `curl`.

**Replacement:** a status strip in the project header (and on `/settings`) that appears only when a
module is degraded, rendering the server's reason verbatim through `StatusDot` so it joins the §4
vocabulary rather than inventing an eleventh state. The event plumbing is already there; this is a
component and a query hook.

---

## Specified but not built

Placeholders, not bugs — recorded so the migration does not mistake them for conversions.

| Surface | Spec | Today |
|---|---|---|
| Overview: recent runs, pinned wiki pages, activity feed | §5.2 "three columns below" | Three empty-state sentences |
| Home: the repo column of the projects table | §5.1 | A literal `—`, with a code comment saying it lands when repo data is joined |
| Ticket sidebar: linked PR | §5.4 | Renders `#N` correctly *when* `pr_number` is set; the ticket's complaint ("says None forever") is a data-population gap, not a rendering one — the field is wired |

---

## What this changes about the plan

Three things fell out of doing the audit that were not obvious before it:

1. **The work is concentrated, not spread.** Ten of fifteen hidden actions are on the board, and
   six of those ten are retired by **one** control — a card overflow menu. The migration should
   therefore not be ordered by "biggest screen first"; it should be ordered so the board's card
   menu lands early, because it is the single highest-value change in the audit.

2. **The route-reachability test has a blind spot worth closing.** It dedupes crawled URLs by
   pathname, so `?view=backlog` — a route the spec names in §1 — can be an orphan while the suite
   is green. Any view that lives in a query parameter is invisible to it. Cheap fix: let the crawl
   key on pathname + the search params a route declares in `validateSearch`.

3. **"Discoverable" and "accessible" turned out to be the same list.** Every hidden action found
   here is also an accessibility defect — a capability with no accessible name, because it has no
   control to carry one. That is why the fix is a component library rather than a pass of copy
   edits: the same control that gives a mouse user a door gives a screen-reader user a name.
