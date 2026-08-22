/*
 * S36 push tiering (architecture §12): needs input / awaiting approval / failed push;
 * completed — rewritten as a review row — updates the badge silently. And the diffing rule
 * that keeps "permission at first occurrence, never on load" honest: the baseline pass
 * pushes nothing.
 */
import { describe, expect, it } from "vitest";

import type { Notification as NotificationRow } from "../api/client";
import { freshPushes, pushTier } from "./tier";

function row(over: Partial<NotificationRow>): NotificationRow {
  return {
    id: "n1",
    user_id: "u1",
    project_id: "p1",
    run_id: "r1",
    flavor: "question",
    title: "Dev asked a question",
    body: "Question: which retry strategy?",
    state: "unread",
    created_at: "2026-08-22T12:00:00Z",
    updated_at: "2026-08-22T12:00:00Z",
    ...over,
  };
}

describe("pushTier (state → push | badge)", () => {
  it("pushes needs input, awaiting approval and failed", () => {
    expect(pushTier("question")).toBe("push");
    expect(pushTier("approval")).toBe("push");
    expect(pushTier("failure")).toBe("push");
  });

  it("completed (review flavor) is badge-only, silently", () => {
    expect(pushTier("review")).toBe("badge");
  });

  it("an unknown flavor degrades to the quiet tier", () => {
    expect(pushTier("someday-new")).toBe("badge");
  });
});

describe("freshPushes", () => {
  it("the first data after page load pushes NOTHING (permission never requested on load)", () => {
    expect(freshPushes(null, [row({}), row({ id: "n2", flavor: "failure" })])).toEqual([]);
  });

  it("a row that appears after the baseline pushes", () => {
    const prev = new Map([["n1", "2026-08-22T12:00:00Z"]]);
    const fresh = freshPushes(prev, [row({}), row({ id: "n2", flavor: "approval" })]);
    expect(fresh.map((n) => n.id)).toEqual(["n2"]);
  });

  it("an in-place update (same id, new updated_at) pushes again; a mere refetch does not", () => {
    const prev = new Map([["n1", "2026-08-22T12:00:00Z"]]);
    expect(freshPushes(prev, [row({})])).toEqual([]);
    const updated = row({ updated_at: "2026-08-22T12:05:00Z", flavor: "failure" });
    expect(freshPushes(prev, [updated])).toEqual([updated]);
  });

  it("badge-tier and non-unread rows never push", () => {
    const prev = new Map<string, string>();
    expect(
      freshPushes(prev, [
        row({ id: "a", flavor: "review" }),
        row({ id: "b", state: "read" }),
        row({ id: "c", state: "dismissed" }),
      ]),
    ).toEqual([]);
  });
});
