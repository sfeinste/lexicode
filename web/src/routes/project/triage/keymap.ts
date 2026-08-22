/*
 * The triage queue's contribution to the keyboard map (UI spec §6, triage scope) as DATA
 * for the S07 registry — the cheatsheet and ⌘K palette read these. The §6 line is:
 *
 *   1 2 3 H   triage: accept · duplicate · decline · snooze
 *
 * plus the list grammar: J/K move, Space peek, Enter open, Esc back. The factory takes
 * callbacks so the bindings are testable without mounting the page (keymap.test.ts).
 */
import type { KeyBinding } from "../../../lib/keyboard/registry";

export interface TriageKeyActions {
  moveSelection: (delta: 1 | -1) => void;
  peek: () => void;
  openSelected: () => void;
  clearSelection: () => void;
  accept: () => void;
  duplicate: () => void;
  decline: () => void;
  snooze: () => void;
  /** Gate for chords that need a selected row. */
  hasSelection: () => boolean;
}

export function buildTriageBindings(a: TriageKeyActions): KeyBinding[] {
  const sel = a.hasSelection;
  return [
    {
      id: "triage.next",
      scope: "route",
      chord: "j",
      title: "Next item",
      group: "Triage",
      run: () => a.moveSelection(1),
    },
    {
      id: "triage.prev",
      scope: "route",
      chord: "k",
      title: "Previous item",
      group: "Triage",
      run: () => a.moveSelection(-1),
    },
    {
      id: "triage.peek",
      scope: "route",
      chord: "space",
      title: "Peek",
      group: "Triage",
      enabled: sel,
      run: a.peek,
    },
    {
      id: "triage.open",
      scope: "route",
      chord: "enter",
      title: "Open ticket",
      group: "Triage",
      enabled: sel,
      run: a.openSelected,
    },
    {
      id: "triage.back",
      scope: "route",
      chord: "escape",
      title: "Back",
      group: "Triage",
      run: a.clearSelection,
    },
    {
      id: "triage.accept",
      scope: "route",
      chord: "1",
      title: "Accept",
      group: "Triage",
      enabled: sel,
      run: a.accept,
    },
    {
      id: "triage.duplicate",
      scope: "route",
      chord: "2",
      title: "Mark duplicate",
      group: "Triage",
      enabled: sel,
      run: a.duplicate,
    },
    {
      id: "triage.decline",
      scope: "route",
      chord: "3",
      title: "Decline",
      group: "Triage",
      enabled: sel,
      run: a.decline,
    },
    {
      id: "triage.snooze",
      scope: "route",
      chord: "h",
      title: "Snooze",
      group: "Triage",
      enabled: sel,
      run: a.snooze,
    },
  ];
}
