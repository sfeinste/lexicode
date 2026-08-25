# Screenshots — the LEXI-13 proof of concept

These are real renders of the shipped components, not mock-ups. `web/screenshots/capture.mjs`
boots Vite over `web/screenshots/index.html`, which mounts the app's **actual route tree**
(`src/app/router.tsx`) over a memory history with `fetch` stubbed from
`web/screenshots/fixtures.ts`, then drives headless Chromium. Anything visible here is what
the components produce.

Regenerate after changing the screen:

```
cd web
npx playwright install chromium     # once
node screenshots/capture.mjs
```

Deliberately not part of `make check`: CI has no browser, and a screenshot is a review
artifact rather than an assertion. The assertions that *do* guard this screen are the axe
suite, the token-contrast suite and the route-reachability crawl, all of which run in
`make check`.

| File | What it shows |
|---|---|
| `run-detail-dark.png` / `run-detail-light.png` | The landing state. Failed steps auto-select, so the failing `go test` step opens with its output expanded. Note the collapsed `Read 9 files ▸` group, the labelled **Detail level** control, and **Next failure (1)** / **Copy link to step** — the two actions that used to exist only as the `f` chord and as unwritten knowledge about the URL. |
| `run-detail-approval-dark.png` / `run-detail-approval-light.png` | The inline approval: the six enriched fields, the four responses (Approve · Approve with edits · Respond · Deny), and the scoped "Always allow" checkbox. Inline in the timeline, never a modal (UI spec §5.7). |
| `run-detail-failure-dark.png` / `run-detail-failure-light.png` | A terminal failed run. The current-step sentence is replaced by the outcome line, and the steering composer, Stop and Take over correctly disappear — there is nothing left to steer. |

Both themes are shot for every state because the theme is the part most likely to regress:
the MUI palette is derived from `tokens.css` at import time (`web/src/styles/muiTheme.ts`),
so a token edit moves these pictures.

The status vocabulary survives the conversion intact — every state in the shots renders as a
glyph **and** a colour **and** a word (`✕ Failed`, `▲ Awaiting approval`, `✓`), never colour
alone (UI spec §10).
