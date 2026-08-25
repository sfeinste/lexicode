/*
 * Render the converted screen in a real browser and write PNGs.
 *
 * LEXI-13 asks for a proof of concept that is grounded in something real. A jsdom test can
 * assert that a control exists; it cannot show that the screen looks right, that Material UI's
 * components resolve to the §3 tokens, or that light and dark are both genuinely themed. This
 * does — it drives the ACTUAL production bundle (`web/dist`, the same artifact `go:embed` ships)
 * in headless Chromium, with the API stubbed at the network layer so no server or database is
 * needed.
 *
 * Deliberately not part of `make check`: it needs a browser binary, which the check must not.
 *
 *   cd web && npm run build
 *   node scripts/screenshot.mjs            # writes ../design/screenshots/*.png
 *
 * Playwright is deliberately NOT a dependency of this project — it would put a browser
 * download in front of `npm ci`, which `make check` runs. Install it anywhere and point
 * PLAYWRIGHT at the module:
 *
 *   npm --prefix /tmp/pw i playwright && npx --prefix /tmp/pw playwright install chromium
 *   PLAYWRIGHT=/tmp/pw/node_modules/playwright/index.mjs node scripts/screenshot.mjs
 */
import { createReadStream } from "node:fs";
import { mkdir, stat } from "node:fs/promises";
import { createServer } from "node:http";
import { dirname, extname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const DIST = resolve(HERE, "..", "dist");
const OUT = resolve(HERE, "..", "..", "design", "screenshots");
const PORT = 4319;

const T = "2026-08-22T12:00:00Z";

// ---- fixtures ---------------------------------------------------------------------------
// The same shapes the route-reachability and axe suites seed, filled in richly enough that the
// screenshot shows a real run rather than a set of empty states.

const user = {
  id: "u1",
  email: "ada@example.com",
  display_name: "Ada Lovelace",
  role: "owner",
  avatar_color: "#446688",
  created_at: T,
};

const inheritedInt = (value) => ({ value, inherited: true, workspace_value: value });

const project = {
  id: "p1",
  key: "PAY",
  name: "Payments",
  description: "Card capture and settlement",
  color: "#4653cf",
  owner_id: "u1",
  agent_guidance: "",
  archived_at: null,
  created_at: T,
  updated_at: T,
  settings: {
    daily_budget_cents: inheritedInt(2000),
    context_threshold_tokens: inheritedInt(4000),
    verification_days: inheritedInt(30),
    pr_size_warning_lines: inheritedInt(600),
  },
};

const agent = {
  id: "a1",
  project_id: "p1",
  name: "Dev",
  role: "implementer",
  color: "#0f7f9c",
  runtime_id: "claude-code",
  model: "claude-sonnet-5",
  effort: "medium",
  autonomy: "supervised",
  permissions: { allow: [], deny: [], ask: [] },
  git_author_name: "Dev",
  git_author_email: "dev@agents.local",
  forge_login: null,
  concurrency_cap: 1,
  daily_cap_cents: null,
  max_wall_clock_seconds: 3600,
  max_steps: 100,
  enabled: true,
  directive_version_id: null,
  archived_at: null,
  created_at: T,
  updated_at: T,
  runs_week: 4,
  spend_week_cents: 310,
  success_rate: 0.75,
};

/* A FAILED run: it exercises the §4 `failed` vocabulary, auto-expands its failed step, and
 * gives the "Next failure" button — the affordance this conversion added — something to do. */
const run = {
  id: "r1",
  seq: 7,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: "t1",
  trigger_id: null,
  state: "failed",
  state_reason: "",
  hold_reason: "",
  autonomy: "supervised",
  directive_version_id: null,
  model: "claude-sonnet-5",
  effort: "medium",
  branch: "dev/pay-1-idempotency-keys",
  subject_key: "ticket:PAY-1",
  current_step: "",
  cost_cents: 142,
  tokens_in: 71000,
  tokens_out: 13200,
  tokens_cache_read: 48000,
  tokens_cache_write: 0,
  step_count: 9,
  error_message: "npm test exited 1",
  takeover_note: "",
  queued_at: T,
  started_at: T,
  ended_at: "2026-08-22T12:04:12Z",
  acknowledged_at: null,
};

const act = (over) => ({
  seq: 1,
  type: "action",
  level: 1,
  tool_name: "Bash",
  group_key: "",
  title: "",
  payload: {},
  ok: true,
  attempt: 1,
  duration_ms: 1200,
  queued_ms: 120,
  model_ms: 700,
  tool_ms: 380,
  cost_cents: 3,
  tokens_in: 4200,
  tokens_out: 380,
  tokens_cache_read: 3100,
  created_at: T,
  ...over,
});

/* Nine steps: a provision, a thought, a run of consecutive Reads that the timeline COLLAPSES
 * into one `Read 5 files` group (spec §5.7's highest-value log affordance), an edit rendered as
 * a diff hunk, and a failing bash step that auto-expands and carries the exit code. */
const activities = [
  act({
    seq: 1,
    type: "provision",
    tool_name: "",
    title: "Provision container",
    payload: { state: "ok" },
    duration_ms: 4300,
    cost_cents: 0,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
  }),
  act({
    seq: 2,
    type: "thought",
    tool_name: "",
    title: "Reading the charge path before touching the retry logic",
    duration_ms: 2600,
  }),
  ...["src/api/charge.ts", "src/api/refund.ts", "src/db/ledger.ts", "src/lib/retry.ts", "src/api/charge.test.ts"].map(
    (path, i) =>
      act({
        seq: 3 + i,
        tool_name: "Read",
        group_key: "Read",
        title: `Read ${path}`,
        payload: { path, lines: 120 + i * 30 },
        duration_ms: 320 + i * 40,
        cost_cents: 1,
      }),
  ),
  act({
    seq: 8,
    tool_name: "Edit",
    title: "Edit src/api/charge.ts",
    payload: {
      path: "src/api/charge.ts",
      hunks: [
        {
          header: "@@ -88,6 +88,11 @@ export async function charge(req: ChargeRequest) {",
          lines: [
            "   const key = req.idempotency_key;",
            "-  if (key === undefined) {",
            "-    throw new BadRequest('idempotency_key is required');",
            "+  if (key === undefined || key === '') {",
            "+    throw new BadRequest('idempotency_key is required');",
            "+  }",
            "+  const seen = await ledger.lookup(key);",
            "+  if (seen !== null) {",
            "+    return seen.result;",
            "   }",
          ],
        },
      ],
    },
    duration_ms: 5100,
    cost_cents: 9,
    tokens_in: 12400,
    tokens_out: 1900,
  }),
  act({
    seq: 9,
    tool_name: "Bash",
    title: "npm test",
    ok: false,
    payload: {
      argv: ["bash", "-lc", "npm test"],
      exit: 1,
      stdout:
        "> lexicode-payments@1.0.0 test\n" +
        "> vitest run\n" +
        "\n" +
        " ✓ src/api/refund.test.ts (12)\n" +
        " ❯ src/api/charge.test.ts (14)\n" +
        "   × replays a stored result for a repeated idempotency key\n" +
        "\n" +
        " FAIL  src/api/charge.test.ts > replays a stored result\n" +
        " AssertionError: expected 'captured' to be 'replayed'\n" +
        "  ❯ src/api/charge.test.ts:88:24\n" +
        "\n" +
        " Test Files  1 failed | 1 passed (2)\n" +
        "      Tests  1 failed | 25 passed (26)\n",
    },
    duration_ms: 18400,
    queued_ms: 200,
    model_ms: 1200,
    tool_ms: 17000,
    cost_cents: 4,
  }),
];

const context = [
  {
    provider: "wiki",
    source_ref: "conventions",
    position: 1,
    title: "Conventions",
    reason: "agent_scope=always",
    tokens: 1840,
  },
  {
    provider: "wiki",
    source_ref: "payments-runbook",
    position: 2,
    title: "Payments runbook",
    reason: "scope_paths matched src/api/",
    tokens: 2960,
  },
  {
    provider: "repo",
    source_ref: "CLAUDE.md",
    position: 3,
    title: "CLAUDE.md",
    reason: "repository file, always loaded",
    tokens: 720,
  },
];

const outputs = [
  {
    id: "o1",
    run_id: "r1",
    kind: "branch",
    ref: "dev/pay-1-idempotency-keys",
    url: "",
    summary: "partial work pushed",
    created_at: T,
  },
];

const FIXTURES = [
  [/\/auth\/me$/, user],
  [/\/projects(\?.*)?$/, { projects: [{ ...project, stats: { open_tickets: 3, running_agents: 1, needs_you: 1, spend_today_cents: 142, last_activity: T } }] }],
  [/\/projects\/PAY$/, project],
  [/\/projects\/PAY\/budget$/, { spend_today_cents: 142, ceiling_cents: 2000, inherited: true, exhausted: false, day: "2026-08-22", resets_at: T }],
  [/\/projects\/PAY\/counts$/, { tickets: 3, runs: 7, wiki_pages: 2 }],
  [/\/projects\/PAY\/triage$/, { items: [], pending_count: 0 }],
  [/\/projects\/PAY\/runs\?view=needs_you$/, { runs: [run] }],
  [/\/projects\/PAY\/runs(\?.*)?$/, { runs: [run] }],
  [/\/runs\/r1$/, { run, outputs, context, messages: [], elicitations: [] }],
  [/\/runs\/r1\/activities/, { activities }],
  [/\/runs\/r1\/chain$/, { chain: [] }],
  [/\/projects\/PAY\/agents(\?.*)?$/, { agents: [agent] }],
  [/\/projects\/PAY\/wiki\/context-budget$/, {
    threshold_tokens: 4000,
    always_tokens: 2560,
    over: false,
    pages: [
      { id: "w1", slug: "conventions", title: "Conventions", tokens: 1840 },
      { id: "w2", slug: "payments-runbook", title: "Payments runbook", tokens: 720 },
    ],
  }],
  [/\/inbox$/, { runs: [] }],
  [/\/notifications$/, { notifications: [], unread: 0 }],
  [/\/system\/modules$/, { modules: [] }],
];

// ---- a static server for web/dist, with SPA fallback -------------------------------------

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".woff2": "font/woff2",
};

