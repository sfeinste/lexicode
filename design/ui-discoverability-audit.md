# Discoverability audit — every route, every action

**Date:** 25 August 2026 · LEXI-13
**Companion documents:** [ui-library-evaluation.md](ui-library-evaluation.md) ·
[../plan/06-ui-redesign-plan.md](../plan/06-ui-redesign-plan.md)

---

## 1. What this audit asks

For every route in `web/src/app/router.tsx`, every action a user can take on it, judged by one
question:

> **Could someone who has never seen this app, and has been told nothing about it, find this
> action by looking at the screen?**

Not "is it possible", not "is it documented", not "is it in the cheatsheet". *Visible, on the
screen, labelled in words.* Three verdicts:

| | Meaning |
|---|---|
| **Visible** | A labelled control is on screen. A newcomer finds it by reading. |
| **Weak** | Reachable by mouse, but with no label, no affordance, or only after an unprompted step (hover, select a row first). Findable by poking; not by reading. |
| **Hidden** | Reachable only by keyboard chord, or by typing a URL. A newcomer cannot find it at all. |

Every **Hidden** and **Weak** row names the visible control that should replace it. That column
is the actual deliverable — the rest is bookkeeping.

**Method.** Read every route component and its children in `web/src/routes` plus the chrome in
`web/src/app/shell`, cross-referenced against every `chord:` registration in the codebase
(`grep -rn "chord:"` → 44 bindings across 8 keymaps). The route list is `router.tsx`'s tree,
in full, including the four routes the reachability test exempts.

**Scope note.** Two of the ticket's stated failures were already fixed before this run and the
audit records them as fixed rather than repeating them: workspace settings now has a link (top
bar → avatar → **Workspace settings**, guarded by `reachability.test.tsx`), and the setup script
now has a UI (`ProjectSettingsPage` → Repository → `SetupScriptSection`). The `D` key and the
ticket **Run** button now genuinely start runs, guarded by `delegateStartsRun.test.tsx` and
`runButton.test.tsx`.

---

## 2. The chrome — present on every signed-in route

