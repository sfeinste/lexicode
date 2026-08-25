# UI migration — Material UI, in stages

**Decision:** [D-1 amendment A-1](00-decisions.md#amendment-a-1-lexi-13-august-2026--reversed-the-ui-moves-onto-material-ui).
**Evidence:** [../design/ui-library-evaluation.md](../design/ui-library-evaluation.md) ·
[../design/discoverability-audit.md](../design/discoverability-audit.md).

Two goals, in this order:

1. **Self-discoverable.** Every action has a visible control with a readable label. The audit found
   15 that do not; this plan retires all of them and names the control for each.
2. **Self-explanatory.** Library conventions, so a newcomer's guesses are right.

Keyboard shortcuts all stay. Nothing is reachable *only* by keyboard.

## The rule every stage obeys

> At the end of every stage the app **builds**, **passes `make check`**, and is **usable**.

And one more, which `make check` cannot enforce for you:

> **Look at the converted screens, in both themes, before calling a stage done.**
> `cd web && npm run build && node scripts/screenshot.mjs`.

That is not ceremony. Every accessibility guarantee in this repository is structural — axe runs
over jsdom, which renders no colour; contrast is asserted arithmetically against `tokens.css`;
reachability is a crawl over the route tree. Nothing in `make check` looks at a pixel. Stage 0
shipped a verbosity switch whose unselected options were invisible in dark mode (Material defaults
`palette.action.active` to a literal `rgba(0, 0, 0, 0.54)`), and **every test in the repository
passed**. It was caught by a screenshot and fixed in `theme.ts`; `theme/theme.tokens.test.ts` now
fails if any `palette.action` slot is left on a Material literal. Expect one of these per stage.

That is achievable because MUI and the CSS modules coexist without a fight: the theme sits on the
root route, so a converted screen is themed wherever it renders, and an unconverted screen keeps
its stylesheet untouched. `CssBaseline` is deliberately **not** installed until the final stage —
adding Material's global reset while half the app is still on CSS modules would restyle every
unconverted screen at once, which is the exact thing staging exists to prevent.

Each stage is one branch, one PR, one `make check`.

## What "converted" means

Per the amendment: **every element is a library component, or a documented composition of library
primitives.** A composition is legitimate — MUI ships no log viewer, no diff hunk, no
application-layout component — but it must be named in the file header, with what it is built from
and why the library has no answer. Stage 0's files are the worked examples.

---

# Stage 0 — Foundation + the hardest screen · **MERGED**

**What landed**

| | |
|---|---|
| `web/src/theme/theme.ts` | The MUI theme. Palette entries are `var(--token)` references, so `tokens.css` stays the single source of truth and the contrast test keeps its teeth. `cssVariables: true` is load-bearing — see the evaluation. |
| `web/src/theme/routerLinks.tsx` | The MUI ✕ TanStack Router seam, closed once with the router's `createLink`. ~20 lines for the whole app. |
| `web/src/app/shell/RootLayout.tsx` | `ThemeProvider` on the **root route**, not in `App.tsx`, so the axe suite and the reachability crawl — which mount `routeTree` directly — see the real theme. |
| `StatusDot` · `CostChip` · `ContextMeter` · `LoopChain` | The shared status/cost primitives, rebuilt on library components. Four CSS modules deleted. |
| `/p/:key/runs/:id` | The run detail: `RunDetailPage` · `renderers` · `Intervention` · `InlineElicitation`. ~1,090 lines of CSS left `runs.module.css`. |
| `runDetail.mui.test.tsx` | Six tests pinning the converted screen's acceptance, including a sweep asserting **every route-scoped chord has a visible control** — mutation-checked: renaming the button fails the suite by name. |
| `theme/theme.tokens.test.ts` | Four tests pinning the token-by-reference arrangement: no `palette.action` slot may be left on a Material literal. Mutation-checked against the bug that motivated it. |
| `web/scripts/screenshot.mjs` | The screenshot harness. Drives `web/dist` — the bundle `go:embed` ships — in headless Chromium with the API stubbed at the network layer, and writes `design/screenshots/*.png`. Not in `make check`: it needs a browser binary. |

**Discoverability retired:** `f` (next failure) → a labelled button beside the verbosity switch.
**Defect found and fixed:** the verbosity switch's unselected options were illegible in dark mode
— MUI's `palette.action.active` default — and no test in the repository could see it. This is
where the "look at it" rule above comes from.
**Cost paid:** +96.6 kB gzip JS, −3.2 kB CSS. One-time.
**Verified:** `make check` green, and the screen rendered and inspected in both themes.

---

# Stage 1 — The board · **first, and not because it is biggest**

**Why first.** Ten of the fourteen remaining hidden actions are on this one screen, and six of them
are retired by a *single* new control. Nothing else in the plan has that ratio. The board is also
where the product's thesis lives — `D` for delegate beside `A` for assign — and today there is no
mouse path to it at all, which means a user who never presses `D` never meets the idea Lexicode is
built on.

Converting the board first also settles the one open technical question early: **native HTML5 drag
inside MUI components**. MUI provides no drag-and-drop, so the existing implementation carries over
unchanged — but "unchanged" needs proving on a `Paper`-based card before four more stages depend on
the answer.

**Files:** `BoardPage.tsx` (1,285) · `TicketCard.tsx` (149) · `NeedsYouLane.tsx` (54) ·
`board.module.css` (~1,000) · `EmptyState` · `RunNotice` · `TabBadge`.

**New controls** (from the audit's replacement column):

| Retires | Control |
|---|---|
| `S` `P` `A` `D` `L` `R` — status, priority, assignee, delegate, labels, rename | **One** `⋯` overflow `IconButton` per card (visible on hover, always on focus) opening a `Menu`, each `MenuItem` showing its own chord. Spec §6 asks for exactly this: "every context menu displays its own shortcut, so users learn the grammar from the mouse UI." |
| `X` — multi-select | A `Checkbox` in the card corner + a selection `Toolbar` naming the count and the bulk actions |
| `Space` — peek | Peek becomes what a **single click** does. Single click currently selects and shows almost nothing, so the affordance is free. |
| `Enter` / double-click — open | "Open" in the card menu, **and** the ticket key becomes a real `<a>` — which also retires the ticket-detail route's activation-only status (below) |
| `?view=backlog` — URL-only | A **All · Backlog** `ToggleButtonGroup` beside the layout toggle |
| — | `group_by`'s label becomes "Group by" instead of the API field name |

**Also in this stage:** teach `reachability.test.tsx` to key its crawl on pathname **+ declared
search params**, so a query-string-only view like `?view=backlog` can never be an orphan while the
suite is green. That blind spot is why this bug survived a test written specifically to catch it.

**Leaves the app usable:** yes — one screen, self-contained.
**Cost:** ~2,500 lines touched. **1 run** (≈2 engineer-days).
**Discoverability after this stage: 10 of 14 remaining hidden actions gone.**

---

# Stage 2 — The chrome

**Why second.** The chrome renders on every route, so converting it is the stage most likely to
disturb everything else. Doing it early means the remaining stages are built against the final
frame rather than being re-checked against it later. It also retires the last two hidden actions.

**Files:** `AppShell` · `TopBar` · `LeftRail` · `ProjectLayout` · `shell.module.css` (1,110 total) ·
`CommandPalette` · `AskAgentPalette` · `KeyboardCheatsheet` (~700).

Menus become `Menu`/`MenuItem`, tabs become `Tabs`/`Tab`, the palettes become `Dialog` +
`Autocomplete`, the cheatsheet becomes a `Dialog` + `Table`.

| Retires | Control |
|---|---|
| `⌘\` — collapse the rail (palette-only) | A chevron `IconButton` on the rail's edge, `aria-label="Collapse sidebar"`, chord in the tooltip |
| `⌘J` — ask an agent (palette-only) | A second top-bar button beside the search one: "⌘J ask an agent" |
| — | The `◔` notifications glyph gets a real icon or a text label |
| — | The duplicated `⚙` (header **and** eighth tab) resolves to one |

**Leaves the app usable:** yes — the chrome is structural; unconverted screens render inside it
unchanged.
**Cost:** ~1,800 lines. **1 run** (≈1.5 engineer-days).
**Discoverability after this stage: 12 of 14 gone. The two that remain need an API change or a
ticket-detail link, both below.**

---

# Stage 3 — The list screens

**Why third.** Home, inbox, triage and the run list are the same shape — dense rows, filters, empty
states — so they share the conversion patterns and are cheapest done together. It is also where the
`Table`-with-32px-rows density fight gets settled once, in the theme, for every table after it.

**Files:** `HomePage` + `CreateProjectDialog` (753) · `InboxPage` (564) · `TriagePage` (929) ·
`RunsPage` + the rest of `runs.module.css` (520) · `RunSessionCard` (300).

| Retires | Control |
|---|---|
| `Space` — peek on triage | Same as the board: peek is what a single click does |
| Home's projects table row (Weak) | Rows become real links — middle-click, right-click, copy address |
| The run list's status-cell-only link (Weak) | The whole row becomes the link |

**Leaves the app usable:** yes — four independent screens; can even split across two PRs if the
diff gets uncomfortable.
**Cost:** ~3,100 lines. **1–2 runs** (≈3 engineer-days).
**`runs.module.css` disappears entirely at the end of this stage.**

---

# Stage 4 — Ticket detail and the wiki

**Why fourth.** These two share the `Editor` (313 lines + 96 CSS), the app's most intricate shared
component: slash commands, `@`-mentions, and the selection-to-sub-tickets gesture that spec §5.4
calls the primary agent→human handoff. It converts once, here, and both screens depend on it.
Doing it after the list screens means the patterns are established before touching the fiddliest
component in the codebase.

**Files:** `TicketPage` + `Composer` + `DescriptionSection` + `ticket.module.css` (2,175) ·
the wiki screens + `wiki.module.css` (2,190) · `Editor` + `MarkdownView` (~560) · `ScopeBadge` ·
`VerifiedChip`.

| Retires | Control |
|---|---|
| `/p/:key/t/:ticket` reachable only by *activating* a card | Landed in stage 1 (the ticket key becomes a link); this stage removes the `URL_ONLY` exemption from `reachability.test.tsx` and lets the crawler prove it |

**Risk to name:** the `Editor` has its own placement test suite (`Editor.placements.test.tsx`)
asserting it behaves identically in all four placements. That suite is the safety net; if it needs
rewriting rather than passing, the conversion is wrong.

**Leaves the app usable:** yes, but this is the stage most likely to need two PRs — Editor first,
then its consumers.
**Cost:** ~4,900 lines. **2 runs** (≈4 engineer-days).
**Discoverability after this stage: 14 of 14 hidden actions retired.**

---

# Stage 5 — The form screens

**Why last of the conversions.** Settings, agents and triggers are the largest remaining surface
and the *least* broken: the audit found **zero** hidden actions across all of them. The trigger
editor in particular is the best screen in the app for the ticket's stated goal — it reads as
prose, every control is labelled in words, and the loop-protection panel explains itself. There is
little discoverability to win here, so it goes last on purpose: it is the stage the owner can defer
or drop without losing the point of the exercise.

**Files:** `WorkspaceSettingsPage` + `AuditLogPage` + `CredentialsSection` (1,060) · project
settings, six sections (1,893) · agents (1,510) · triggers (2,849) · overview + bootstrap (1,173) ·
auth screens (334) · `SecretsPanel` · `InheritedField` · `SaveStatus`.

Mostly mechanical: labelled inputs → `TextField`, selects → `Select`, checkbox groups →
`FormGroup`/`FormControlLabel`, section rails → `Tabs orientation="vertical"`, the danger zone →
`Dialog` with typed confirmation.

**Leaves the app usable:** yes — splits cleanly into three PRs (workspace settings; project
settings + overview + bootstrap; agents + triggers + auth).
**Cost:** ~8,800 lines. **3 runs** (≈6 engineer-days).

---

# Stage 6 — Close it out

1. **Install `CssBaseline`** and delete the parts of `reset.css` it supersedes. Safe only once no
   CSS module remains — which is why it is here and not in stage 0.
2. **Delete the last CSS modules.** ~6,900 lines remain today; the target is `tokens.css` and a
   thin `reset.css`, nothing else.
3. **Recheck the bundle.** Expected steady state ≈ **+76 kB gzip** over the pre-MUI baseline, as
   the returned CSS offsets part of the one-time +96 kB.
4. **Re-run the audit** and update `design/discoverability-audit.md` with the new counts. The audit
   is the acceptance test for the whole programme, not a one-off document.

**Cost:** ~500 lines. **1 run** (≈1 engineer-day).

---

# Two capabilities that need a story, not a conversion

Found by the audit; **not** fixed by any stage above, because neither is a styling problem.

### A. The container image (`image_ref`) has no API and no UI

`ImageRef` exists in `internal/domain/repo.go`, is pulled by `internal/module/docker/image.go`,
is validated for `git`/`claude` on PATH by `sandbox.go`, and is read and written by
`internal/kernel/store/repos.go`. There is **no field in `openapi.yaml`** —
`internal/service/bootstrap/reposettings.go:13` says so in a comment: *"image_ref is deliberately
NOT here: it has its own story to be reported separately."*

The only way to set a custom container image today is to edit the SQLite row by hand.

**Work:** add the field to `RepoSettings` in the OpenAPI schema and the service, regenerate
`types.gen.ts`, and add a field in project settings → Repository beside the setup script, surfacing
the existing `ImageMissingToolsError` inline. **≈1 run (1 engineer-day).** Touches Go, so it is a
separate ticket from the UI migration.

### B. Module health has an API and an SSE event, and no screen

`GET /api/v1/system/modules` exists, `systemApi.modules` is in the generated client, and
`applyEvent.ts:166` invalidates `["system","modules"]` when a `module.degraded` frame arrives. But
`systemApi.modules` **has no caller anywhere in `web/src`** — the invalidation refreshes a query no
screen subscribes to. When the GitHub poller degrades, the reason exists only for someone running
`curl`.

**Work:** a status strip in the project header and on `/settings`, rendering only when a module is
degraded, with the server's reason verbatim through `StatusDot` so it joins the §4 vocabulary
instead of inventing an eleventh state. **≈half a run.** Frontend only; fits inside stage 2.

---

# The whole cost, so the owner can decide

| Stage | Scope | Lines | Runs | ≈ Days | Hidden actions retired |
|---|---|---|---|---|---|
| **0** | Theme, shared primitives, run detail | ~2,000 | — | — | 1 · **merged** |
| **1** | Board | ~2,500 | 1 | 2 | **10** |
| **2** | Chrome + palettes (+ module health) | ~1,800 | 1 | 1.5 | 2 |
| **3** | Home, inbox, triage, run list | ~3,100 | 1–2 | 3 | 1 |
| **4** | Ticket detail, wiki, Editor | ~4,900 | 2 | 4 | 1 |
| **5** | Settings, agents, triggers, auth | ~8,800 | 3 | 6 | 0 |
| **6** | CssBaseline, last CSS, re-audit | ~500 | 1 | 1 | 0 |
| | **Remaining after stage 0** | **~21,600** | **9–10** | **17.5** | **14** |
| A | Container image (needs an API change) | — | 1 | 1 | — |
| B | Module health screen | — | 0.5 | 0.5 | — |

*Calibration: stage 0 converted ~2,000 lines of source and deleted ~1,340 lines of CSS in a single
run, including the research and this plan. The per-stage estimates use that rate, discounted for
stage 4's `Editor` (intricate, well-tested) and stage 5's bulk (repetitive, low-risk).*

### Three honest ways to stop early

The stages are ordered so that stopping is a real option, not an abandonment:

- **Stop after stage 2** (~2.5 more runs, 3.5 days). **12 of 14 hidden actions gone**, including
  every one on the board. The two most-used screens and the chrome are on the library; settings and
  the wiki still look like today. This is where the discoverability curve flattens — it buys 86% of
  the audit's value for about 20% of the remaining cost, and it is the recommendation if the
  question is "what is the least I can do and still fix the problem the ticket describes".
- **Stop after stage 4** (~5 more runs, 10.5 days). Every hidden action retired, every daily-use
  screen converted. Only the form screens — the ones the audit found nothing wrong with — remain on
  CSS modules. The app is visually mixed, which is a real cost in polish and a small one in
  correctness.
- **Go to stage 6** (~9–10 runs, 17.5 days). One system, one styling mechanism, ~6,900 lines of CSS
  deleted, and the bundle settles at +76 kB.

### What is *not* worth doing

Converting the trigger editor for its own sake. It is the largest single screen left (2,849 lines)
and the audit rates it the best-designed one in the app. It is in stage 5 for consistency, and
consistency is the only reason.
