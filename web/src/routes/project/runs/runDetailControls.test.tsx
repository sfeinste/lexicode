/*
 * The run detail's contract as a CONVERTED screen (LEXI-13, stage 0).
 *
 * The conversion's central claim is a behavioural one — "every action on this screen has a
 * visible control with a readable label, and nothing is reachable by keyboard alone" — and
 * before this file nothing held it. The route's existing coverage is axe.test.tsx (a
 * completed run with ZERO activities, so the empty state only), reachability (does the route
 * render), and statusDotUsage (a source grep). Reverting the whole conversion left the suite
 * green. That made every criterion the proof of concept is judged on a paragraph in a file
 * header plus a screenshot.
 *
 * So this file pins the claims themselves, over the real App, the real router and the real
 * query layer with fetch stubbed at the network boundary:
 *
 *   1. Each control the redesign introduced or promoted exists and is findable BY ITS
 *      ACCESSIBLE NAME — Next failure, Copy link to step, Detail level, Context & cost,
 *      Send, Stop run, Take over. Accessible name, not textContent, because that is what a
 *      screen reader announces and it is what catches a decorative glyph leaking into a
 *      label.
 *   2. Nothing is keyboard-only: the route registers no route-scoped key binding at all, and
 *      the deleted `f` chord does nothing.
 *   3. Copy link to step copies a link that names the selected step — including on the
 *      landing state, where the selection comes from defaultSelection() and the address bar
 *      carries no ?step= at all.
 *   4. MUI's `Alert` is an assertive live region by default and this screen uses it as a
 *      styling container; the landing state and a step selection must announce NOTHING.
 *   5. Stop opens a Dialog that says what stopping does, and stops nothing until confirmed.
 *   6. The empty state says what to do next, not just that there is nothing there.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRouter,
  RouterProvider,
  type AnyRouter,
} from "@tanstack/react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "../../../app/router";
import { useKeyboardDispatch } from "../../../lib/keyboard/hooks";
import { keyboard } from "../../../lib/keyboard/registry";

const T = "2026-08-25T12:00:00Z";

const user = {
  id: "u1",
  email: "spruce@example.com",
  display_name: "Spruce",
  role: "owner",
  avatar_color: "#4653cf",
  created_at: T,
};

const inheritedInt = (value: number) => ({ value, inherited: true, workspace_value: value });

const project = {
  id: "p1",
  key: "TEST",
  name: "Test project",
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

const agent = {
  id: "a1",
  project_id: "p1",
  name: "dev",
  role: "Implementer",
  color: "#0f7f9c",
  runtime_id: "rt1",
  model: "claude-sonnet-5",
  effort: "medium",
  autonomy: "supervised",
  permissions: { allow: [], deny: [], ask: [] },
  git_author_name: "dev",
  git_author_email: "dev@agents.lexicode.local",
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
  runs_week: 0,
  spend_week_cents: 0,
  success_rate: null,
};

/** A live run — live is the state with the most controls on screen (steer, stop, take over). */
const run = {
  id: "r1",
  seq: 42,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: null,
  trigger_id: null,
  state: "running",
  state_reason: "",
  hold_reason: "",
  autonomy: "supervised",
  directive_version_id: null,
  model: "claude-sonnet-5",
  effort: "medium",
  branch: "dev/run-42",
  subject_key: "",
  current_step: "rerunning the failing test",
  cost_cents: 34,
  tokens_in: 1200,
  tokens_out: 400,
  tokens_cache_read: 0,
  tokens_cache_write: 0,
  step_count: 4,
  error_message: "",
  takeover_note: "",
  queued_at: T,
  started_at: T,
  ended_at: null,
  acknowledged_at: null,
};

function activity(over: Record<string, unknown>) {
  return {
    seq: 0,
    type: "action",
    level: 1,
    tool_name: "",
    group_key: "",
    title: "",
    payload: null as unknown,
    ok: null as boolean | null,
    attempt: 1,
    duration_ms: 120,
    queued_ms: 10,
    model_ms: 60,
    tool_ms: 50,
    cost_cents: 0,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    created_at: T,
    ...over,
  };
}

/**
 * Two failures, so "Next failure" has somewhere to go and the (n) count is not 1-by-luck.
 * defaultSelection() picks seq 2 — the first failure — with no ?step= in the URL, which is
 * exactly the state the copy-link regression lived in.
 */
const ACTIVITIES = [
  activity({ seq: 1, type: "thought", level: 0, title: "Reading the failing test" }),
  activity({
    seq: 2,
    tool_name: "Bash",
    title: "go test ./...",
    ok: false,
    payload: { argv: ["bash", "-lc", "go test ./..."], exit: 1, stderr: "FAIL\n" },
  }),
  activity({ seq: 3, tool_name: "Read", title: "internal/run/loop.go", ok: true }),
  activity({
    seq: 4,
    type: "error",
    title: "the container exited",
    ok: false,
    payload: { subtype: "container_exit", result: "exit status 137" },
  }),
];

