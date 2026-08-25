# Component library evaluation — Lexicode

**Date:** 25 August 2026 · LEXI-13 · Supersedes the visual half of [ui-ux-spec.md](ui-ux-spec.md)
**Companion documents:** [ui-discoverability-audit.md](ui-discoverability-audit.md) ·
[../plan/06-ui-redesign-plan.md](../plan/06-ui-redesign-plan.md) ·
[../plan/00-decisions.md](../plan/00-decisions.md) D-1 amendment

---

## 1. What is actually being decided

Decision D-1 said: *"No component library; the spec's component list (§7) is the component
library."* Nine months and roughly 39,000 lines of `web/src` later, that produced a UI which is
capable and unreadable — see the [discoverability audit](ui-discoverability-audit.md) for the
count. The owner is reversing the decision. This document picks the replacement.

The choice is **not** "which library is best". It is "which library carries *this* product":

| Requirement | Where it comes from | Why it is hard |
|---|---|---|
| Dense data, 28–32px rows | UI spec §2.2 | Most libraries' defaults are 36–48px and built for marketing pages |
| Live updates over SSE | architecture §12 | Rows mutate under the user; anything that re-mounts on every frame is unusable |
| A board with drag and drop | §5.3 | Almost no component library ships DnD |
| A three-pane run detail | §5.7 | No mainstream library ships a split-pane |
| Virtualised lists, hundreds of rows | §5.7 (500-step acceptance) | Almost no component library ships a virtualiser |
| Light **and** dark, both first-class | §3 | Runtime-CSS libraries do this well; static-stylesheet libraries less so |
| Colour is never the sole status carrier | §10 | Needs a theming system that can express a semantic palette, not just "primary/secondary" |
| Contrast ≥ 4.5:1 body, ≥ 3:1 glyph | §10 | Must be able to override the library palette wholesale |

And one constraint that changes the weighting from a typical web app: **Lexicode is a single
binary serving an embedded SPA to localhost** (D-1). There is no CDN, no cold-start-over-3G,
no bundle budget imposed by a business metric. Bundle size matters here as *build and parse
time on a laptop*, not as a revenue lever. It is a tiebreaker, not a gate.

---

## 2. Method — what was measured, and what was judged

Two of the four requested comparison axes were measured directly in this container rather than
quoted. Being explicit about which is which:

**Bundle size — measured.** A probe app per library imports a component set representative of
what Lexicode needs (app frame, buttons, a table, tabs, a dialog, text field, select,
chip/badge, tooltip, menu, toggle group, alert, spinner, theme provider), then Vite builds it
in library mode with `NODE_ENV=production`, React/React-DOM externalised, esbuild minification
and the library's own stylesheet imported where it ships one. The numbers below are gzipped
bytes of that output. They are therefore *tree-shaken, comparable, and specific to this
product's component set* — not the "minzipped whole package" figures published on bundle-size
sites, which for these libraries run 2–5× higher.

**Maintenance health — measured.** Pulled from the npm registry on 25 Aug 2026: latest version,
its publish date, releases in the preceding twelve months, weekly downloads, declared licence,
and the direct dependency count.

**Theming and "can it serve the run-detail pane" — judged, then tested.** Judged from the
libraries' documented component inventories, then the winner was actually built: the run detail
is converted and running (§6). That conversion is the strongest evidence in this document,
because it is the only claim here that was executed rather than reasoned about.

**Accessibility record — partly judged.** Nobody can audit seven libraries' a11y histories from
a container. What is stated below is (a) the architectural basis each library rests on, which is
a fact, and (b) for the winner only, an actual axe-core run over the converted screen, which
reports **zero critical violations**. Where the assessment is reputational, it says so.

---

## 3. The candidates, measured

Seven libraries were probed. Four are compared in depth (§4); the other three are recorded
here because ruling them out quickly is itself a finding.

### Bundle — gzipped, representative component set

