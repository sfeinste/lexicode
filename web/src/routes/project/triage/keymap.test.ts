import { beforeEach, describe, expect, it, vi } from "vitest";

import { KeyboardRegistry } from "../../../lib/keyboard/registry";
import { buildTriageBindings, type TriageKeyActions } from "./keymap";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

function actions(over: Partial<TriageKeyActions> = {}): TriageKeyActions {
  return {
    moveSelection: vi.fn(),
    peek: vi.fn(),
    openSelected: vi.fn(),
    clearSelection: vi.fn(),
    accept: vi.fn(),
    duplicate: vi.fn(),
    decline: vi.fn(),
    snooze: vi.fn(),
    hasSelection: () => true,
    ...over,
  };
}

describe("buildTriageBindings (UI spec §6, triage scope)", () => {
  it("covers the §6 triage key map — 1/2/3/H verbs plus the list grammar — route-scoped", () => {
    const bindings = buildTriageBindings(actions());
    const chords = new Map(bindings.map((b) => [b.id, b.chord]));
    expect(chords).toEqual(
      new Map([
        ["triage.next", "j"],
        ["triage.prev", "k"],
        ["triage.peek", "space"],
        ["triage.open", "enter"],
        ["triage.back", "escape"],
        ["triage.accept", "1"],
        ["triage.duplicate", "2"],
        ["triage.decline", "3"],
        ["triage.snooze", "h"],
      ]),
    );
    expect(bindings.every((b) => b.scope === "route")).toBe(true);
  });

  describe("dispatch through the registry", () => {
    let reg: KeyboardRegistry;
    let a: TriageKeyActions;
    let deactivate: () => void;

    beforeEach(() => {
      reg = new KeyboardRegistry();
      a = actions();
      for (const b of buildTriageBindings(a)) reg.register(b);
      deactivate = reg.activateScope("route");
      return () => deactivate();
    });

    it("J/K move the selection, Space peeks, Enter opens", () => {
      reg.handleKeydown(keydown({ key: "j" }));
      expect(a.moveSelection).toHaveBeenLastCalledWith(1);
      reg.handleKeydown(keydown({ key: "k" }));
      expect(a.moveSelection).toHaveBeenLastCalledWith(-1);
      reg.handleKeydown(keydown({ key: " " }));
      expect(a.peek).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "Enter" }));
      expect(a.openSelected).toHaveBeenCalledTimes(1);
    });

    it("1 accepts, 2 marks duplicate, 3 declines, H snoozes", () => {
      reg.handleKeydown(keydown({ key: "1" }));
      expect(a.accept).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "2" }));
      expect(a.duplicate).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "3" }));
      expect(a.decline).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "h" }));
      expect(a.snooze).toHaveBeenCalledTimes(1);
    });

    it("the verbs are gated on a selection; J/K are not", () => {
      const gated = actions({ hasSelection: () => false });
      const reg2 = new KeyboardRegistry();
      for (const b of buildTriageBindings(gated)) reg2.register(b);
      const off = reg2.activateScope("route");

      reg2.handleKeydown(keydown({ key: "1" }));
      reg2.handleKeydown(keydown({ key: "2" }));
      reg2.handleKeydown(keydown({ key: "3" }));
      reg2.handleKeydown(keydown({ key: "h" }));
      reg2.handleKeydown(keydown({ key: " " }));
      reg2.handleKeydown(keydown({ key: "Enter" }));
      expect(gated.accept).not.toHaveBeenCalled();
      expect(gated.duplicate).not.toHaveBeenCalled();
      expect(gated.decline).not.toHaveBeenCalled();
      expect(gated.snooze).not.toHaveBeenCalled();
      expect(gated.peek).not.toHaveBeenCalled();
      expect(gated.openSelected).not.toHaveBeenCalled();

      reg2.handleKeydown(keydown({ key: "j" }));
      expect(gated.moveSelection).toHaveBeenCalledTimes(1);
      off();
    });
  });
});
