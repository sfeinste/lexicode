import { beforeEach, describe, expect, it, vi } from "vitest";

import { KeyboardRegistry } from "../../../lib/keyboard/registry";
import { buildBoardBindings, type BoardKeyActions } from "./keymap";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

function actions(over: Partial<BoardKeyActions> = {}): BoardKeyActions {
  return {
    createTicket: vi.fn(),
    moveSelection: vi.fn(),
    peek: vi.fn(),
    openSelected: vi.fn(),
    toggleMultiSelect: vi.fn(),
    clearSelection: vi.fn(),
    openPicker: vi.fn(),
    edit: vi.fn(),
    rename: vi.fn(),
    toggleLayout: vi.fn(),
    toggleDisplayMenu: vi.fn(),
    hasSelection: () => true,
    ...over,
  };
}

describe("buildBoardBindings (UI spec §6, board scope)", () => {
  it("covers the full §6 board key map, all route-scoped", () => {
    const bindings = buildBoardBindings(actions());
    const chords = new Map(bindings.map((b) => [b.id, b.chord]));
    expect(chords).toEqual(
      new Map([
        ["board.create", "c"],
        ["board.next", "j"],
        ["board.prev", "k"],
        ["board.peek", "space"],
        ["board.open", "enter"],
        ["board.select", "x"],
        ["board.back", "escape"],
        ["board.status", "s"],
        ["board.priority", "p"],
        ["board.assign", "a"],
        ["board.delegate", "d"],
        ["board.labels", "l"],
        ["board.edit", "e"],
        ["board.rename", "r"],
        ["board.layout", "mod+b"],
        ["board.display", "shift+v"],
      ]),
    );
    expect(bindings.every((b) => b.scope === "route")).toBe(true);
  });

  describe("dispatch through the registry", () => {
    let reg: KeyboardRegistry;
    let a: BoardKeyActions;
    let deactivate: () => void;

    beforeEach(() => {
      reg = new KeyboardRegistry();
      a = actions();
      for (const b of buildBoardBindings(a)) reg.register(b);
      deactivate = reg.activateScope("route");
      return () => deactivate();
    });

    it("C creates, J/K move the selection, Enter opens, Space peeks, X selects", () => {
      reg.handleKeydown(keydown({ key: "c" }));
      expect(a.createTicket).toHaveBeenCalledTimes(1);

      reg.handleKeydown(keydown({ key: "j" }));
      expect(a.moveSelection).toHaveBeenLastCalledWith(1);
      reg.handleKeydown(keydown({ key: "k" }));
      expect(a.moveSelection).toHaveBeenLastCalledWith(-1);

      reg.handleKeydown(keydown({ key: "Enter" }));
      expect(a.openSelected).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: " " }));
      expect(a.peek).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "x" }));
      expect(a.toggleMultiSelect).toHaveBeenCalledTimes(1);
    });

    it("S/P/A/D/L open the right pickers", () => {
      const expected: Array<[string, string]> = [
        ["s", "status"],
        ["p", "priority"],
        ["a", "assign"],
        ["d", "delegate"],
        ["l", "label"],
      ];
      for (const [key, picker] of expected) {
        reg.handleKeydown(keydown({ key }));
        expect(a.openPicker).toHaveBeenLastCalledWith(picker);
      }
      expect(a.openPicker).toHaveBeenCalledTimes(expected.length);
    });

    it("⌘B toggles the layout and ⇧V the display menu; E edits, R renames", () => {
      reg.handleKeydown(keydown({ key: "b", metaKey: true }));
      expect(a.toggleLayout).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "V", shiftKey: true }));
      expect(a.toggleDisplayMenu).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "e" }));
      expect(a.edit).toHaveBeenCalledTimes(1);
      reg.handleKeydown(keydown({ key: "r" }));
      expect(a.rename).toHaveBeenCalledTimes(1);
    });

    it("mutation chords are gated on a selection; C and ⌘B are not", () => {
      const gated = actions({ hasSelection: () => false });
      const reg2 = new KeyboardRegistry();
      for (const b of buildBoardBindings(gated)) reg2.register(b);
      const off = reg2.activateScope("route");

      reg2.handleKeydown(keydown({ key: "s" }));
      reg2.handleKeydown(keydown({ key: "Enter" }));
      reg2.handleKeydown(keydown({ key: " " }));
      expect(gated.openPicker).not.toHaveBeenCalled();
      expect(gated.openSelected).not.toHaveBeenCalled();
      expect(gated.peek).not.toHaveBeenCalled();

      reg2.handleKeydown(keydown({ key: "c" }));
      expect(gated.createTicket).toHaveBeenCalledTimes(1);
      reg2.handleKeydown(keydown({ key: "b", metaKey: true }));
      expect(gated.toggleLayout).toHaveBeenCalledTimes(1);
      off();
    });

    it("nothing fires when the route scope is inactive (another screen)", () => {
      deactivate();
      reg.handleKeydown(keydown({ key: "c" }));
      expect(a.createTicket).not.toHaveBeenCalled();
    });
  });
});