| Library | JS (gz) | CSS (gz) | **Total (gz)** | Note |
|---|---:|---:|---:|---|
| **MUI** `@mui/material` 9.3.1 | 119.2 KB | 0 | **119.2 KB** | styles generated at runtime by Emotion |
| react-aria-components 1.20.0 | 123.5 KB | 0 | **123.5 KB** | **unstyled** — buys behaviour, no visuals |
| Mantine `@mantine/core` 9.5.2 | 92.3 KB | 33.8 KB | **126.1 KB** | CSS file is all-or-nothing |
| Fluent UI `@fluentui/react-components` 9.74.7 | 133.6 KB | 0 | **133.6 KB** | Griffel atomic CSS-in-JS |
| Chakra `@chakra-ui/react` 3.36.1 | 136.4 KB | 0 | **136.4 KB** | Emotion + Ark UI |
| Radix Themes `@radix-ui/themes` 3.3.0 | 64.6 KB | 79.5 KB | **144.1 KB** | smallest JS, largest CSS |
| Ant Design `antd` 6.6.1 | 294.7 KB | 0 | **294.7 KB** | 2.5× the field |

Two things this table says that a single "bundle size" number cannot:

- **Mantine and Radix Themes ship one indivisible stylesheet.** You pay 33.8 KB / 79.5 KB
  gzipped whether you use six components or sixty; it does not tree-shake, because CSS
  cannot. For an app that will eventually use most of the library that is fine. For a *staged*
  migration it means the full CSS cost lands on the first converted screen.
- **Radix Themes has the smallest JS in the field and the largest total.** Picking on JS alone
  would have chosen it.

For scale: Lexicode's bundle before this work was **195.25 KB gz JS + 20.08 KB gz CSS**.

### Maintenance health — npm registry, 25 Aug 2026

| Library | Latest | Published | Releases (12mo) | Weekly downloads | Direct deps | Licence |
|---|---|---|---:|---:|---:|---|
| MUI | 9.3.1 | 2026-08-06 | 25 | 10,365,517 | 12 | MIT |
| Ant Design | 6.6.1 | 2026-08-17 | 47 | 3,765,959 | **48** | MIT |
| react-aria-components | 1.20.0 | 2026-07-31 | 268 | 4,102,840 | 7 | Apache-2.0 |
| Mantine | 9.5.2 | 2026-08-22 | 48 | 2,434,706 | 5 | MIT |
| Chakra | 3.36.1 | 2026-07-19 | 15 | 1,755,968 | 7 | MIT |
| Radix Themes | 3.3.0 | **2026-01-31** | **1** | 974,514 | 4 | MIT |
| Fluent UI | 9.74.7 | 2026-08-24 | 277 | 394,167 | **62** | MIT |

### Ruled out early, with the reason

- **Radix Themes** — one release in twelve months, last published 31 January 2026. Radix
  *Primitives* remain healthy and are the substrate under Chakra and others; the *Themes*
  layer, which is the part that would actually be Lexicode's component library, has gone
  quiet. Adopting a dormant styling layer as the foundation of a UI rewrite is the specific
  risk this evaluation exists to avoid. Its 79.5 KB unsplittable stylesheet is a distant
  second reason.
- **Base UI** `@base-ui-components/react` — still `1.0.0-rc.0`, published 4 December 2025, six
  releases in twelve months. It is MUI's own next-generation unstyled layer and is worth
  revisiting; it is not something to bet a rewrite on today. (This matters for the
  recommendation: MUI's own roadmap points here, so see §5's "where the winner is weak".)
- **PrimeReact** 11.1.0 — publishes no `repository` field and declares its licence as
  `SEE LICENSE IN LICENSE.md` rather than an SPDX identifier. For a project that ships a
  binary to other people's machines, a licence that cannot be resolved mechanically is a
  procurement problem before it is a technical one.

---

## 4. The four compared in depth

### 4.1 MUI (Material UI) — `@mui/material` 9.3.1

