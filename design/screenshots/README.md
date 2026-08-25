# Screenshots

Referenced from [../ui-library-evaluation.md](../ui-library-evaluation.md). Generated, not drawn
by hand — regenerate them rather than editing them.

```
cd web
npm run build
npm --prefix /tmp/pw i playwright && npx --prefix /tmp/pw playwright install chromium
PLAYWRIGHT=/tmp/pw/node_modules/playwright/index.mjs node scripts/screenshot.mjs
```

[`web/scripts/screenshot.mjs`](../../web/scripts/screenshot.mjs) drives `web/dist` — the same
bundle `go:embed` ships — in headless Chromium, with the API answered from fixtures at the network
layer, so no server, database or container is involved.

Playwright is deliberately not a dependency of `web/package.json`: `make check` runs `npm ci`, and
a browser download does not belong in front of the project's own check. That is also why these
images are committed. Nothing in `make check` looks at a pixel — axe runs over jsdom, which renders
no colour — so a screenshot is currently the only way to catch a colour that the library filled in
for us. It has already caught one; see weakness #7 in the evaluation.

| File | What it shows |
|---|---|
| `run-detail-dark.png` | The converted run detail, dark. Three panes, the collapsed `Read 5 files ▸` group, the timing gutters, and the **Next failure** button the conversion added. |
| `run-detail-light.png` | The same screen and the same components in light — only `tokens.css` changed underneath them. |
| `run-detail-diff-dark.png` | The tool-aware centre pane: an edit step renders as a diff hunk, with `+`/`−` as well as colour. |
| `run-detail-narrow-light.png` | Below 1400px the context pane collapses behind a labelled `Context ▸` toggle (spec §10). |