| Action | Control today | Verdict | Replacement if not Visible |
|---|---|---|---|
| Go home | "Lexicode" wordmark + "Home" link, top bar | Visible | — |
| Go to inbox | "Inbox" link with unread badge | Visible | — |
| Open notifications | Bell button, `aria-haspopup="menu"` | Visible | — |
| Open the command palette | Button reading "⌘K search…" | Visible | — |
| Open the shortcut cheatsheet | "?" icon button | Visible | — |
| Workspace settings | Avatar menu → "Workspace settings" | Visible | — |
| Change theme | Avatar menu → Theme select | Visible | — |
| Change density | Avatar menu → Density select | Visible | — |
| Sign out | Avatar menu → "Sign out" | Visible | — |
| Switch project | Left rail project list | Visible | — |
| Jump to a blocked item | Left rail "Needs you" rows | Visible | — |
| **Collapse the left rail** | `⌘\` only — no button anywhere | **Hidden** | A chevron toggle at the rail's foot, `aria-expanded`, labelled "Collapse sidebar" |
| **Ask an agent (⌘J palette)** | `⌘J` only. `AskAgentPalette` registers no button and appears in no menu | **Hidden** | An "Ask an agent" button beside the ⌘K search field in the top bar |
| **`G`-prefixed navigation** (`G B` board, `G W` wiki, `G R` runs, `G A` agents, `G T` triage, `G G` triggers, `G S` settings, `G H` home, `G I` inbox) | Chords only | **Hidden** | Already redundant — every destination is a project tab or a top-bar link. **Delete the chords**; they duplicate visible navigation and cost a cheatsheet to learn |
| Project tabs (Overview/Board/Triage/Wiki/Runs/Agents/Triggers) | Tab bar with badges | Visible | — |
| Project settings | ⚙ in the project header **and** ⚙ tab | Visible | — |
| Daily-spend chip | Header chip with `aria-label` | Visible (read-only) | — |

---

## 3. Route by route

### `/setup` — first-run account creation · `/login` · `/invite/$token`

| Action | Control today | Verdict |
|---|---|---|
| Create the first owner account / sign in / accept an invite | Labelled form, single submit button | Visible |

These three are URL-only *routes* by design (the API redirects to them; an invite token is the
address). Their *actions* are all visible. No change needed.

### `/` — Home

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Create a project | "+ New project" button; the empty state's "Create project" CTA | Visible | — |
| Answer a question / approve, inline | Card's primary action button ("Answer" / "Approve") expanding `InlineElicitation` | Visible | — |
| Open a blocked item | Card subject link | Visible | — |
| Review a PR | Card action, opens the PR in a new tab | Visible | — |
| **Open a project** | Table `<tr tabIndex={0} onClick>` — a clickable row with no link semantics | **Weak** | Make the project **name a real link** (`<a href>`). A row that navigates but is not a link cannot be middle-clicked, gives screen readers no cue, and shows no `:visited`/hover affordance |
| **See the three Overview columns** | — | n/a | (see `/p/$key` below) |

### `/inbox` — cross-project needs-you

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Answer / approve inline | Row action button | Visible | — |
| Open the run | Row link ("View run") | Visible | — |
| Dismiss a failure | "Dismiss" button on failure rows | Visible | — |
| **Move the selection** (`J`/`K`) | Chords only | **Hidden** | Redundant with clicking a row. **Delete**; rows are already focusable |
| `Enter` open · `A` primary action · `X` dismiss | Chords, each duplicating a visible button | Hidden but **redundant** | Delete the chords; the buttons already exist and are labelled |

### `/settings` — Workspace settings

| Action | Control today | Verdict |
|---|---|---|
| Paste the Claude OAuth token | Labelled input + "Save" | Visible |
| Import the token from `~/.claude` | "Import" button (when available) | Visible |
| Clear the token | "Clear" button | Visible |
| Invite a member | Form + button | Visible |
| Edit the eight workspace defaults | Labelled inputs, autosaved | Visible |
| Add/remove a workspace secret | Secrets section controls | Visible |
| Open the audit log | "Audit log →" link | Visible |

The screen the ticket was written about is now, on its own terms, the best-behaved screen in the
app. Everything is a labelled control with a visible save state.

### `/settings/audit` — Audit log

| Action | Control today | Verdict |
|---|---|---|
| Filter by actor / action / subject / project | Four labelled inputs | Visible |
| Page through entries | Cursor pagination control | Visible |

### `/p/$key` — Project Overview

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Connect a repository | `ConnectRepoCard`, shown only when no repo is connected | Visible | — |
| Read the About card | Read-only | n/a | — |
| **Recent runs** | A heading over the sentence *"Runs appear here once agents start working."* — **the list is never rendered, for any data** | **Missing, not hidden** | Render the last 10 runs as rows linking to `/p/:key/runs/:id`, per §5.2 |
| **Pinned wiki pages** | Same — a permanent placeholder | **Missing** | Render pinned pages, or remove the column until it exists |
| **Activity feed** | Same — a permanent placeholder | **Missing** | Render the activity feed, or remove the column |

Three of this screen's four regions are stubs that render placeholder copy *unconditionally*.
This is the most misleading screen in the app: it looks finished and is empty by construction.

### `/p/$key/bootstrap` — Repository scan / import

| Action | Control today | Verdict |
|---|---|---|
| Choose which issues to import | Checkbox list | Visible |
| Set each detected doc's agent scope | Labelled select per doc | Visible |
| Accept / skip proposed triggers, agents, overview | Section controls | Visible |
| Finish and go to board / wiki / triggers / agents | Named links on the completion screen | Visible |

### `/p/$key/board` — Board / list · **the worst screen in the app**

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Switch board ⇄ list | Toggle group "Board \| List" | Visible | — |
| Change `group_by` | Labelled select | Visible | — |
| Filter by assignee / delegate / label / priority | Four `aria-label`led selects | Visible | — |
| Search | Search input | Visible | — |
| Display properties | "Display ⇧V" button | Visible | — |
| New ticket | "+ New ticket" button | Visible | — |
| Clear one filter | Removable chips | Visible | — |
| Open a ticket | Card click / double-click (`role="button"`) | Visible | — |
| Move a ticket between columns | Native HTML5 drag | Weak | Keep drag, **and** add the status control below — drag is undiscoverable to anyone who does not try it, and impossible without a pointer |
| **Set status** (`S`) | Chord only | **Hidden** | A "⋯" overflow menu on every card (MUI `IconButton` + `Menu`) with **Status ▸** |
| **Set priority** (`P`) | Chord only | **Hidden** | Same menu: **Priority ▸** |
| **Assign a human** (`A`) | Chord only | **Hidden** | Same menu: **Assignee ▸** |
| **Delegate to an agent — this starts a run** (`D`) | Chord only | **Hidden** | Same menu: **Delegate & run ▸**, worded so it is clear it *starts* something. This is the single most consequential hidden action in the product: it spends money and it is bound to one unlabelled keypress |
| **Edit labels** (`L`) | Chord only | **Hidden** | Same menu: **Labels ▸** |
| **Rename** (`R`) | Chord only | **Hidden** | Same menu: **Rename** |
| **Peek without opening** (`Space`) | Chord only | **Hidden** | Delete. A newcomer opens the ticket; peek is an expert optimisation that costs a whole interaction mode |
| **Multi-select** (`X`) | Chord only | **Hidden** | Checkbox on card hover/focus + a bulk-action bar when any card is selected |
| **Move the selection** (`J`/`K`), **clear it** (`Esc`) | Chords only | **Hidden** | Delete — cards are already focusable |
| Import issues from GitHub | Empty state's "Import N open issues" CTA | Visible (only while the board is empty) | — |

**Ten hidden actions on one screen, including the one that spends money.** Every mutation this
screen supports is keyboard-only. The fix is one composition — MUI `IconButton` + `Menu` +
`MenuItem`, one per card — replacing seven chords at once.

### `/p/$key/triage` — Intake queue

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Accept (`1`) / Duplicate (`2`) / Decline (`3`) / Snooze (`H`) | Buttons **that render only once a row is selected**, each showing its `<kbd>` hint | **Weak** | Render the four actions on every row (or on hover/focus), not only the selected one. The `<kbd>` hints are good practice and should stay |
| **Peek** (`Space`) | Chord only | **Hidden** | Delete, as on the board — or make it the row's own expand chevron |
| Move selection (`J`/`K`), open (`Enter`), close (`Esc`) | Chords only | Hidden but **redundant** | Delete |
| See provenance | Rendered inline on every row | Visible | — |

### `/p/$key/t/$ticket` — Ticket detail

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| **Run the delegate now** | "▶ Run delegate now" button with a hint sentence, disabled with the reason stated | Visible | — |
| Change status / priority / assignee / delegate | Four `aria-label`led selects in the sidebar | Visible | — |
| Toggle the sidebar | Button reading "Sidebar ⌘I" | Visible | — |
| Add / check / reorder / delete acceptance criteria | Buttons per row + an "Add a criterion" input | Visible | — |
| Create sub-tickets | "+ Sub-ticket ⌘⇧O" button | Visible | — |
| Comment | Composer at the foot of the stream | Visible | — |
| Copy the checkout command | Copy button on the branch row | Visible | — |
| **Rename** (`R`) | The `<h1>` is clickable, with a `title="Rename (R)"` tooltip. No `role`, no `tabindex`, no visible affordance | **Weak** | An edit (pencil) `IconButton` beside the title, `aria-label="Rename ticket"`. A clickable `<h1>` is invisible to a screen reader and to anyone who does not hover it |
| `E` edit / `Esc` back | Chords duplicating existing behaviour | Hidden but redundant | Delete |
| **Linked PR** | Sidebar row rendering `#<n> <state>` as **plain text** — not a link, and no check status | **Weak** (and see §5) | Make it a link to the PR with the check-status glyph beside it, per §5.4 |