| Axis | Assessment |
|---|---|
| Bundle | **119.2 KB gz** — smallest total in the field |
| Theming | `createTheme` with a full palette, typography ramp, and per-component `styleOverrides` / `defaultProps`. Light and dark are first-class (`palette.mode`, or `colorSchemes` + CSS variables). The palette accepts arbitrary colours, so the §3 semantic ladder maps onto it directly |
| Accessibility | Components implement the WAI-ARIA patterns; focus management, `Dialog` focus trap, `Menu` roving tabindex and `ToggleButtonGroup` semantics are built in. **Measured:** axe-core reports zero critical violations on the converted run detail |
| Run-detail pane | Everything except the split-pane and the virtualiser. `Box` with CSS grid covers the former (documented); the latter is not a component at all (§5) |
| Maintenance | 25 releases/12mo, published 3 weeks ago, 10.4M weekly downloads — an order of magnitude more than anything else here. 12 direct dependencies |
| Component inventory | 130+ exported components |

### 4.2 Mantine — `@mantine/core` 9.5.2

| Axis | Assessment |
|---|---|
| Bundle | 92.3 KB JS (smallest of the styled libraries) **+ 33.8 KB of unsplittable CSS** = 126.1 KB |
| Theming | CSS-variable based, genuinely excellent; `MantineProvider` + `createTheme`, dark mode via `data-mantine-color-scheme`. Arguably the cleanest theming story of the four |
| Accessibility | Good and improving; built on Floating UI and `react-remove-scroll`. Reputational, not measured here |
| Run-detail pane | Same gaps as MUI (no split-pane, no virtualiser), plus it *does* ship `Spotlight` — a command palette, which is a component Lexicode currently hand-rolls |
| Maintenance | 48 releases/12mo, published 3 days ago, 2.4M weekly. Only 5 direct dependencies — the leanest here |
| Density | Compact by default. Of the four, the closest to §2.2's aesthetic out of the box |

**Why it lost, honestly:** by the numbers this is close, and on *density* Mantine is the better
fit — its defaults look like a dev tool where MUI's look like a Google product. It lost on two
things. First, ecosystem depth: MUI has 4× the downloads and a far larger body of
answered-questions, third-party components, and worked examples for exactly the awkward
integrations this migration will hit (routing shims, virtualisation, DnD). For a 39,000-line
migration executed largely by agents, "the answer already exists and is findable" is worth more
than 7 KB. Second, 48 releases in twelve months including a major — Mantine moves fast and has
a history of churny majors; MUI's 25 releases over the same period is the more boring number,
and boring is what a two-quarter migration wants underneath it.

### 4.3 Ant Design — `antd` 6.6.1

| Axis | Assessment |
|---|---|
| Bundle | **294.7 KB gz — 2.5× the field.** Even with tree-shaking, on a representative set |
| Theming | `ConfigProvider` + algorithmic themes (`theme.darkAlgorithm`) via `@ant-design/cssinjs`. Powerful, and the token system is genuinely well designed |
| Accessibility | The weakest of the four by reputation; historically ARIA has trailed features |
| Run-detail pane | **The best data components in the field.** `Table` is far ahead of MUI's — sorting, filtering, fixed columns and *built-in virtualisation*. If the board and the run list were the whole product this would be a serious contender |
| Maintenance | Healthy — 47 releases/12mo, 3.8M weekly. But **48 direct dependencies**, nearly all `@rc-component/*` |

**Why it was rejected:** the bundle, and the dependency surface. 294.7 KB gzipped is not a
disqualifier for a localhost binary on its own, but it buys a design language — dense
enterprise-Chinese, heavily rounded, strongly branded — that would have to be fought on every
screen to reach §2.2. Paying 2.5× the bytes to then override the look is the worst of both.
The 48-package `@rc-component` surface is a real supply-chain and upgrade-coordination cost
for a project that ships a signed binary.

### 4.4 React Aria Components — `react-aria-components` 1.20.0 (Adobe)

