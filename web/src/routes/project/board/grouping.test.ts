import { describe, expect, it } from "vitest";

import type { Column, Label, Ticket } from "../../../lib/api/client";
import {
  NONE_GROUP,
  dropPosition,
  filterTickets,
  flattenGroups,
  groupTickets,
} from "./grouping";

// ---- fixture ----------------------------------------------------------------------------

let seq = 0;
function ticket(over: Partial<Ticket>): Ticket {
  seq += 1;
  return {
    id: `t${seq}`,
    project_id: "p1",
    seq,
    key: `PAY-${seq}`,
    title: `Ticket ${seq}`,
    description: "",
    column_id: "col-backlog",
    category: "backlog",
    position: seq,
    priority: "none",
    assignee_id: null,
    delegate_agent_id: null,
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
    ...over,
  };
}

function column(id: string, name: string, position: number): Column {
  return {
    id,
    project_id: "p1",
    name,
    category: "backlog",
    position,
    wip_limit: null,
    auto_start_delegate: false,
    ticket_count: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

const label = (id: string, name: string): Label => ({
  id,
  project_id: "p1",
  name,
  color: "#ff0000",
});

const columns = [column("col-doing", "Doing", 2), column("col-backlog", "Backlog", 1)];

describe("groupTickets", () => {
  it("status: one group per column in position order, tickets in fractional order", () => {
    const tickets = [
      ticket({ id: "a", column_id: "col-doing", position: 5 }),
      ticket({ id: "b", column_id: "col-backlog", position: 2 }),
      ticket({ id: "c", column_id: "col-backlog", position: 1.5 }),
    ];
    const groups = groupTickets(tickets, "status", { columns, labels: [] });
    expect(groups.map((g) => g.id)).toEqual(["col-backlog", "col-doing"]);
    expect(groups[0].tickets.map((t) => t.id)).toEqual(["c", "b"]);
    expect(groups[1].tickets.map((t) => t.id)).toEqual(["a"]);
    expect(groups[0].column?.name).toBe("Backlog");
  });

  it("status: empty columns still render (they are drop targets)", () => {
    const groups = groupTickets([], "status", { columns, labels: [] });
    expect(groups).toHaveLength(2);
    expect(groups.every((g) => g.tickets.length === 0)).toBe(true);
  });

  it("priority: all five buckets, urgent first", () => {
    const tickets = [ticket({ priority: "low" }), ticket({ priority: "urgent" })];
    const groups = groupTickets(tickets, "priority", { columns, labels: [] });
    expect(groups.map((g) => g.id)).toEqual(["urgent", "high", "medium", "low", "none"]);
    expect(groups[0].tickets).toHaveLength(1);
    expect(groups[3].tickets).toHaveLength(1);
  });

  it("delegate: one group per agent, unset group last, empty unset omitted", () => {
    const tickets = [
      ticket({ id: "a", delegate_agent_id: "dev" }),
      ticket({ id: "b", delegate_agent_id: "docs" }),
      ticket({ id: "c", delegate_agent_id: "dev" }),
    ];
    const groups = groupTickets(tickets, "delegate", { columns, labels: [] });
    expect(groups.map((g) => g.id)).toEqual(["dev", "docs"]);
    expect(groups[0].tickets.map((t) => t.id)).toEqual(["a", "c"]);

    const withUnset = groupTickets([...tickets, ticket({ id: "d" })], "delegate", {
      columns,
      labels: [],
    });
    expect(withUnset.map((g) => g.id)).toEqual(["dev", "docs", NONE_GROUP]);
    expect(withUnset[2].name).toBe("No delegate");
  });

  it("assignee: groups by assignee_id with Unassigned last", () => {
    const tickets = [ticket({ assignee_id: "u1" }), ticket({})];
    const groups = groupTickets(tickets, "assignee", { columns, labels: [] });
    expect(groups.map((g) => g.id)).toEqual(["u1", NONE_GROUP]);
    expect(groups[1].name).toBe("Unassigned");
  });

  it("label: a ticket with two labels appears in both groups; No label last", () => {
    const labels = [label("l2", "bug"), label("l1", "api")];
    const tickets = [
      ticket({ id: "a", label_ids: ["l1", "l2"] }),
      ticket({ id: "b", label_ids: ["l2"] }),
      ticket({ id: "c" }),
    ];
    const groups = groupTickets(tickets, "label", { columns, labels });
    expect(groups.map((g) => g.name)).toEqual(["api", "bug", "No label"]);
    expect(groups[0].tickets.map((t) => t.id)).toEqual(["a"]);
    expect(groups[1].tickets.map((t) => t.id)).toEqual(["a", "b"]);
    expect(groups[2].tickets.map((t) => t.id)).toEqual(["c"]);
  });

  it("label: labels with no tickets get no group", () => {
    const groups = groupTickets([ticket({ id: "a", label_ids: ["l1"] })], "label", {
      columns,
      labels: [label("l1", "api"), label("l9", "unused")],
    });
    expect(groups.map((g) => g.id)).toEqual(["l1"]);
  });
});

describe("filterTickets", () => {
  const tickets = [
    ticket({ id: "a", title: "Add idempotency keys", assignee_id: "u1", priority: "high" }),
    ticket({ id: "b", title: "Fix retries", delegate_agent_id: "dev", label_ids: ["l1"] }),
    ticket({ id: "c", title: "Idempotent charge API" }),
  ];

  it("filters by each chip, with 'none' meaning unset", () => {
    expect(filterTickets(tickets, { assignee: "u1" }).map((t) => t.id)).toEqual(["a"]);
    expect(filterTickets(tickets, { assignee: NONE_GROUP }).map((t) => t.id)).toEqual([
      "b",
      "c",
    ]);
    expect(filterTickets(tickets, { delegate: "dev" }).map((t) => t.id)).toEqual(["b"]);
    expect(filterTickets(tickets, { priority: "high" }).map((t) => t.id)).toEqual(["a"]);
    expect(filterTickets(tickets, { label: "l1" }).map((t) => t.id)).toEqual(["b"]);
    expect(filterTickets(tickets, { label: NONE_GROUP }).map((t) => t.id)).toEqual(["a", "c"]);
  });

  it("search matches key and title, case-insensitive", () => {
    expect(filterTickets(tickets, { q: "idempoten" }).map((t) => t.id)).toEqual(["a", "c"]);
    const byKey = filterTickets(tickets, { q: tickets[1].key.toLowerCase() });
    expect(byKey.map((t) => t.id)).toEqual(["b"]);
  });

  it("filters compose (AND)", () => {
    expect(filterTickets(tickets, { assignee: "u1", q: "retries" })).toHaveLength(0);
  });
});

describe("flattenGroups", () => {
  it("walks groups in order and drops duplicate tickets (label grouping)", () => {
    const a = ticket({ id: "a" });
    const b = ticket({ id: "b" });
    const flat = flattenGroups([
      { id: "g1", name: "g1", tickets: [a, b] },
      { id: "g2", name: "g2", tickets: [a] },
    ]);
    expect(flat.map((t) => t.id)).toEqual(["a", "b"]);
  });
});

describe("dropPosition", () => {
  const list = [
    ticket({ id: "a", position: 1 }),
    ticket({ id: "b", position: 2 }),
    ticket({ id: "c", position: 4 }),
  ];

  it("empty group → 1", () => {
    expect(dropPosition([], 0)).toBe(1);
  });
  it("top → before the first", () => {
    expect(dropPosition(list, 0)).toBe(0);
  });
  it("end → after the last", () => {
    expect(dropPosition(list, 3)).toBe(5);
  });
  it("between → the midpoint (fractional)", () => {
    expect(dropPosition(list, 2)).toBe(3);
    expect(dropPosition(list, 1)).toBe(1.5);
  });
});