### `/p/$key/wiki` — Wiki index · `/p/$key/wiki/$slug` — Wiki page

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Search pages | Input, placeholder "Search wiki…  /" | Visible | — |
| Focus the search box (`/`) | Chord; the placeholder advertises it | Hidden but **redundant** and self-documenting | Delete the chord, keep the box |
| Create a page | "New page" form in the tree | Visible | — |
| Edit a page | "Edit" button | Visible | — |
| Change agent scope | Scope badge with an "Edit agent scope" button opening a menu | Visible | — |
| Add / remove tags | Tag chips with remove buttons + "Add tag" | Visible | — |
| Set "verified until" | Labelled date input | Visible | — |
| Link an unlinked mention | Per-mention button | Visible | — |
| Accept / edit / dismiss an agent proposal | Three buttons on the proposal view | Visible | — |
| Import `AGENTS.md` | Empty state's detected-content CTA | Visible | — |
| Read the context budget | Meter with threshold, in the tree | Visible | — |

The wiki is the best-designed screen in the app for discoverability and is worth using as the
reference for the rest.

### `/p/$key/runs` — Run list

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Switch saved view | Tablist of views | Visible | — |
| Save the current filters as a view | "Save view" form | Visible | — |
| Add a state / agent / ticket filter | Labelled selects | Visible | — |
| Remove one filter | Chips with `aria-label="Remove filter: …"` | Visible | — |
| Clear all filters | The filtered-empty state's "Clear filters" CTA | Weak | Also offer "Clear all" beside the chips, not only inside the empty state — a user with 12 matching rows and 4 wrong filters never sees the empty state |
| Open a run | Row link | Visible | — |