| Axis | Assessment |
|---|---|
| Bundle | 123.5 KB gz of behaviour and **zero** styling |
| Theming | None. You write every rule. That is the design |
| Accessibility | **The best in the field, and not close.** Adobe's team maintains the reference implementations of the ARIA patterns; internationalised date/number handling, correct touch and screen-reader behaviour, tested against real AT |
| Run-detail pane | Ships `Virtualizer` + `ListLayout` — the **only** candidate that answers the virtualised-list requirement with a first-party component |
| Maintenance | Excellent — 268 releases/12mo, 4.1M weekly, Apache-2.0 |

**Why it was rejected, despite being the most accessible option:** it is unstyled. Adopting it
means writing the entire visual layer by hand — which is *precisely what decision D-1 already
did*, and precisely what the owner is reversing. The ticket is explicit: "Do not invent
components. Use the library's own components and follow its conventions, including its patterns
for navigation, forms, empty states, dialogs, tables and feedback." React Aria has no such
patterns to follow; it has behaviour hooks and an empty canvas. Choosing it would deliver a
second hand-made design system with better keyboard handling — a better version of the mistake,
not a fix for it.

This is the rejection worth revisiting later: if Lexicode ever needs a11y guarantees stronger
than "axe is clean", React Aria is where to go, and MUI components can be replaced piecemeal
underneath the same theme.

---

## 5. Recommendation: MUI — and where it is weak

**Adopt MUI (`@mui/material` v9) with Emotion, themed from the existing `tokens.css`.**

The case, in one paragraph: it is the smallest total bundle of the seven measured, it has by an
order of magnitude the largest ecosystem, its theming system is expressive enough to hold the
whole §3 token ladder *and* to compress Material's spacing down to a 28–32px dev-tool density,
it ships 130+ components including every form, feedback, navigation and dialog pattern the spec
calls for, and — the part that is evidence rather than argument — the hardest screen in the app
has been converted onto it, `make check`'s web half passes, axe reports zero critical
violations, and both themes render (§6, with screenshots).

### Where MUI is weak for this product — stated plainly

1. **Material is the wrong design language for a dense dev tool.** This is the real cost and it
   does not go away. MUI's defaults are spacious, rounded, elevated and uppercase — Google's
   design language, not Linear's. The conversion needed a ~90-line `components` override block
   (`styles/muiTheme.ts`) to reach §2.2's density before a single screen was written, and every
   future screen inherits that block rather than the library's defaults. Mantine would not have
   needed it. **The mitigation is real but it is maintenance:** each MUI major can move the
   internals those overrides target.

2. **No virtualiser.** MUI ships nothing for windowing and its own docs point at
   react-window/react-virtuoso. Lexicode's timeline must window 500 rows. The conversion kept
   the existing 112-line `VirtualList` rather than adding a dependency — fine here, because the
   fixed row height makes the arithmetic exact, but it means the run detail's most
   performance-sensitive component is still hand-maintained. React Aria's first-party
   `Virtualizer` is a genuine advantage MUI does not answer.

3. **No drag and drop.** The board (§5.3) needs it. MUI has no story; the board currently uses
   native HTML5 drag events. Stage 5 of the migration plan will have to either keep that or add
   `@dnd-kit` — a decision this evaluation does not make, because the board has not been
   converted and guessing would be worthless.

4. **No split-pane.** The three-pane run detail is `Box` + CSS grid. That is MUI's documented
   layout approach and it works, but it is composition, not a component — the panes are not
   resizable and making them so means a fifth-party library or hand-written code.

5. **MUI's polymorphic `component` prop does not type-check against TanStack Router's `Link`.**
   Concrete, hit on day one: `<Button component={Link} to="…" params={…}>` fails overload
   resolution because TanStack's `Link` props are generic over the whole route tree. The fix is
   MUI's own documented "Routing libraries" shim — a `forwardRef` adapter
   (`components/RouterLink/RouterLink.tsx`, 20 lines) — but **it erases route-param type safety
   at that boundary**: `to` and `params` become plain strings, so a typo in a route path is
   caught by the router at runtime instead of by `tsc`. Every converted screen pays this on
   every link that is also a MUI component.

