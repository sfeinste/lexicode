# UI redesign — staged migration plan

**Date:** 25 August 2026 · LEXI-13 · Implements the [D-1 amendment](00-decisions.md#amendment-lexi-13--a-component-library-after-all)
**Inputs:** [../design/ui-library-evaluation.md](../design/ui-library-evaluation.md) ·
[../design/ui-discoverability-audit.md](../design/ui-discoverability-audit.md)

---

## 1. The rule every stage obeys

> **Every stage ends with `make check` green and the app usable.**

This is not aspiration; it is what makes the plan safe to stop halfway through. It holds because
of three properties established by the proof of concept:

1. **The theme is scoped, not global.** `MuiThemeProvider` is mounted per converted screen, not
   at the app root, and it uses `ScopedCssBaseline` rather than MUI's global `CssBaseline`. An
   unconverted screen renders exactly as it did before — its CSS modules are untouched and MUI's
   resets never reach it.
2. **The theme is derived from `tokens.css`, not a replacement for it.** `styles/muiTheme.ts`
   parses the same stylesheet the contrast test parses. Converted and unconverted screens
   therefore cannot drift in colour, and `tokens.contrast.test.ts` keeps guarding both.
3. **The status vocabulary, the routes and the query layer are out of scope.** Nothing in
   `lib/api`, `lib/sse`, `app/router.tsx` or `components/StatusDot` changes. A conversion
   rewrites JSX and deletes CSS; it does not touch data flow. The *existing* tests are
   likewise untouched — but each stage ADDS one, below.

**A stage is done when:** `make check` passes; the axe suite reports zero critical violations on
the converted routes; `reachability.test.tsx` still crawls to every route; the converted screen
has no action reachable only by keyboard; **each converted screen ships a test that names its
controls by accessible name and asserts the route registers no route-scoped key binding**
(`routes/project/runs/runDetailControls.test.tsx` is the model — see stage 0); and the
screenshots are regenerated.

Why that test is not optional: axe passes on this screen and always did. axe checks markup
validity, and every defect the redesign is meant to fix — an action with no control, a control
whose label is a bare glyph, `role="alert"` on a container that is only being *displayed* — is
valid markup. Nothing but a behavioural assertion catches those.

---

## 2. Sizing

| | Lines |
|---|---|
| `web/src` total (ts + tsx + css) | 41,720 |
| of which generated (`lib/api/types.gen.ts`) | 6,018 |
| of which tests (50 files) | 6,326 |
| **Hand-written app code in scope** | **~29,400** |
| `*.module.css` (the part that mostly disappears) | 7,118 |

The proof of concept converted the run detail: ~1,800 lines of TSX rewritten, 1,108 lines of CSS
deleted, one new theme module, one routing shim. That is the unit of measurement used below.

---

## 3. Order — and why

The route tree is **not** the right order. Two forces override it:

- **Discoverability debt.** Seven of the nine irreplaceable hidden actions are on the board
  (audit §4). Fixing them is the point of the exercise, so the board goes early.
- **The shared-component graph.** Converting a screen pulls in whatever it renders. The run
  detail's elicitation renderers are embedded in the home needs-you card and in inbox rows, so
  converting the run detail already pushed MUI into two unconverted screens (handled with a
  nested provider). Shared components must therefore be converted *with* or *before* their
  second consumer, never after.

The dependency edges that matter:

```
StatusDot ─────────► everywhere            (never converts — product semantics, architecture §13)
CostChip ──────────► run list · run detail · project header
ContextMeter ──────► wiki · agent detail · run detail
EmptyState ────────► every list screen
Editor ────────────► ticket · wiki · agent directive
RunSessionCard ────► ticket stream · run list · overview
InlineElicitation ─► home · inbox · run detail        ← already converted
```

`EmptyState` and `Editor` are the two that force ordering: `EmptyState` appears on almost every
list screen, and `Editor` is the single largest shared component.

---

## 4. The stages

### Stage 0 — foundation ✅ **done in this run**

The theme derived from `tokens.css`, the scoped provider, the router shim, the screenshot
harness, and one converted screen proving the shape works.

**Shipped:** `styles/muiTheme.ts`, `styles/MuiThemeProvider.tsx`,
`components/RouterLink/RouterLink.tsx`, the converted run detail (`RunDetailPage.tsx`,
`renderers.tsx`, `Intervention.tsx`, `InlineElicitation.tsx`),
`routes/project/runs/runDetailControls.test.tsx`, `web/screenshots/*`, `design/screenshots/*`.
**Cost:** this run.
**Verification:** `tsc`, `eslint`, `vitest` (50 files / 458 tests) green; axe clean; both themes
rendered and screenshotted. *Go half of `make check` not runnable in this container — no Go
toolchain (see §7).*

`runDetailControls.test.tsx` is what makes this stage's claims regressions rather than prose:
it renders the route over the real route tree and asserts each promoted control **by its
accessible name**, that the route registers no route-scoped key binding at all, that "Copy
link to step" copies a link naming the selected step *on the landing state* (where the
selection is `defaultSelection()` and the URL is bare), that nothing is announced
assertively — MUI's `Alert` defaults to `role="alert"` and this screen uses it as a styling
container — that Stop is a Dialog which stops nothing until confirmed, and that the empty
state says what to do next. **Every stage below inherits this rule**: a converted screen ships
with a test that names its controls, or the discoverability claim is unguarded.