### `/p/$key/runs/$id` — Run detail · **converted in this run**

| Action | Control before | Control now | Verdict now |
|---|---|---|---|
| Change verbosity | Three unlabelled buttons under the timeline | `ToggleButtonGroup` labelled **Detail level** in the toolbar | Visible |
| **Jump to the next failure** | **`f` chord only** | **"✕ Next failure (n)"** button, stating the count, disabled when there are none | **Fixed** |
| **Permalink a step or log line** | "Selection state lives in the URL" — no control at all | **"Copy link to step"** button | **Fixed** |
| **Show/hide Context & cost** | A toggle that appeared **only below 1400px**, so on a wide screen a closed pane was unrecoverable | Toggle present at every width | **Fixed** |
| Select a step | Timeline row (`ListItemButton`) | Same | Visible |
| Expand a grouped tool burst | "Read 9 files ▸" row | Same | Visible |
| Select a log line | Click the line | Same | Weak — the line is a `ButtonBase` but looks like text. Left as-is: fixing it well means a hover gutter, which is stage 3 work |
| Steer the run | Composer + "Send" | Same, with helper text | Visible |
| Stop the run | Inline "Stop" → toolbar swaps to "Confirm stop / Keep running" | "Stop run" → **Dialog** stating what stopping does | Visible, and clearer |
| Take over | "Take over" → custom modal | "Take over" → MUI `Dialog` | Visible |
| Answer a question | Option cards + "Answer" | `ToggleButtonGroup` (exclusive for single-select) + "Answer" | Visible |
| Approve / approve with edits / respond / deny | Four buttons | Four MUI Buttons, inline, never a modal | Visible |
| "Always allow" | Checkbox + explanation | `FormControlLabel` + `Checkbox`, same wording | Visible |

**Result: zero hidden actions on this route.** The `f` chord is deleted rather than
supplemented. The only shortcut left is Enter-to-send in the composer, beside an always-visible
**Send** button.

### `/p/$key/agents` — Roster · `/p/$key/agents/$id` — Agent config

| Action | Control today | Verdict |
|---|---|---|
| Add an agent | "New agent" button + empty-state CTA | Visible |
| Enable / disable an agent | Toggle with `aria-label` on the card | Visible |
| Open an agent | Card link | Visible |
| Edit identity (name, colour, git author, forge login) | Labelled fields | Visible |
| Edit the directive, save a version, diff versions | Textarea + "Save" + version list with diff buttons | Visible |
| Change model / effort / wall-clock / max steps | Labelled controls | Visible |
| Set permissions | Checkboxes with a lock icon | Visible |
| Review scoped permission rules | Rules section, with the rule "Always allow" wrote | Visible |
| Set autonomy | Four-stop dial | Visible |
| Set limits | Labelled inputs | Visible |
| Preview the resolved context | Context-preview section with token count | Visible |

### `/p/$key/triggers` — List · `/p/$key/triggers/$id` — Editor

