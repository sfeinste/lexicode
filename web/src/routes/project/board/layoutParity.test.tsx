/*
 * S11 acceptance: `group_by=delegate` produces IDENTICAL group membership in the board
 * layout and the list layout — same fixture, both real renderings, through the real router
 * and the real query layer (fetch stubbed at the network boundary).
 */
import { render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// ---- network fixture --------------------------------------------------------------------

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
    ticket_count: 6,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
];

let seq = 0;
function ticket(delegate: string | null) {
  seq += 1;
  return {
    id: `t${seq}`,
    project_id: "p1",
    seq,
    key: `TEST-${seq}`,
    title: `Ticket number ${seq}`,
    description: "",
    column_id: "col1",
    category: "backlog",
    position: seq,
    priority: "none",
    assignee_id: null,
    delegate_agent_id: delegate,
    parent_id: null,
    pr_number: null,
    pr_state: null,
    branch: null,
    origin: "human",
    created_by_user_id: null,
    created_by_agent_id: null,
    label_ids: [],
    criteria_total: 0,
    criteria_checked: 0,
    archived_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

const tickets = [
  ticket("agent-dev"),
  ticket(null),
  ticket("agent-docs"),
  ticket("agent-dev"),
  ticket(null),
  ticket("agent-dev"),
];

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubNetwork(): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const url = String(input);
      const path = url.replace(/^https?:\/\/[^/]+/, "");
      if (path === "/api/v1/auth/me") return json(user);
      if (path === "/api/v1/projects") return json({ projects: [] });
      if (path === "/api/v1/projects/TEST") return json(project);
      if (path === "/api/v1/projects/TEST/columns") return json({ columns });
      if (path === "/api/v1/projects/TEST/tickets") return json({ tickets });
      if (path === "/api/v1/projects/TEST/labels") return json({ labels: [] });
      // The S21/S22 needs-you view does not exist yet: the query must treat 404 as empty.
      if (path.startsWith("/api/v1/projects/TEST/runs")) {
        return json({ type: "not_found", title: "Not found", status: 404 }, 404);
      }
      throw new Error(`unstubbed fetch: ${path}`);
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

/** aria-label of each group section → the ticket keys rendered inside it, in order. */
function membership(): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const sec of document.querySelectorAll("section[aria-label]")) {
    const name = sec.getAttribute("aria-label") ?? "";
    if (name.startsWith("Needs you")) continue;
    out[name] = [...sec.querySelectorAll("[data-ticket-key]")].map(
      (el) => el.getAttribute("data-ticket-key") ?? "",
    );
  }
  return out;
}

describe("board and list layouts share group membership (group_by=delegate)", () => {
  beforeEach(() => {
    stubNetwork();
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the same delegate groups with the same tickets in both layouts", async () => {
    window.history.replaceState(null, "", "/p/TEST/board?group_by=delegate");
    const { App } = await import("../../../app/App");
    const { router } = await import("../../../app/router");

    render(<App />);

    await waitFor(() => {
      if (document.querySelectorAll("[data-ticket-key]").length === 0) {
        throw new Error("board not rendered yet");
      }
    });

    const boardMembership = membership();
    expect(boardMembership).toEqual({
      "agent-dev": ["TEST-1", "TEST-4", "TEST-6"],
      "agent-docs": ["TEST-3"],
      "No delegate": ["TEST-2", "TEST-5"],
    });

    // ⌘B: same URL, layout=list.
    await router.navigate({
      to: "/p/$key/board",
      params: { key: "TEST" },
      search: { group_by: "delegate", layout: "list" },
    });

    await waitFor(() => {
      const btn = [...document.querySelectorAll("button")].find(
        (b) => b.textContent === "List",
      );
      if (!btn?.hasAttribute("data-active")) throw new Error("list layout not active yet");
    });

    const listMembership = membership();
    expect(listMembership).toEqual(boardMembership);
  });
});
