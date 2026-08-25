/*
 * Render the converted screen in a real browser and write PNGs into design/screenshots/.
 *
 * `node screenshots/capture.mjs` — needs Playwright's chromium (npx playwright install
 * chromium). Deliberately NOT wired into `make check`: CI has no browser, and a screenshot
 * is a review artifact, not an assertion. Re-run it by hand when the screen changes.
 *
 * It boots Vite programmatically over screenshots/index.html (the harness mounts the real
 * route tree against seeded fixtures), then shoots each screen in both themes.
 */
import { mkdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import react from "@vitejs/plugin-react";
import { chromium } from "playwright";
import { createServer } from "vite";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(here, "../../design/screenshots");

const RUN = "/p/PAY/runs/r-4821";

/** name → harness query. Both themes are shot for every entry. */
const SHOTS = [
  {
    name: "run-detail",
    path: RUN,
    what: "The landing state: failed steps auto-select, so the failing test run opens expanded.",
  },
  {
    name: "run-detail-approval",
    path: `${RUN}?step=17`,
    what: "The inline approval — six enriched fields and the four responses, never a modal.",
  },
  {
    name: "run-detail-failure",
    path: RUN,
    state: "failed",
    what: "A terminal failed run: the outcome line replaces the current-step sentence.",
  },
];

// Tall enough that the steering composer and its helper text are not clipped at the fold.
const VIEWPORT = { width: 1600, height: 1080 };

async function main() {
  await mkdir(outDir, { recursive: true });

  const server = await createServer({
    root: here,
    configFile: false,
    // The harness has its own root, so it does not inherit vite.config.ts — without the
    // React plugin, JSX compiles to the classic runtime and the page dies on "React is
    // not defined".
    plugins: [react()],
    logLevel: "error",
    server: { port: 5199, strictPort: true },
  });
  await server.listen();
  const base = `http://localhost:5199`;

  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: VIEWPORT, deviceScaleFactor: 2 });

  for (const shot of SHOTS) {
    for (const theme of ["dark", "light"]) {
      const query = new URLSearchParams({ path: shot.path, theme });
      if (shot.state) query.set("state", shot.state);
      // Keep the OS preference and the explicit toggle agreeing, so tokens.css's media
      // query and MuiThemeProvider resolve to the same mode.
      await page.emulateMedia({ colorScheme: theme });
      await page.goto(`${base}/?${query}`, { waitUntil: "networkidle" });
      // The queries settle and the timeline windows; a fixed beat beats a brittle selector.
      await page.waitForTimeout(1200);
      const file = resolve(outDir, `${shot.name}-${theme}.png`);
      await page.screenshot({ path: file });
      console.log(`  ${file.replace(resolve(here, "../.."), ".")}  — ${shot.what}`);
    }
  }

  await browser.close();
  await server.close();
}

await main();