/** Swapped per case: the empty-state test renders the same run with no steps. */
let activities: ReturnType<typeof activity>[] = ACTIVITIES;

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
    },
  });
}

interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[] = [];

function stubNetwork(): void {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
      const path = String(input).replace(/^https?:\/\/[^/]+/, "");
      const method = init?.method ?? "GET";
      calls.push({
        method,
        path,
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      });
      if (path === "/api/v1/auth/me") return json(user);
      if (path === "/api/v1/projects") return json({ projects: [project] });
      if (path === "/api/v1/projects/TEST") return json(project);
      if (path === "/api/v1/projects/TEST/columns") return json({ columns: [] });
      if (path === "/api/v1/projects/TEST/labels") return json({ labels: [] });
      if (path.startsWith("/api/v1/projects/TEST/tickets")) return json({ tickets: [] });
      if (path.startsWith("/api/v1/projects/TEST/agents")) return json({ agents: [agent] });
      if (path === "/api/v1/projects/TEST/wiki/context-budget") {
        return json({ always_tokens: 0, threshold_tokens: 4000, pages: [] });
      }
      if (path === "/api/v1/projects/TEST/wiki") return json({ pages: [] });
      if (path.startsWith("/api/v1/projects/TEST/runs")) return json({ runs: [run] });
      if (path === "/api/v1/runs/r1") {
        return json({ run, outputs: [], context: [], messages: [], elicitations: [] });
      }
      if (path.startsWith("/api/v1/runs/r1/activities")) return json({ activities });
      if (path === "/api/v1/runs/r1/chain") return json({ chain: [] });
      if (path === "/api/v1/runs/r1/stop") return json({ run: { ...run, state: "canceled" } });
      if (path === "/api/v1/inbox") return json({ runs: [] });
      if (path === "/api/v1/notifications") return json({ notifications: [], unread: 0 });
      return json({ type: "not_found", title: "Not found", status: 404, detail: path }, 404);
    }),
  );
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
  close(): void {}
}

/** What "Copy link to step" put on the clipboard. */
let copied: string[] = [];

function stubClipboard(): void {
  copied = [];
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: {
      writeText: (text: string) => {
        copied.push(text);
        return Promise.resolve();
      },
    },
  });
}

/**
 * App.tsx's two responsibilities — the QueryClient and the global keydown dispatcher —
 * around a router this test owns. The app's `router` is a module singleton whose location
 * survives unmount, so cases would inherit each other's ?step=; a memory history per case is
 * what axe.test.tsx already does for the same reason. `useKeyboardDispatch` matters: without
 * it the "pressing f does nothing" assertion would pass because nothing listens at all.
 */
function Harness({ router, client }: { router: AnyRouter; client: QueryClient }) {
  useKeyboardDispatch();
  return (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}

async function openRunDetail(search = ""): Promise<AnyRouter> {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [`/p/TEST/runs/r1${search}`] }),
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<Harness router={router} client={client} />);
  // The header only exists once the run detail query resolves; before that the route is a
  // "Loading run…" line and every control assertion would be vacuously false.
  await waitFor(() => {
    screen.getByRole("heading", { name: "Run #42" });
  });
  return router;
}

/** The router's own idea of the current search string — the address bar under memory history. */
function searchOf(router: AnyRouter): string {
  return router.state.location.searchStr;
}

function writes(): Call[] {
  return calls.filter((c) => c.method !== "GET");
}

