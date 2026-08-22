/*
 * UI spec §5.3, verbatim: "Drag never starts a run. Starting is `D` (delegate), the Run
 * button on the ticket, or a trigger." This file pins the `D` half against the regression
 * that shipped: the picker used to PATCH `delegate_agent_id`, which sets a field and starts
 * nothing, so a user who pressed `D` and picked an agent got silence.
 *
 * Real App, real router, real query layer; fetch stubbed at the network boundary and every
 * request recorded, so the assertions are about the wire — which is exactly the layer an
 * API-level test cannot check, because the API was always right.
 */
import { render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const user = {
  id: "u1",
  email: "spruce@example.com",
  display_name: "Spruce",
  role: "owner",
  avatar_color: "#4653cf",
  created_at: "2026-01-01T00:00:00Z",
};

const project = {
  id: "p1",
  key: "TEST",
  name: "Test project",
  description: "",
  archived_at: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const columns = [
  {
    id: "col1",
    project_id: "p1",
    name: "Backlog",
    category: "backlog",
    position: 1,
    wip_limit: null,
    auto_start_delegate: false,
    ticket_count: 1,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

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
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  runs_week: 0,
  spend_week_cents: 0,
  success_rate: null,
};

const ticket = {
  id: "t1",
  project_id: "p1",
  seq: 1,
  key: "TEST-1",
  title: "Add idempotency keys",
  description: "",
  column_id: "col1",
  category: "backlog",
  position: 1,
  priority: "none",
  assignee_id: null,
  delegate_agent_id: null as string | null,
  parent_id: null,
  pr_number: null,
  pr_state: null,
  branch: null,
  origin: "human",
  created_by_user_id: "u1",
  created_by_agent_id: null,
  label_ids: [] as string[],
  criteria_total: 0,
  criteria_checked: 0,
  archived_at: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const queuedRun = {
  id: "r1",
  seq: 7,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: "t1",
  trigger_id: null,
  state: "queued",
  state_reason: "delegate button",
  hold_reason: "waiting: dev is at its 1-run limit",
  autonomy: "supervised",
  directive_version_id: null,
  model: "claude-sonnet-5",
  effort: "medium",
  branch: null,
  subject_key: "ticket:TEST-1",
  current_step: "",
  cost_cents: 0,
  tokens_in: 0,
  tokens_out: 0,
  tokens_cache_read: 0,
  tokens_cache_write: 0,
  step_count: 0,
  error_message: "",
  takeover_note: "",
  queued_at: "2026-01-01T00:00:00Z",
  started_at: null,
  ended_at: null,
  acknowledged_at: null,
};

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

/** Every request the page makes, so "which endpoint" is assertable. */
let calls: Call[] = [];
/** What POST /tickets/t1/delegate answers with. */
let delegateReply: () => Response = () => json({ run_id: "r1" }, 201);

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
      if (path === "/api/v1/projects") return json({ projects: [] });
      if (path === "/api/v1/projects/TEST") return json(project);
      if (path === "/api/v1/projects/TEST/columns") return json({ columns });
      if (path === "/api/v1/projects/TEST/tickets") return json({ tickets: [ticket] });
      if (path === "/api/v1/projects/TEST/labels") return json({ labels: [] });
      if (path.startsWith("/api/v1/projects/TEST/agents")) return json({ agents: [agent] });
      if (path === "/api/v1/tickets/t1/delegate") return delegateReply();
      if (path === "/api/v1/tickets/t1" && method === "PATCH") return json(ticket);
      if (path === "/api/v1/runs/r1") {
        return json({ run: queuedRun, outputs: [], context: [], messages: [], elicitations: [] });
      }
      if (path.startsWith("/api/v1/projects/TEST/runs")) {
        return json({ type: "not_found", title: "Not found", status: 404 }, 404);
      }
      throw new Error(`unstubbed fetch: ${method} ${path}`);
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
  close(): void {}
}

/** Open the board with TEST-1 under the cursor and press `D`. */
async function openDelegatePicker(): Promise<void> {
  window.history.replaceState(null, "", "/p/TEST/board?sel=TEST-1");
  const { App } = await import("../../../app/App");
  // The QueryClient is a module singleton shared by every test in the file; clear it so a
  // case's fixture is what the page renders, not the previous case's cached ticket.
  const { queryClient } = await import("../../../lib/api/queryClient");
  queryClient.clear();
  render(<App />);
  await waitFor(() => {
    if (document.querySelectorAll("[data-ticket-key]").length === 0) {
      throw new Error("board not rendered yet");
    }
  });
  window.dispatchEvent(new KeyboardEvent("keydown", { key: "d", bubbles: true }));
  await waitFor(() => {
    if (document.querySelector('[role="dialog"]') === null) {
      throw new Error("picker not open yet");
    }
  });
}

function pickerOption(text: string): HTMLButtonElement {
  const dialog = document.querySelector('[role="dialog"]');
  const btn = [...(dialog?.querySelectorAll("button") ?? [])].find((b) =>
    (b.textContent ?? "").startsWith(text),
  );
  if (btn === undefined) throw new Error(`no picker option starting with "${text}"`);
  return btn as HTMLButtonElement;
}

function writes(): Call[] {
  return calls.filter((c) => c.method !== "GET");
}

describe("the board's `D` picker starts a run (UI spec §5.3)", () => {
  beforeEach(() => {
    delegateReply = () => json({ run_id: "r1" }, 201);
    stubNetwork();
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts to the delegate endpoint — never a bare PATCH of the field", async () => {
    await openDelegatePicker();
    pickerOption("dev").click();

    await waitFor(() => {
      if (writes().length === 0) throw new Error("no write yet");
    });
    expect(writes()[0]).toEqual({
      method: "POST",
      path: "/api/v1/tickets/t1/delegate",
      body: { agent_id: "a1" },
    });
    // The regression: a PATCH would set the field and start nothing.
    expect(writes().some((c) => c.method === "PATCH")).toBe(false);
  });

  it("says where the run actually is — queued with its hold reason, linked", async () => {
    await openDelegatePicker();
    pickerOption("dev").click();

    await waitFor(() => {
      const notice = document.querySelector('[role="status"]');
      if (notice === null || !notice.textContent?.includes("Run #7")) {
        throw new Error(`no run notice yet: ${notice?.textContent ?? "(none)"}`);
      }
    });
    const notice = document.querySelector('[role="status"]');
    expect(notice?.textContent).toContain("Run #7");
    expect(notice?.textContent).toContain("dev");
    expect(notice?.textContent).toContain("waiting: dev is at its 1-run limit");
    expect(notice?.querySelector("a")?.getAttribute("href")).toBe("/p/TEST/runs/r1");
  });

  it("surfaces a refusal's detail inline instead of doing nothing visible", async () => {
    delegateReply = () =>
      json(
        {
          type: "validation_failed",
          title: "Validation failed",
          status: 400,
          detail: "One or more fields are invalid.",
          errors: [{ field: "agent_id", message: "This agent is disabled." }],
        },
        400,
      );
    await openDelegatePicker();
    pickerOption("dev").click();

    await waitFor(() => {
      const alert = document.querySelector('[role="alert"]');
      if (alert === null) throw new Error("no alert yet");
    });
    const alert = document.querySelector('[role="alert"]');
    // The field message, not the generic "One or more fields are invalid."
    expect(alert?.textContent).toContain("This agent is disabled.");
    expect(alert?.textContent).toContain("TEST-1");
    expect(alert?.textContent).toContain("no run started");
    expect(document.querySelector('[role="status"]')).toBeNull();
  });

  it("clearing the delegate stays a PATCH — clearing a field starts nothing", async () => {
    ticket.delegate_agent_id = "a1";
    try {
      await openDelegatePicker();
      pickerOption("No delegate").click();

      await waitFor(() => {
        if (writes().length === 0) throw new Error("no write yet");
      });
      expect(writes()[0]).toEqual({
        method: "PATCH",
        path: "/api/v1/tickets/t1",
        body: { delegate_agent_id: null },
      });
      expect(writes().some((c) => c.path.endsWith("/delegate"))).toBe(false);
    } finally {
      ticket.delegate_agent_id = null;
    }
  });
});