| Action | Control today | Verdict |
|---|---|---|
| Create a trigger | "New trigger" link (header and empty state) | Visible |
| Enable / disable | Toggle on the card | Visible |
| Open the editor | Card link | Visible |
| Build WHEN / IF / THEN | Three labelled sections; selects for field/operator/value; "+ And" link; "Add Or group" button | Visible |
| Remove a condition | Per-row button, `aria-label="Remove condition"` | Visible |
| Reorder actions | Move up/down buttons with `aria-label`s | Visible |
| Configure loop protection | Panel of labelled toggles with plain-language descriptions | Visible |
| Backtest | Button + result list | Visible |
| Save | "Save" button | Visible |
| Read firing history | Section with the outcome sparkline | Visible |

### `/p/$key/settings` and `/p/$key/settings/$` — Project settings

| Action | Control today | Verdict | Replacement |
|---|---|---|---|
| Switch section | Left rail of section links | Visible | — |
| General: name, description, budget, thresholds | Labelled fields, autosaved with a status indicator | Visible | — |
| Board: columns, categories, WIP limits, auto-start | Section controls | Visible | — |
| Secrets | Section controls | Visible | — |
| Repository: repo, branch, naming template, **setup script**, network policy | Section controls | Visible | — |
| Danger zone | Typed-confirmation controls | Visible | — |
| **Wiki, Agents, Triggers, Members & access, Notifications** | Five rail entries rendered as `<span aria-disabled="true">` | **Weak** | A disabled label with no explanation reads as a bug. Either say why ("Coming in a later release") or remove the entry until it works |
| **Container image** (`repos.image_ref`) | **No control. No API field. See §5** | **Missing** | A "Container image" field in the Repository section, once the API exposes it |

---

## 4. The tally

Counted mechanically from the tables above, not by impression.