describe("the converted run detail: every action is a visible control (LEXI-13)", () => {
  beforeEach(() => {
    activities = ACTIVITIES;
    stubNetwork();
    stubClipboard();
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("names every promoted action as a labelled control, glyphs excluded", async () => {
    await openRunDetail();

    // The `f` chord's replacement, and it says how many failures there are.
    expect(screen.getByRole("button", { name: "Next failure (2)" })).toBeDefined();
    // The permalink, which had no control at all before the conversion.
    expect(screen.getByRole("button", { name: "Copy link to step" })).toBeDefined();
    // The verbosity control: a labelled group, not three bare buttons.
    expect(screen.getByText("Detail level")).toBeDefined();
    for (const level of ["Summary", "Normal", "Verbose"]) {
      expect(screen.getByRole("button", { name: level })).toBeDefined();
    }
    // Visible at every width now — it used to appear only below 1400px.
    expect(screen.getByRole("button", { name: "Context & cost" })).toBeDefined();
    // The S24 intervention surfaces.
    expect(screen.getByRole("button", { name: "Send" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Stop run" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Take over" })).toBeDefined();
    expect(screen.getByRole("textbox", { name: /Send a message to this run/ })).toBeDefined();
  });

  it("registers no route-scoped key binding — the `f` chord is gone, not rebound", async () => {
    const router = await openRunDetail();

    expect(keyboard.getBindings().filter((b) => b.scope === "route").map((b) => b.id)).toEqual([]);

    const before = searchOf(router);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "f", bubbles: true }));
    expect(searchOf(router)).toBe(before);
  });

  it("copies a link that names the selected step even before anything is clicked", async () => {
    const router = await openRunDetail();
    // The regression this pins: on load the selection is defaultSelection() — seq 2, the
    // first failure — while the URL still carries no ?step=. Copying window.location.href
    // verbatim handed out a link that re-resolves to the RECIPIENT's default selection,
    // which on a live run is the moving tail.
    expect(searchOf(router)).toBe("");

    screen.getByRole("button", { name: "Copy link to step" }).click();
    await waitFor(() => {
      if (copied.length === 0) throw new Error("nothing copied yet");
    });
    const url = new URL(copied[0]);
    expect(url.pathname).toBe("/p/TEST/runs/r1");
    expect(url.searchParams.get("step")).toBe("2");
  });

  it("Next failure moves the selection to the next failed step and into the URL", async () => {
    const router = await openRunDetail();
    screen.getByRole("button", { name: "Next failure (2)" }).click();

    // seq 2 was already selected, so the next one is seq 4 — and it is now shareable.
    await waitFor(() => {
      if (!searchOf(router).includes("step=4")) {
        throw new Error(`step not in the URL yet: "${searchOf(router)}"`);
      }
    });
    screen.getByRole("button", { name: "Copy link to step" }).click();
    await waitFor(() => {
      if (copied.length === 0) throw new Error("nothing copied yet");
    });
    expect(new URL(copied[0]).searchParams.get("step")).toBe("4");
  });

  it("announces nothing incidentally — step content is not an assertive live region", async () => {
    await openRunDetail();
    // MUI's Alert is role="alert" by default and this screen uses it as a styling container
    // (the Bash exit code, the Outcome card, the error card, the loop chain). The landing
    // state auto-selects the first FAILED step, so the exit-code banner is on screen.
    expect(document.body.textContent).toContain("exit 1");
    expect(document.querySelectorAll('[role="alert"]')).toHaveLength(0);

    // And it stays quiet as the user clicks through the timeline — the centre pane remounts
    // on every selection, so an assertive default would fire on every step.
    screen.getByRole("button", { name: "Next failure (2)" }).click();
    await waitFor(() => {
      if (!document.body.textContent?.includes("container_exit")) {
        throw new Error("the error step has not rendered yet");
      }
    });
    expect(document.querySelectorAll('[role="alert"]')).toHaveLength(0);

    // The one region that IS allowed to speak is the page's own, and it is polite.
    const live = document.querySelector('[aria-live]');
    expect(live?.getAttribute("aria-live")).toBe("polite");
  });

  it("Stop is a dialog that says what stopping does, and stops nothing until confirmed", async () => {
    await openRunDetail();
    screen.getByRole("button", { name: "Stop run" }).click();

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Stop this run?")).toBeDefined();
    expect(dialog.textContent).toContain("cannot be resumed");
    // A way out that is not the destructive one — the old inline toolbar swap had the
    // confirm land where the trigger had just been.
    expect(within(dialog).getByRole("button", { name: "Keep running" })).toBeDefined();
    expect(writes()).toEqual([]);

    within(dialog).getByRole("button", { name: "Stop run" }).click();
    await waitFor(() => {
      if (writes().length === 0) throw new Error("no stop request yet");
    });
    expect(writes()[0]).toEqual({
      method: "POST",
      path: "/api/v1/runs/r1/stop",
      body: { reason: "stopped by a human" },
    });
  });

  it("the empty state says what to do next and offers the way out", async () => {
    activities = [];
    await openRunDetail();

    expect(screen.getByRole("heading", { name: "Nothing has happened yet" })).toBeDefined();
    expect(document.body.textContent).toContain("its container is probably still starting");
    expect(document.body.textContent).toContain("No steps yet.");
    expect(screen.getByRole("link", { name: "See all runs" })).toBeDefined();
    // Nothing to jump to and nothing to link to, and both controls say so rather than
    // silently doing nothing.
    expect(screen.getByRole("button", { name: "Next failure" })).toHaveProperty("disabled", true);
    expect(screen.getByRole("button", { name: "Copy link to step" })).toHaveProperty(
      "disabled",
      true,
    );
  });
});
