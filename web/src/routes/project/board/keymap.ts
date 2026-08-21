/*
 * The board's contribution to the keyboard map (UI spec §6, board scope) as DATA for the
 * S07 registry — the cheatsheet and ⌘K palette read these. Single letters are mutation,
 * per the §6 grammar; ⌘B and ⇧V are the two view chords.
 *
 * The factory takes callbacks so the bindings are testable without mounting the page:
 * keymap.test.ts asserts every §6 board chord exists, is route-scoped, and drives the right
 * action.
 */
import type { KeyBinding } from "../../../lib/keyboard/registry";

export type BoardPickerKind = "status" | "priority" | "assign" | "delegate" | "label";

export interface BoardKeyActions {
  createTicket: () => void;
  moveSelection: (delta: 1 | -1) => void;
  peek: () => void;
  openSelected: () => void;
  toggleMultiSelect: () => void;
  clearSelection: () => void;
  openPicker: (kind: BoardPickerKind) => void;
  edit: () => void;
  rename: () => void;
  toggleLayout: () => void;
  toggleDisplayMenu: () => void;
  /** Gate for chords that need a selected ticket. */
  hasSelection: () => boolean;
}

export function buildBoardBindings(a: BoardKeyActions): KeyBinding[] {
  const sel = a.hasSelection;
  return [
    {
      id: "board.create",
      scope: "route",
      chord: "c",
      title: "New ticket",
      group: "Board",
      palette: true,
      run: a.createTicket,
    },
    {
      id: "board.next",
      scope: "route",
      chord: "j",
      title: "Next ticket",
      group: "Board",
      run: () => a.moveSelection(1),
    },
    {
      id: "board.prev",
      scope: "route",
      chord: "k",
      title: "Previous ticket",
      group: "Board",
      run: () => a.moveSelection(-1),
    },
    {
      id: "board.peek",
      scope: "route",
      chord: "space",
      title: "Peek",
      group: "Board",
      enabled: sel,
      run: a.peek,
    },
    {
      id: "board.open",
      scope: "route",
      chord: "enter",
      title: "Open ticket",
      group: "Board",
      enabled: sel,
      run: a.openSelected,
    },
    {
      id: "board.select",
      scope: "route",
      chord: "x",
      title: "Select",
      group: "Board",
      enabled: sel,
      run: a.toggleMultiSelect,
    },
    {
      id: "board.back",
      scope: "route",
      chord: "escape",
      title: "Back",
      group: "Board",
      run: a.clearSelection,
    },
    {
      id: "board.status",
      scope: "route",
      chord: "s",
      title: "Status",
      group: "Board",
      enabled: sel,
      run: () => a.openPicker("status"),
    },
    {
      id: "board.priority",
      scope: "route",
      chord: "p",
      title: "Priority",
      group: "Board",
      enabled: sel,
      run: () => a.openPicker("priority"),
    },
    {
      id: "board.assign",
      scope: "route",
      chord: "a",
      title: "Assign (human)",
      group: "Board",
      enabled: sel,
      run: () => a.openPicker("assign"),
    },
    {
      id: "board.delegate",
      scope: "route",
      chord: "d",
      title: "Delegate (agent)",
      group: "Board",
      enabled: sel,
      run: () => a.openPicker("delegate"),
    },
    {
      id: "board.labels",
      scope: "route",
      chord: "l",
      title: "Labels",
      group: "Board",
      enabled: sel,
      run: () => a.openPicker("label"),
    },
    {
      id: "board.edit",
      scope: "route",
      chord: "e",
      title: "Edit",
      group: "Board",
      enabled: sel,
      run: a.edit,
    },
    {
      id: "board.rename",
      scope: "route",
      chord: "r",
      title: "Rename",
      group: "Board",
      enabled: sel,
      run: a.rename,
    },
    {
      id: "board.layout",
      scope: "route",
      chord: "mod+b",
      title: "Board / list toggle",
      group: "Board",
      palette: true,
      run: a.toggleLayout,
    },
    {
      id: "board.display",
      scope: "route",
      chord: "shift+v",
      title: "Display options",
      group: "Board",
      palette: true,
      run: a.toggleDisplayMenu,
    },
  ];
}