function serveDist() {
  return createServer(async (req, res) => {
    const url = new URL(req.url ?? "/", "http://localhost");
    const candidate = join(DIST, normalize(url.pathname));
    let file = candidate;
    try {
      const s = await stat(candidate);
      if (s.isDirectory()) file = join(DIST, "index.html");
    } catch {
      file = join(DIST, "index.html"); // SPA fallback: every route serves the shell
    }
    res.writeHead(200, { "Content-Type": MIME[extname(file)] ?? "application/octet-stream" });
    createReadStream(file).pipe(res);
  });
}

// ---- the shots ---------------------------------------------------------------------------

const SHOTS = [
  {
    name: "run-detail-dark",
    theme: "dark",
    path: "/p/PAY/runs/r1",
    caption: "Run detail, dark — the converted screen",
  },
  {
    name: "run-detail-light",
    theme: "light",
    path: "/p/PAY/runs/r1",
    caption: "Run detail, light — the same screen, the same tokens",
  },
  {
    name: "run-detail-diff-dark",
    theme: "dark",
    path: "/p/PAY/runs/r1?step=8",
    caption: "The diff-hunk renderer: Paper + Typography over the log composition",
  },
  {
    name: "run-detail-narrow-light",
    theme: "light",
    path: "/p/PAY/runs/r1",
    viewport: { width: 1180, height: 900 },
    caption: "Below 1400px the context pane collapses to a labelled toggle (§10)",
  },
];