6. **Emotion is a runtime.** Styles are generated in the browser rather than extracted at build
   time. MUI v9 supports Pigment CSS (zero-runtime) as an alternative, but adopting it is a
   second migration and it is not what this proof of concept used.

7. **The good data grid is not free.** `@mui/x-data-grid`'s MIT tier is deliberately limited;
   virtualisation, column pinning and tree data are in the paid Pro tier. Ant Design gives all
   of that away. If the board or run list later needs a real data grid, MUI's answer is a
   licence or a hand-roll.

8. **MUI's own roadmap points at Base UI**, its unstyled successor — which is still at
   `1.0.0-rc.0`. There is a plausible future in which `@mui/material` becomes the legacy layer.
   This is a watch item, not a reason to wait: Base UI is not shippable today, and MUI v9 is
   being actively released.

### Cost of being wrong

Low, and deliberately so. The migration plan converts screen-by-screen behind a scoped
`MuiThemeProvider`, and the theme is derived from `tokens.css` rather than replacing it. If MUI
proves wrong after two or three screens, the tokens, the routes, the queries, the status
vocabulary and the tests are all untouched — what gets rewritten is the JSX of the converted
screens, which is the same work the migration was going to do anyway.

---

## 6. Proof of concept: the run detail, converted

**Screen chosen:** `/p/:key/runs/:id`. It was chosen because it is the screen §1's table is
about — three panes, a virtualised timeline, live SSE updates, tool-aware renderings, an inline
approval surface, the full status vocabulary, dialogs, forms, disclosures and two distinct
empty states. If MUI could not carry this screen, the answer would have been "not MUI", and the
answer would have been *known* rather than argued.

**What exists now**, converted and running:

- `routes/project/runs/RunDetailPage.tsx` — the three-pane frame, header, toolbar, timeline,
  context pane, current-step line
- `routes/project/runs/renderers.tsx` — all eleven tool-aware renderers
- `routes/project/runs/Intervention.tsx` — steering composer, Stop dialog, Take over dialog
- `routes/project/runs/InlineElicitation.tsx` — the shared answer/approve surface
- `styles/muiTheme.ts` + `styles/MuiThemeProvider.tsx` — the theme, derived from `tokens.css`
- `components/RouterLink/RouterLink.tsx` — the MUI ⇄ TanStack Router shim (§5, weakness 5)

**Verification.** `make check`'s web half — `tsc -b --noEmit`, `eslint .`, `vitest run` —
passes: **49 test files, 451 tests, all green**, including the axe suite (zero critical
violations on every route, this one included), the token-contrast suite (§10's 4.5:1 / 3:1
floors in both themes), the `StatusDot` usage scan, the focus-ring scan, the empty-state copy
suite and the route-reachability crawl. `make check`'s **Go half could not be run — this
container has no Go toolchain installed** (see §8).

**Screenshots:** [screenshots/](screenshots/) — three states × two themes, rendered by headless
Chromium against the real route tree.

### The three claims the conversion had to defend

**No invented components.** Every element on the screen is a MUI component or one of five
documented compositions, each with a stated reason:

| Composition | Why it is not a MUI component |
|---|---|
| Three-pane frame | `Box` + CSS grid. MUI ships no split-pane; `Box` is its documented layout primitive |
| `VirtualList` | Windowing is a scrolling strategy, not a component. MUI ships no virtualiser and points at third-party ones; the in-repo 112-line windower predates this work and has its own test. Its rows are MUI `ListItemButton`s |
| `StatusDot` | The §4 status vocabulary — architecture §13 designates it as the single place a status becomes a colour and a glyph. Product semantics, not chrome; it is the mechanism that guarantees §10's "colour is never the only carrier" |
| `CostChip` / `ContextMeter` / `LoopChain` | Shared with screens this run does not convert. Converting them would push MUI into unconverted screens; they move with their own screens (migration stages 3 and 5) |
| `RouterLink` | MUI's own documented routing shim (§5, weakness 5) |

