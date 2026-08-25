/*
 * S38 acceptance: an axe-core pass with ZERO CRITICAL violations on every route.
 *
 * Mechanics: the real route tree (app/router.tsx exports it) over a memory history, the
 * API stubbed at the fetch layer with a small seeded fixture set, EventSource stubbed
 * (jsdom has none). Each route renders its placeholder-or-seeded state:
 *
 *   - seeded: /, /inbox, /p/PAY (overview), /p/PAY/board (empty board with columns query),
 *     /p/PAY/triage, /p/PAY/wiki (+ a missing page), /p/PAY/runs (empty), /p/PAY/runs/r1
 *     (a completed run with an output), /p/PAY/agents, /p/PAY/triggers, /p/PAY/settings,
 *     /login, /setup — mostly the §8 empty states plus the real chrome.
 *   - error-placeholder: routes whose data endpoints 404 in the stub (ticket detail, agent
 *     detail, trigger editor, workspace settings, audit) render their error/loading
 *     states — still real DOM, still axe-checked.
 *
 * color-contrast is disabled: axe needs real layout/canvas for it, which jsdom cannot do —
 * contrast is asserted arithmetically against the tokens in tokens.contrast.test.ts.
 */
import { render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import axe from "axe-core";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "../../app/router";

const T = "2026-08-22T12:00:00Z";

const user = {
  id: "u1",
  email: "ada@example.com",
  display_name: "Ada",
  role: "owner",
  avatar_color: "#446688",
  created_at: T,
};

const inheritedInt = (value: number) => ({ value, inherited: true, workspace_value: value });

const project = {
  id: "p1",
  key: "PAY",
  name: "Payments",
  description: "Seeded fixture project",
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

const run = {
  id: "r1",
  seq: 1,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: null,
  trigger_id: null,
  state: "completed",
  state_reason: "",
  hold_reason: "",
  autonomy: "auto",
  directive_version_id: null,
  model: "claude-sonnet-5",
  effort: "medium",
  branch: "dev/run-1",
  subject_key: "",
  current_step: "",
  cost_cents: 12,
  tokens_in: 1000,
  tokens_out: 400,
  tokens_cache_read: 0,
  tokens_cache_write: 0,
  step_count: 3,
  error_message: "",
  takeover_note: "",
  queued_at: T,
  started_at: T,
  ended_at: T,
  acknowledged_at: null,
};

/**
 * The run detail is the S39 Material UI proof of concept, and an empty activity list renders
 * none of what was converted — no timeline rows, no log lines, no disclosures. It is seeded
 * with a real stream (a thought, a passing step, a failing Bash step with stdout) so this
 * suite sees the converted markup rather than an empty screen. The failing step is also the
 * default selection, so the centre pane renders its output lines without a ?step= URL.
 */
const activity = (over: Record<string, unknown>) => ({
  seq: 1,
  type: "action",
  level: 1,
  tool_name: "Bash",
  group_key: "",
  title: "npm test",
  payload: {},
  ok: true,
  attempt: 1,
  duration_ms: 1000,
  queued_ms: 100,
  model_ms: 500,
  tool_ms: 400,
  cost_cents: 3,
  tokens_in: 100,
  tokens_out: 40,
  tokens_cache_read: 0,
  created_at: T,
  ...over,
});

const RUN_ACTIVITIES = [
  activity({ seq: 1, type: "thought", tool_name: "", title: "Reading the failing test" }),
  activity({ seq: 2, title: "npm run build", ok: true }),
  activity({
    seq: 3,
    title: "npm test",
    ok: false,
    payload: {
      argv: ["bash", "-lc", "npm test"],
      exit: 1,
      stdout: "> npm test\n1 failing\n",
      stderr: "AssertionError: expected 1 to equal 2\n",
    },
  }),
];

const agent = {
  id: "a1",
  project_id: "p1",
  name: "Dev",
  role: "implementer",
  color: "#888888",
  runtime_id: "claude-code",
  model: "claude-sonnet-5",
  effort: "medium",
  autonomy: "auto",
  permissions: {},
  git_author_name: "Dev",
  git_author_email: "dev@agents.local",
  forge_login: null,
  concurrency_cap: 1,
  daily_cap_cents: null,
  enabled: true,
  archived_at: null,
  created_at: T,
  updated_at: T,
};

/** method+path → payload. First match wins; anything else is a problem+json 404. */
const FIXTURES: Array<[RegExp, unknown]> = [
  [/\/auth\/me$/, user],
  [/\/projects\?/, { projects: [] }],
  [/\/projects$/, { projects: [] }],
  [/\/projects\/PAY$/, project],
  [
    /\/projects\/PAY\/overview$/,
    {
      project,
      owner: { id: "u1", display_name: "Ada", avatar_color: "#446688" },
      repo: null,
      agent_count: 1,
      open_tickets: 0,
      runs_today: 1,
      spend_today_cents: 12,
    },
  ],
  [
    /\/projects\/PAY\/budget$/,
    {
      spend_today_cents: 12,
      ceiling_cents: 2000,
      inherited: true,
      exhausted: false,
      day: "2026-08-22",
      resets_at: T,
    },
  ],
  [/\/projects\/PAY\/triage$/, { items: [], pending_count: 0 }],
  [/\/projects\/PAY\/runs\?view=needs_you$/, { runs: [] }],
  [/\/projects\/PAY\/runs$/, { runs: [run] }],
  [/\/runs\/r1$/, { run, outputs: [], context: [], messages: [], elicitations: [] }],
  [/\/runs\/r1\/activities/, { activities: RUN_ACTIVITIES }],
  [/\/runs\/r1\/chain$/, { chain: [] }],
  [/\/projects\/PAY\/agents$/, { agents: [agent] }],
  [/\/projects\/PAY\/tickets/, { tickets: [] }],
  [/\/projects\/PAY\/columns$/, { columns: [] }],
  [/\/projects\/PAY\/labels$/, { labels: [] }],
  [/\/projects\/PAY\/wiki$/, { pages: [] }],
  [/\/projects\/PAY\/wiki\/context-budget$/, { always_tokens: 0, threshold_tokens: 4000, pages: [] }],
  [/\/projects\/PAY\/triggers$/, { triggers: [] }],
  [/\/projects\/PAY\/repo$/, { connected: false }],
  [/\/inbox$/, { runs: [] }],
  [/\/notifications$/, { notifications: [], unread: 0 }],
];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetchStub(): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url);
      if ((init?.method ?? "GET") === "GET") {
        for (const [re, body] of FIXTURES) {
          if (re.test(u)) return jsonResponse(body);
        }
      }
      return jsonResponse(
        { type: "not_found", title: "Not found", status: 404, detail: "stubbed" },
        404,
      );
    }),
  );
}

