import { describe, expect, it } from "vitest";

import type { Ticket } from "../../../lib/api/client";
import { cardBadges } from "./badges";

function ticket(over: Partial<Ticket>): Ticket {
  return {
    id: "t1",
    project_id: "p1",
    seq: 1,
    key: "PAY-14",
    title: "Add idempotency keys to charge API",
    description: "",
    column_id: "c1",
    category: "ready",
    position: 1,
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

describe("cardBadges (§5.3: badges only when earned)", () => {
  it("a bare ticket earns nothing — no empty badge slots", () => {
    const b = cardBadges(ticket({}));
    expect(b.criteria).toBeNull();
    expect(b.pr).toBeNull();
    expect(b.hasPeopleRow).toBe(false);
  });

  it("criteria progress appears only when criteria exist", () => {
    expect(cardBadges(ticket({ criteria_total: 5, criteria_checked: 3 })).criteria).toBe(
      "3/5 acceptance criteria",
    );
    expect(cardBadges(ticket({ criteria_total: 0 })).criteria).toBeNull();
  });

  it("zero-checked progress still renders once criteria exist", () => {
    expect(cardBadges(ticket({ criteria_total: 2, criteria_checked: 0 })).criteria).toBe(
      "0/2 acceptance criteria",
    );
  });

  it("the PR chip appears only with a linked PR", () => {
    expect(cardBadges(ticket({ pr_number: 219 })).pr).toBe("#219");
    expect(cardBadges(ticket({})).pr).toBeNull();
  });

  it("the people row earns itself from either the delegate or the assignee", () => {
    expect(cardBadges(ticket({ delegate_agent_id: "dev" })).hasPeopleRow).toBe(true);
    expect(cardBadges(ticket({ assignee_id: "u1" })).hasPeopleRow).toBe(true);
    const both = cardBadges(ticket({ delegate_agent_id: "dev", assignee_id: "u1" }));
    expect(both.delegate).toBe("dev");
    expect(both.assignee).toBe("u1");
  });
});