**No keyboard-only actions.** The `f` "next failure" chord is **deleted**, not supplemented; it
is now a **Next failure (n)** button that states how many failures exist and disables itself
when there are none. Permalinking — previously "selection state lives in the URL", knowable only
if someone told you — is a **Copy link to step** button. The Context & cost pane's toggle used
to appear only below 1400px, so on a wide screen a closed pane was unreachable; the toggle is
now present at every width. The one shortcut that remains is Enter-to-send in the steering
composer, which sits beside an always-visible **Send** button doing the same thing.

**Status vocabulary preserved.** Every run state and trigger outcome still renders through
`StatusDot`: colour *and* glyph *and* word. `statusDotUsage.test.ts` scans every `<StatusDot>`
in the codebase and still passes.

### What the conversion cost, measured

| | Before | After | Δ |
|---|---:|---:|---:|
| Bundle, JS (gz) | 195.25 KB | 294.25 KB | **+99.0 KB** |
| Bundle, CSS (gz) | 20.08 KB | 17.39 KB | **−2.7 KB** |
| **Total (gz)** | **215.33 KB** | **311.64 KB** | **+96.3 KB** |
| `runs.module.css` | 1,318 lines | 210 lines | **−1,108 lines** |

The +96 KB is essentially the *entire* MUI + Emotion footprint arriving for one screen; the
next twenty screens add the marginal cost of the components they introduce, not another 99 KB
each. The −1,108 lines of CSS is the shape of the win: what was hand-written layout and colour
became theme and `sx`. `web/src` held **8,226 lines of `*.module.css`** before this run and
holds 7,118 now — one screen accounted for **13%** of the app's entire hand-written stylesheet
surface. `tokens.css` and `reset.css` are not part of that number and are not going anywhere:
they remain the source the theme is derived from.

---

## 7. What the research changed about the plan

The ticket asked to be told if the research changed the shape of the plan. Three things did.

1. **Shared components force the theme provider to leak ahead of the migration.** The
   answer/approve surface is embedded in the home needs-you card and in inbox rows, so
   converting the run detail's renderers pushed MUI into two screens that are not being
   converted. The fix — `InlineElicitation` mounts its own nested `MuiThemeProvider` — works and
   is cheap, but it means **the migration order must follow the shared-component graph, not the
   route tree**. That reordered stages 3 and 4 of the plan.

2. **The audit found more broken than hidden.** The discoverability audit was scoped to find
   actions with no visible control. It found those — but it also found three capabilities with
   *no user interface at all and, in two cases, no API either*: the container image field
   (`repos.image_ref` exists in the schema, is read by the run prep, and is absent from
   `openapi.yaml`), module health (`GET /system/modules` exists and **no screen calls it**), and
   the ticket→PR link (nothing in production code ever writes `tickets.pr_number`). Those are
   backend gaps wearing a UI costume, and a component-library migration will not fix any of
   them. They are called out separately in the audit §5 so they are not silently rolled into a
   redesign estimate.

3. **The status vocabulary should not be converted.** The initial instinct was to express
   `StatusDot` as a MUI `Chip`. That is wrong: the §4 vocabulary is product semantics enforced
   by a source-scanning test, and re-expressing it in library terms would trade a guarantee for
   a look. It stays as it is. The same logic protects `ContextMeter` and `CostChip` until their
   own screens move.

---

## 8. Verification status, stated honestly

| Check | Status |
|---|---|
| `tsc -b --noEmit` | Passes |
| `eslint .` | Passes |
| `vitest run` | Passes — 49 files, 451 tests |
| axe-core, all 21 routes | Zero critical violations |
| Token contrast, both themes | Passes |
| `go build` / `go vet` / `golangci-lint` / `go test` | **Not run — no Go toolchain in this container** |

No Go source was modified by this work, so the Go half of `make check` is expected to be
unaffected; that expectation has not been verified here and should be confirmed in CI.