/** jsdom has no EventSource; the stream manager just needs the interface. */
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = 0;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  addEventListener(): void {}
  removeEventListener(): void {}
  close(): void {
    this.readyState = 2;
  }
}

/** Every route in the app (S38 DoD: axe on every route's rendered state). */
const ROUTES = [
  "/",
  "/inbox",
  "/settings",
  "/settings/audit",
  "/login",
  "/setup",
  "/p/PAY",
  "/p/PAY/bootstrap",
  "/p/PAY/board",
  "/p/PAY/triage",
  "/p/PAY/t/PAY-1",
  "/p/PAY/wiki",
  "/p/PAY/wiki/some-page",
  "/p/PAY/runs",
  "/p/PAY/runs/r1",
  "/p/PAY/agents",
  "/p/PAY/agents/a1",
  "/p/PAY/triggers",
  "/p/PAY/triggers/new",
  "/p/PAY/settings",
] as const;

beforeEach(() => {
  installFetchStub();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("axe-core: zero critical violations on every route (S38)", () => {
  it.each(ROUTES)(
    "%s",
    async (path) => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      const router = createRouter({
        routeTree,
        history: createMemoryHistory({ initialEntries: [path] }),
      });
      const { container, unmount } = render(
        <QueryClientProvider client={qc}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
      // Wait for the route to produce real DOM (the shell chrome or an auth form), then
      // give queries a beat to settle into their placeholder-or-seeded render.
      await waitFor(() => {
        expect(container.childElementCount).toBeGreaterThan(0);
      });
      await new Promise((r) => setTimeout(r, 150));

      const results = await axe.run(container, {
        rules: { "color-contrast": { enabled: false } },
      });
      const critical = results.violations.filter((v) => v.impact === "critical");
      expect(
        critical,
        critical
          .map((v) => `${v.id}: ${v.help} → ${v.nodes.map((n) => n.target.join(" ")).join("; ")}`)
          .join("\n"),
      ).toEqual([]);
      unmount();
      qc.clear();
    },
    20_000,
  );
});