async function main() {
  const { chromium } = await import(process.env.PLAYWRIGHT ?? "playwright");
  await mkdir(OUT, { recursive: true });

  const server = serveDist();
  await new Promise((r) => server.listen(PORT, r));

  const browser = await chromium.launch();
  try {
    for (const shot of SHOTS) {
      const ctx = await browser.newContext({
        viewport: shot.viewport ?? { width: 1600, height: 1000 },
        deviceScaleFactor: 2,
        colorScheme: shot.theme,
      });

      // Every API call is answered from the fixtures above; the SSE stream is left hanging,
      // which is exactly what an idle stream looks like.
      await ctx.route("**/api/v1/**", async (route) => {
        const path = new URL(route.request().url()).pathname + new URL(route.request().url()).search;
        if (path.includes("/stream?")) return route.abort();
        for (const [re, body] of FIXTURES) {
          if (re.test(path)) {
            return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
          }
        }
        return route.fulfill({
          status: 404,
          contentType: "application/problem+json",
          body: JSON.stringify({ type: "not_found", title: "Not found", status: 404 }),
        });
      });

      // The theme preference is Zustand-persisted (stores/ui.ts); seed it before first paint
      // so the screenshot never catches a flash of the other palette.
      await ctx.addInitScript(
        ([key, theme]) => {
          window.localStorage.setItem(
            key,
            JSON.stringify({ state: { railCollapsed: false, theme, density: "comfortable" }, version: 0 }),
          );
        },
        ["lexicode-ui", shot.theme],
      );

      const page = await ctx.newPage();
      await page.goto(`http://127.0.0.1:${PORT}${shot.path}`, { waitUntil: "networkidle" });
      await page.waitForSelector('[aria-label="Step timeline"]');
      await page.waitForTimeout(600);
      const out = join(OUT, `${shot.name}.png`);
      await page.screenshot({ path: out });
      console.log(`${out}  —  ${shot.caption}`);
      await ctx.close();
    }
  } finally {
    await browser.close();
    server.close();
  }
}

await main();
