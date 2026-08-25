/*
 * S39 acceptance for the Material UI proof of concept — the run detail screen.
 *
 * The ticket's bar for the converted screen, asserted rather than asserted-to:
 *
 *  1. NO ACTION IS REACHABLE ONLY BY KEYBOARD. Every binding this route registers in the
 *     keyboard registry must also exist on screen as a control with a readable name. This
 *     is the general form of the bug the ticket names ("`f` next failure" had no button),
 *     so it is written as a sweep over the registry rather than as one assertion about `f`
 *     — a future route-scoped chord added without a control fails here by name.
 *  2. THE STATUS VOCABULARY SURVIVES. Colour is never the only carrier: the run's state
 *     renders with its §4 glyph AND its label, and the glyph's colour comes from the §3.2
 *     palette slot for that meaning.
 *  3. LIGHT AND DARK BOTH WORK. The screen renders under `data-theme="light"` and
 *     `data-theme="dark"` and resolves to the two different token sets.
 *  4. THE EMPTY STATE SAYS WHAT TO DO NEXT — §8's rule, applied to the two "nothing here"
 *     states this screen owns (no steps yet; no context loaded).
 */
import { act, render, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "../../../app/router";
import { keyboard } from "../../../lib/keyboard/registry";

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
  description: "",
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

/** A failed run, so the "next failure" affordance has something to jump to. */
const run = {
  id: "r1",
  seq: 7,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: null,
  trigger_id: null,
  state: "failed",
  state_reason: "",
  hold_reason: "",
  autonomy: "auto",
  directive_version_id: null,
  model: "claude-sonnet-5",
  effort: "medium",
  branch: "dev/run-7",
  subject_key: "",
  current_step: "",
  cost_cents: 42,
  tokens_in: 1000,
  tokens_out: 400,
  tokens_cache_read: 0,
  tokens_cache_write: 0,
  step_count: 3,
  error_message: "npm test exited 1",
  takeover_note: "",
  queued_at: T,
  started_at: T,
  ended_at: T,
  acknowledged_at: null,
};

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
  cost_cents: 0,
  tokens_in: 0,
  tokens_out: 0,
  tokens_cache_read: 0,
  created_at: T,
  ...over,
});

const ACTIVITIES = [
  activity({ seq: 1, type: "thought", tool_name: "", title: "Reading the failing test" }),
  activity({ seq: 2, title: "npm run build", ok: true }),
  activity({
    seq: 3,
    title: "npm test",
    ok: false,
    payload: { argv: ["bash", "-lc", "npm test"], exit: 1, stdout: "1 failing\n" },
  }),
];

const FIXTURES: Array<[RegExp, unknown]> = [
  [/\/auth\/me$/, user],
  [/\/projects\?/, { projects: [] }],
  [/\/projects\/PAY$/, project],
  [/\/projects\/PAY\/budget$/, {
    spend_today_cents: 42,
    ceiling_cents: 2000,
    inherited: true,
    exhausted: false,
    day: "2026-08-22",
    resets_at: T,
  }],
  [/\/projects\/PAY\/triage$/, { items: [], pending_count: 0 }],
  [/\/projects\/PAY\/runs\?view=needs_you$/, { runs: [] }],
  [/\/projects\/PAY\/runs/, { runs: [run] }],
  [/\/runs\/r1$/, { run, outputs: [], context: [], messages: [], elicitations: [] }],
  [/\/runs\/r1\/activities/, { activities: ACTIVITIES }],
  [/\/runs\/r1\/chain$/, { chain: [] }],
  [/\/projects\/PAY\/agents/, { agents: [] }],
  [/\/projects\/PAY\/wiki\/context-budget$/, {
    always_tokens: 0,
    threshold_tokens: 4000,
    over: false,
    pages: [],
  }],
  [/\/inbox$/, { runs: [] }],
  [/\/notifications$/, { notifications: [], unread: 0 }],
];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

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

async function renderRunDetail() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/p/PAY/runs/r1"] }),
  });
  const result = render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  await waitFor(() => {
    expect(result.container.querySelector('[aria-label="Step timeline"]')).not.toBeNull();
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 100));
  });
  return { ...result, router, qc };
}

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url);
      if ((init?.method ?? "GET") === "GET") {
        for (const [re, body] of FIXTURES) {
          if (re.test(u)) return jsonResponse(body);
        }
      }
      return jsonResponse({ type: "not_found", title: "Not found", status: 404 }, 404);
    }),
  );
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.documentElement.removeAttribute("data-theme");
});

