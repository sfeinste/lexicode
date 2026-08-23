/*
 * UI spec §5.3 names three ways a run starts: "`D` (delegate), the Run button on the
 * ticket, or a trigger". The Run button did not exist — this file is the button's contract.
 *
 * Two rules it pins, because they are the ones that make the sidebar honest:
 *   - Run posts to the delegate endpoint (start), and is disabled with a stated reason when
 *     there is no delegate to run.
 *   - The delegate <select> beside it stays a plain PATCH (the field editor: "who WOULD
 *     run" — what auto_start_delegate columns and triggers read). Editing it starts nothing.
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

/** The delegate under test; each case sets it before rendering. */
let delegateAgentID: string | null = "a1";

function ticket() {
  return {
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
    delegate_agent_id: delegateAgentID,
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
}

const queuedRun = {
  id: "r1",
  seq: 12,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: "t1",
  trigger_id: null,
  state: "queued",
  state_reason: "delegate button",
  hold_reason: "",
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

let calls: Call[] = [];
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
      if (path.startsWith("/api/v1/projects/TEST/tickets")) return json({ tickets: [ticket()] });
      if (path === "/api/v1/projects/TEST/labels") return json({ labels: [] });
      if (path === "/api/v1/projects/TEST/wiki") return json({ pages: [] });
      if (path === "/api/v1/users") return json({ users: [user] });
      if (path.startsWith("/api/v1/projects/TEST/agents")) return json({ agents: [agent] });
      if (path === "/api/v1/tickets/t1") {
        return json({ ...ticket(), criteria: [], labels: [], children: [] });
      }
      if (path === "/api/v1/tickets/t1/stream") return json({ entries: [] });
      if (path === "/api/v1/tickets/t1/delegate") return delegateReply();
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

async function openTicket(): Promise<void> {
  window.history.replaceState(null, "", "/p/TEST/t/1");
  const { App } = await import("../../../app/App");
  const { queryClient } = await import("../../../lib/api/queryClient");
  queryClient.clear();
  render(<App />);
  // The sidebar renders before the agent roster resolves; the delegate <select> only exists
  // once it has, and the Run button's enablement is derived from it. Wait for both.
  await waitFor(() => {
    if (runButton() === undefined) throw new Error("ticket detail not rendered yet");
    const option = document.querySelector(
      'select[aria-label="Delegate (agent)"] option[value="a1"]',
    );
    // Until the roster resolves the delegate renders as the "(disabled agent)" fallback —
    // wait for the real name, or the Run button is judged before it knows the agent.
    if (option?.textContent !== "dev") throw new Error("agent roster not loaded yet");
  });
}

function runButton(): HTMLButtonElement | undefined {
  return [...document.querySelectorAll("button")].find((b) =>
    (b.textContent ?? "").includes("Run delegate now"),
  ) as HTMLButtonElement | undefined;
}

function writes(): Call[] {
  return calls.filter((c) => c.method !== "GET");
}

describe("the ticket's Run button (UI spec §5.3, §5.4 sidebar)", () => {
  beforeEach(() => {
    delegateAgentID = "a1";
    delegateReply = () => json({ run_id: "r1" }, 201);
    stubNetwork();
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts to the delegate endpoint and reports the run it queued", async () => {
    await openTicket();
    expect(runButton()?.disabled).toBe(false);
    runButton()?.click();

    await waitFor(() => {
      if (writes().length === 0) throw new Error("no write yet");
    });
    expect(writes()[0]).toEqual({
      method: "POST",
      path: "/api/v1/tickets/t1/delegate",
      body: { agent_id: "a1" },
    });

    await waitFor(() => {
      const notice = document.querySelector('[role="status"]');
      if (!notice?.textContent?.includes("Run #12")) {
        throw new Error(`no run notice yet: ${notice?.textContent ?? "(none)"}`);
      }
    });
    const notice = document.querySelector('[role="status"]');
    expect(notice?.textContent).toContain("Queued");
    expect(notice?.querySelector("a")?.getAttribute("href")).toBe("/p/TEST/runs/r1");
  });

  it("is disabled with the reason said out loud when there is no delegate", async () => {
    delegateAgentID = null;
    await openTicket();
    expect(runButton()?.disabled).toBe(true);
    expect(document.body.textContent).toContain("No delegate yet — pick one below, then run.");
    runButton()?.click();
    expect(writes()).toEqual([]);
  });

  it("surfaces a refusal's detail inline instead of failing silently", async () => {
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
    await openTicket();
    runButton()?.click();

    await waitFor(() => {
      if (document.querySelector('[role="alert"]') === null) throw new Error("no alert yet");
    });
    const alert = document.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain("No run started");
    expect(alert?.textContent).toContain("This agent is disabled.");
    expect(document.querySelector('[role="status"]')).toBeNull();
  });

  it("the sidebar delegate select stays a PATCH — the field editor starts nothing", async () => {
    delegateAgentID = null;
    await openTicket();
    const select = document.querySelector<HTMLSelectElement>(
      'select[aria-label="Delegate (agent)"]',
    );
    if (select === null) throw new Error("no delegate select");
    select.value = "a1";
    select.dispatchEvent(new Event("change", { bubbles: true }));

    await waitFor(() => {
      if (writes().length === 0) throw new Error("no write yet");
    });
    expect(writes()[0]).toEqual({
      method: "PATCH",
      path: "/api/v1/tickets/t1",
      body: { delegate_agent_id: "a1" },
    });
    expect(writes().some((c) => c.path.endsWith("/delegate"))).toBe(false);
  });
});