---

### Stage 1 — the chrome and the shared primitives · **~2 runs**

Convert `AppShell`, `TopBar`, `LeftRail`, `ProjectLayout`, plus `EmptyState`, `TabBadge`,
`SaveStatus`, `ScopeBadge`, `CostChip`, `ContextMeter`.

**Why first, before any more screens:** the chrome is on every route, so converting it once
removes the need for per-screen nested providers — `MuiThemeProvider` hoists to `AppShell` and
the `InlineElicitation` workaround comes out. The shared primitives go with it because every
later stage renders them, and converting them later means touching every screen twice.

**Discoverability fixed here** (audit §2): a visible **Collapse sidebar** toggle replacing `⌘\`;
an **Ask an agent** button in the top bar replacing `⌘J`; and the whole `G`-prefixed navigation
set **deleted** — nine chords that duplicate visible tabs and links.

**Also here:** the degraded-module banner (audit §5.2) — `GET /system/modules` already exists
and nothing renders it. This is the one health signal a user must not have to go looking for.

**Risk:** highest blast radius in the plan. Everything renders inside the chrome, so a mistake
here is visible on all 21 routes. It is also the only stage where `CssBaseline` becomes global,
which will shift typography by a pixel or two app-wide. Budget a full run for visual reconciling.

---

### Stage 2 — the board · **~3 runs**

Convert `BoardPage`, `TicketCard`, `NeedsYouLane` and the five picker overlays.

**This is the stage that pays for the project.** Seven of the nine irreplaceable hidden actions
live here, and they collapse into three compositions:

| Replaces | Composition |
|---|---|
| `S` `P` `A` `D` `L` `R` — six chords | One `IconButton` + `Menu` per card: **Status ▸ · Priority ▸ · Assignee ▸ · Delegate & run ▸ · Labels ▸ · Rename** |
| `X` multi-select | `Checkbox` on card hover/focus + a bulk-action `Toolbar` when any card is selected |
| `J` `K` `Space` `Esc` | Deleted — cards are already focusable, and "peek" is an expert optimisation that costs a whole interaction mode |

**Delegate & run** gets special wording, because it is the one action in the product that spends
money and it is currently bound to one unlabelled keypress.

**Open question this stage must settle, not guess:** drag and drop. MUI ships none. The board
currently uses native HTML5 drag events (~60 lines, no dependency). The options are keep it,
or adopt `@dnd-kit` for accessible keyboard-and-pointer dragging. **The evaluation deliberately
does not pick** — the choice depends on whether the per-card menu makes drag optional, which is
only knowable once the menu exists. Decide at the start of this stage, in a one-paragraph
amendment here.

**Risk:** the board has the most tests of any screen (`badges`, `grouping`, `wip`,
`keymap`, `layoutParity`, `delegateStartsRun`). `layoutParity` and `delegateStartsRun` assert
behaviour and should survive untouched; `keymap.test.ts` tests chords that this stage deletes,
so it shrinks to cover only what remains. **That is deletion of a test for a deleted feature,
which needs to be called out explicitly in the PR, not slipped in.**

---

### Stage 3 — runs, triage, inbox · **~2 runs**

Convert `RunsPage` (finishing the runs area and retiring `runs.module.css` entirely),
`TriagePage`, `InboxPage`.

**Why together:** they are the three list-with-inline-actions screens, they share the same
patterns (`Table`, filter `Chip`s, row actions), and after stage 2's per-card menu the
composition is already designed.

**Discoverability fixed here:** triage's four action buttons rendered on **every** row rather
than only the selected one; `Space` peek deleted on both screens; inbox's redundant chords
deleted; a **Clear all** control beside the run list's filter chips (today it exists only inside
the filtered-empty state, which a user with matching rows never sees).

---

### Stage 4 — home, overview, ticket detail · **~3 runs**

Convert `HomePage`, `OverviewPage`, `TicketPage` and `Editor`.

**`Editor` is the risk.** It is the largest shared component (ticket description, comments, wiki
body, agent directives) with slash commands and `@`-mentions. MUI has no rich-text editor — this
is a composition over `TextField`/`Popper`/`MenuList`, or a decision to keep the existing engine
and only re-skin its chrome. Prefer the latter: the mention engine has its own tests and no
discoverability problem.

**Discoverability fixed here:** the home projects table's rows become real links; the ticket
title gets an edit `IconButton` instead of a clickable `<h1>` with a tooltip; the ticket's
`E`/`Esc` chords are deleted.

**Also here — and it is not UI work:** Overview's three columns (recent runs, pinned pages,
activity) are permanent stubs that render placeholder copy unconditionally (audit §3). Either
implement them or remove them. Converting a stub to MUI produces a prettier lie.

---

### Stage 5 — wiki, agents, triggers, settings · **~4 runs**

Convert `WikiScreen`/`WikiIndexPage`/`WikiPagePage`/`ProposalView`/`BacklinksPane`,
`AgentsPage`/`AgentDetailPage`/`sections.tsx`,
`TriggersPage`/`TriggerEditorPage`/`TriggerForm`/`LoopPanel`/`BacktestPanel`,
`ProjectSettingsPage` and its five sections, `WorkspaceSettingsPage`, `AuditLogPage`,
`BootstrapPage`, and the three auth screens.

**Why last:** these are the most component-dense screens (the trigger editor alone is ~1,400
lines of forms) but the *least* discoverability-broken — the audit found essentially no hidden
actions here. They are a straight port with a real payoff in deleted CSS, and no user-facing
urgency. Doing them last means the earlier stages have already established every pattern these
screens need.

**Cleanup that lands here:** delete `lib/keyboard/*` and `KeyboardCheatsheet` once the last
chord is gone; decide the fate of `CommandPalette` (⌘K) — it is *not* on the hidden list because
the top bar advertises it with a visible button, and it is genuinely the best way to move around
a project. **Recommendation: keep ⌘K, delete everything else.** One discoverable, advertised
accelerator is a feature; forty-three undiscoverable ones are the problem this ticket is about.

**Also here:** the two remaining backend gaps from audit §5 — expose `repos.image_ref` in
`openapi.yaml` and add a Container image field; add a write path for `tickets.pr_number` /
`pr_state` / `pr_checks` and turn the Linked PR row into a real link with check status. **Both
are Go changes with a UI consequence, not UI changes.** Size them separately.

---

## 5. What it costs to convert the rest

The owner's decision point. Estimates are in *runs* — one agent run of the size of this one.

| Stage | Screens | ~TSX to rewrite | ~CSS deleted | Runs | Cumulative |
|---|---|---:|---:|---:|---:|
| 0 · foundation + run detail | 1 | 1,800 | 1,108 | **done** | — |
| 1 · chrome + shared primitives | — | 1,500 | 1,100 | 2 | 2 |
| 2 · board | 1 | 3,000 | 1,200 | 3 | 5 |
| 3 · runs list, triage, inbox | 3 | 1,600 | 900 | 2 | 7 |
| 4 · home, overview, ticket, Editor | 3 | 3,400 | 1,300 | 3 | 10 |
| 5 · wiki, agents, triggers, settings, auth, bootstrap | 12 | 7,500 | 2,400 | 4 | **14** |

**Total: ~14 further runs** to convert all 21 routes, removing roughly **7,000 of the 7,118
remaining lines of CSS modules** and every one of the 18 hidden actions.

Four honest caveats on that number:

- **Each stage now also writes a controls test** (§1). Stage 0's is ~430 lines and took a
  fraction of a run; multi-screen stages need one per converted screen, so budget roughly
  +0.2 runs per stage — inside the rounding of the numbers above, not on top of them.

- **It excludes the backend gaps.** The three capabilities with no interface (audit §5) are Go
  work. Best guess 2–3 further runs, but they were not investigated in depth here and should be
  sized on their own.
- **Stage 2 is the least certain**, because of the drag-and-drop decision and because the board
  carries the most behavioural tests.
- **The estimate assumes stages run in order.** They can be reordered, but stage 1 must come
  first — converting more screens before the chrome means more nested providers and more rework.

### Stopping early

Every stage is a shippable end state, so the owner can stop at any of them:

| Stop after | What the product is |
|---|---|
| **Stage 0** (now) | One screen redesigned, the approach proven, the plan grounded. Everything else works exactly as before. |
| **Stage 2** | **The best value-per-run stopping point.** The chrome and the board are converted, and *every irreplaceable hidden action in the product is gone.* The rest of the app is stylistically inconsistent but nothing is undiscoverable. |
| **Stage 3** | Every high-traffic screen converted. The remainder are configuration screens that a user visits rarely. |
| **Stage 5** | Complete. |

If only one further decision is made, it should be **"fund through stage 2"**: it is 5 runs and
it closes the entire discoverability problem the ticket was written about.

---

## 6. What this plan does not do

- **It does not redesign the information architecture.** UI spec §1's routes, tabs and
  project-as-container model survive intact; the reachability test guards that. The redesign is
  of controls and affordances, not of navigation structure.
- **It does not touch the status vocabulary.** §4's run states and trigger outcome classes keep
  their colour + glyph + word rendering through `StatusDot`, guarded by
  `statusDotUsage.test.ts`.
- **It does not change the API.** Except where audit §5 says an API is missing entirely — and
  those are called out as separate, separately-sized work.
- **It does not delete `tokens.css`.** The token ladder is the source the MUI theme derives
  from, and `tokens.contrast.test.ts` remains the enforcement of §10's contrast floors.

---

## 7. Verification status of stage 0

| Check | Status |
|---|---|
| `tsc -b --noEmit` | Passes |
| `eslint .` | Passes |
| `vitest run` | Passes — 50 files, 458 tests |
| axe-core, all 21 routes | Zero critical violations |
| Token contrast, both themes | Passes |
| Route reachability crawl | Passes |
| Run-detail controls (`runDetailControls.test.tsx`) | Passes — 7 cases; verified failing against the pre-fix code |
| `go build` · `go vet` · `golangci-lint` · `go test` | **Not run — this container has no Go toolchain** |

No Go source was modified, so the Go half of `make check` is expected to be unaffected. That
expectation is stated, not verified, and should be confirmed in CI.