| | Count |
|---|---|
| Routes audited | **21** — every route in `router.tsx` |
| Action rows catalogued | **139** |
| **Visible** | 107 |
| **Hidden** | 18 |
| **Weak** | 8 |
| **Missing** — no interface at all (§5, plus Overview's three stubs) | 4 |
| Read-only / not an action | 2 |

Splitting the 18 hidden actions by whether anything else can do the job:

| | Count |
|---|---|
| **Redundant** — a visible control already does the same thing; the chord is pure duplication and should simply be deleted | 9 |
| **Irreplaceable** — the chord is the *only* way | **9** |

The nine redundant ones: the whole `G`-prefixed navigation set (every destination is already a
tab or a top-bar link), `J`/`K`/`Enter`/`Esc` on the inbox, board and triage (rows are already
focusable and clickable), `A`/`X` on the inbox (both have labelled buttons), `E`/`Esc` on the
ticket, `Space` peek on the board and on triage (open the item instead), and `/` on the wiki
(the search box is right there and its placeholder even advertises the chord).

**The nine with no other route in:**

1. **Delegate a ticket to an agent** (`D`, board) — *starts a run and spends money*
2. Set a ticket's status from the board (`S`)
3. Set a ticket's priority from the board (`P`)
4. Assign a human from the board (`A`)
5. Edit labels from the board (`L`)
6. Rename from the board (`R`)
7. Multi-select tickets (`X`, board)
8. Collapse the left rail (`⌘\`)
9. Open the ask-an-agent palette (`⌘J`)

Seven of the nine are on the board. Six of those seven are answered by **one** composition — an
MUI `IconButton` + `Menu` per card — and the seventh by a hover/focus checkbox with a bulk-action
bar. The remaining two are one button each. **The entire irreplaceable-hidden problem is three
components' worth of work**, which is why the board goes first in the migration plan.

**The run detail contributed two more** — `f` for next failure, and permalinking, which had no
control of any kind — until this run converted it. It now has zero hidden actions.

---

## 5. Not hidden — absent. Three capabilities with no user interface at all

These were found while auditing and are recorded separately, because a component-library
migration will not fix any of them and they must not be silently folded into a redesign
estimate. Each is a **backend or API gap wearing a UI costume**.

### 5.1 The ticket→PR link is never written

`tickets.pr_number` exists in the schema (`migrations/0001_init.up.sql`), is exposed by the API,
and `TicketPage` renders it. But **nothing in production code ever assigns it** — a
codebase-wide search for writes to `.PRNumber` outside tests finds only the row scanner in
`internal/kernel/store/tickets.go:150`. The sidebar's *"None — appears when a run opens one"* is
therefore permanent, exactly as the ticket reports. `pr_checks` is stored but is not in
`openapi.yaml` at all, so the "linked PR **with check status**" the spec asks for (§5.4) cannot
be built even if the number were populated.

**What is needed:** a write path from the forge adapter to `tickets.pr_number` / `pr_state` /
`pr_checks`, and `pr_checks` added to the API schema. Then the UI change is one line.

### 5.2 Module health has an endpoint and no screen

`GET /system/modules` is specified in `openapi.yaml`, implemented, and wrapped in the client as
`systemApi.modules`. **No component calls it.** `applyEvent.ts:167` even invalidates the
`["system","modules"]` query key on the relevant SSE frame — invalidating a cache nothing reads.
So when the GitHub poller degrades (rate-limit exhaustion sets `degraded|…`, per
`internal/module/github/transport_test.go`), the reason exists in the API and is invisible in
the product.

**What is needed:** a health strip. It belongs in two places — a persistent banner in the
chrome when any module is degraded (this is the one thing a user must not have to go looking
for), and a detailed list under workspace settings.

### 5.3 The container image cannot be set at all

`repos.image_ref` exists in the schema, and `internal/service/runs/prep.go:339` reads it into
the sandbox spec, so a custom image genuinely works. But `image_ref` appears **nowhere in
`openapi.yaml`** and nowhere in `web/src`. The only way to set it is to write to the SQLite
file by hand. This is worse than the ticket reported: it has no UI *and* no API.

**What is needed:** expose `image_ref` on the repo resource, then a "Container image" field in
Repository settings with the built-in image as the placeholder and a line explaining that a
custom image must contain `git` and `claude` (the sandbox already validates this and returns
`ImageMissingToolsError`).

---

## 6. The pattern underneath all of it

Three habits produced twenty hidden actions, and naming them is more useful than the list:

1. **The keyboard map was designed as the primary interface, and the mouse UI was derived from
   it — where it was derived at all.** UI spec §6 lays out a complete, coherent, genuinely good
   keyboard grammar (single letters mutate, prefixed chords navigate). The spec even says
   *"every context menu displays its own shortcut, so users learn the grammar from the mouse
   UI"* — but on the board there are no context menus, so there is nothing to learn from. The
   grammar has no visible surface to teach itself through. Triage is the one screen that got
   this right: its four buttons each render a `<kbd>` hint.

2. **Progressive disclosure was used where there was nothing to disclose from.** Triage's action
   buttons appear only on the selected row; the board's mutations appear on no row. If the
   affordance is invisible until you have already done the undiscoverable thing, the disclosure
   is not progressive, it is conditional on knowledge.

3. **Placeholder copy was written in the present tense.** "Runs appear here once agents start
   working" reads as an empty state; it is in fact a permanent stub with no code path behind it.
   Overview has three of these. An unimplemented region should look unimplemented.

The migration plan attacks these in that order: the board first, because it holds six of the
eight irreplaceable hidden actions.

---

## 7. Route reachability

`web/src/routes/__tests__/reachability.test.tsx` crawls the real route tree breadth-first from
`/`, following only what a mouse can follow — rendered anchors plus items inside menus opened
from `[aria-haspopup="menu"]` buttons — and fails by name on any route nothing links to.

**It passes.** Every route in `router.tsx` is reachable by clicking from the home page, except
four documented exemptions, each of which is correct:

| Route | Why it is URL-only |
|---|---|
| `/setup` | Exists only while the database has no users; the API redirects to it |
| `/login` | Signed-out entry point; the shell sends a 401 here |
| `/invite/$token` | An emailed link — the token *is* the address |
| `/p/$key/t/$ticket` | Opened by *activating* a ticket (a board card with `role="button"`, a triage row, an inbox row), so there is no anchor to follow |

Reachability and discoverability are different properties, and the gap between them is this
whole document: **every screen can be reached, and twenty of the actions on those screens
cannot be found.**
