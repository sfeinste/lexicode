/*
 * S36: the inbox keyboard map (UI spec §5.10 / §6) — J/K/Enter/A/X, all route-scoped,
 * dispatched through the S07 registry — and the inbox sort rule: approvals to the top
 * ALWAYS, whatever their age.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { NeedsYouRun } from "../../lib/api/client";
import { KeyboardRegistry } from "../../lib/keyboard/registry";
import { buildInboxBindings, type InboxKeyActions } from "./keymap";
import { sortForInbox } from "./needsYouView";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

function actions(over: Partial<InboxKeyActions> = {}): InboxKeyActions {
  return {
    moveSelection: vi.fn(),
    openSelected: vi.fn(),
    primaryAction: vi.fn(),
    dismissSelected: vi.fn(),
    hasSelection: () => true,
    ...over,
  };
}

describe("buildInboxBindings (UI spec §5.10: J/K/Enter/A/X)", () => {
  it("covers the full inbox key map, all route-scoped", () => {
    const bindings = buildInboxBindings(actions());
    const chords = new Map(bindings.map((b) => [b.id, b.chord]));
    expect(chords).toEqual(
      new Map([
        ["inbox.next", "j"],
        ["inbox.prev", "k"],
        ["inbox.open", "enter"],
        ["inbox.act", "a"],
        ["inbox.dismiss", "x"],
      ]),
    );
    expect(bindings.every((b) => b.scope === "route")).toBe(true);
  });

  describe("dispatch through the registry", () => {
    let reg: KeyboardRegistry;
    let a: InboxKeyActions;
    let deactivate: () => void;

    beforeEach(() => {
      reg = new KeyboardRegistry();
      a = actions();
      buildInboxBindings(a).forEach((b) => reg.register(b));
      deactivate = reg.activateScope("route");
      return () => deactivate();
    });

    it("J and K walk the rows", () => {
      reg.handleKeydown(keydown({ key: "j" }));
      expect(a.moveSelection).toHaveBeenLastCalledWith(1);
      reg.handleKeydown(keydown({ key: "k" }));
      expect(a.moveSelection).toHaveBeenLastCalledWith(-1);
    });

    it("Enter opens, A fires the primary action, X dismisses", () => {
      reg.handleKeydown(keydown({ key: "Enter" }));
      expect(a.openSelected).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "a" }));
      expect(a.primaryAction).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "x" }));
      expect(a.dismissSelected).toHaveBeenCalledTimes(1);
    });

    it("selection-gated chords stay quiet without a selection", () => {
      const none = actions({ hasSelection: () => false });
      const reg2 = new KeyboardRegistry();
      buildInboxBindings(none).forEach((b) => reg2.register(b));
      const off = reg2.activateScope("route");
      reg2.handleKeydown(keydown({ key: "Enter" }));
      reg2.handleKeydown(keydown({ key: "a" }));
      reg2.handleKeydown(keydown({ key: "x" }));
      expect(none.openSelected).not.toHaveBeenCalled();
      expect(none.primaryAction).not.toHaveBeenCalled();
      expect(none.dismissSelected).not.toHaveBeenCalled();
      off();
    });
  });
});

function row(over: Partial<NeedsYouRun>): NeedsYouRun {
  return {
    kind: "run",
    id: Math.random().toString(36).slice(2),
    project_key: "PAY",
    ticket_id: null,
    ticket_key: null,
    ticket_title: null,
    agent: "Dev",
    flavor: "question",
    status: "needs_input",
    started_at: "2026-08-22T12:00:00Z",
    ...over,
  };
}

describe("sortForInbox (UI spec §5.10: approvals to the top ALWAYS)", () => {
  it("puts approvals first regardless of age or the service's flavor-rank order", () => {
    // The service order: question → approval → failure → review, oldest first — with the
    // approval the YOUNGEST row of all.
    const oldQuestion = row({ id: "q", started_at: "2026-08-20T00:00:00Z" });
    const youngApproval = row({
      id: "ap",
      flavor: "approval",
      status: "awaiting_approval",
      started_at: "2026-08-22T13:59:00Z",
    });
    const failure = row({ id: "f", flavor: "failure", status: "failed" });
    const review = row({ id: "pr", kind: "pull_request", flavor: "review", status: "open" });
    const sorted = sortForInbox([oldQuestion, youngApproval, failure, review]);
    expect(sorted.map((r) => r.id)).toEqual(["ap", "q", "f", "pr"]);
  });

  it("is stable: everything else keeps the service order", () => {
    const rows = [
      row({ id: "q1" }),
      row({ id: "q2" }),
      row({ id: "a1", flavor: "approval" }),
      row({ id: "f1", flavor: "failure" }),
      row({ id: "a2", flavor: "approval" }),
    ];
    expect(sortForInbox(rows).map((r) => r.id)).toEqual(["a1", "a2", "q1", "q2", "f1"]);
  });
});