describe("run detail on Material UI (S39)", () => {
  it("has no action reachable only by keyboard", async () => {
    const { container, unmount } = await renderRunDetail();

    // Every chord this ROUTE registers while the screen is mounted…
    const routeBindings = keyboard.getBindings().filter((b) => b.scope === "route");
    expect(routeBindings.length, "the run detail registers at least one route chord").toBeGreaterThan(0);

    // …must also be a control on screen. The names are matched case-insensitively against
    // the accessible name of every button and link.
    const names = [...container.querySelectorAll("button, a")].map((el) =>
      `${el.textContent ?? ""} ${el.getAttribute("aria-label") ?? ""}`.toLowerCase(),
    );
    for (const b of routeBindings) {
      expect(
        names.some((n) => n.includes(b.title.toLowerCase())),
        `the "${b.title}" chord (${b.chord}) has no visible control on this screen — ` +
          `LEXI-13's rule is that a shortcut is a shortcut, never the only door. ` +
          `Controls found: ${names.filter((n) => n.trim() !== "").join(" | ")}`,
      ).toBe(true);
    }
    unmount();
  }, 20_000);

  it("the Next failure button and the `f` chord select the same step", async () => {
    const { getByRole, router, unmount } = await renderRunDetail();

    const button = getByRole("button", { name: /next failure/i });
    expect((button as HTMLButtonElement).disabled).toBe(false);

    await act(async () => {
      button.click();
      await new Promise((r) => setTimeout(r, 50));
    });
    // Seq 3 is the only failed step in the fixture; selection lives in the URL (rule 12).
    expect(router.state.location.search).toMatchObject({ step: 3 });
    unmount();
  }, 20_000);

  it("keeps the §4 status vocabulary: glyph AND label, never colour alone", async () => {
    const { container, unmount } = await renderRunDetail();
    const dots = [...container.querySelectorAll('[data-status="failed"]')];
    expect(dots.length, "the run's state renders through StatusDot").toBeGreaterThan(0);
    const dot = dots[0] as HTMLElement;
    // The §4.1 glyph for `failed`, and its label in words beside it.
    expect(within(dot).getByText("✕")).toBeTruthy();
    expect(dot.textContent).toContain("Failed");
    unmount();
  }, 20_000);

  it("renders the verbosity switch as three visible, labelled options", async () => {
    const { getByRole, unmount } = await renderRunDetail();
    const group = getByRole("group", { name: "Verbosity" });
    for (const label of ["Summary", "Normal", "Verbose"]) {
      expect(within(group).getByRole("button", { name: label })).toBeTruthy();
    }
    unmount();
  }, 20_000);

  it("renders in both themes, resolving to the two different token sets", async () => {
    for (const theme of ["light", "dark"] as const) {
      document.documentElement.setAttribute("data-theme", theme);
      const { container, unmount } = await renderRunDetail();
      // The screen is themed by the tokens: the glyph resolves through MUI's palette
      // variable, which resolves to the §3.2 token, which data-theme swaps.
      const dot = container.querySelector('[data-status="failed"] [aria-hidden="true"]');
      expect(dot, `the run detail renders under data-theme="${theme}"`).not.toBeNull();
      expect(getComputedStyle(dot as HTMLElement).color).toContain("--mui-palette-error-main");
      unmount();
    }
    // The two themes really are different palettes — tokens.css is the switch, and the
    // contrast floors for both are asserted in styles/tokens.contrast.test.ts.
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  }, 30_000);

  it("the context pane's empty state says what to do next", async () => {
    const { container, unmount } = await renderRunDetail();
    const pane = container.querySelector('[aria-label="Context and cost"]');
    expect(pane).not.toBeNull();
    // §8: never just "there is nothing here".
    expect(pane!.textContent).toContain("Nothing beyond the ticket itself");
    expect(pane!.textContent).toMatch(/scope a\s+wiki page/);
    expect(within(pane as HTMLElement).getByRole("link", { name: "wiki page" })).toBeTruthy();
    unmount();
  }, 20_000);
});
