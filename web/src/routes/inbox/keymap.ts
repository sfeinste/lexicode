/*
 * The inbox's contribution to the keyboard map (UI spec §5.10 / §6) as DATA for the S07
 * registry — `J`/`K` walk the rows, `Enter` opens the selected row, `A` fires its primary
 * action (answer / approve inline, review a PR), `X` dismisses a failure. The cheatsheet
 * and the ⌘K palette read these like every other scope's bindings.
 *
 * The factory takes callbacks so the map is testable without mounting the page:
 * keymap.test.ts asserts every chord exists, is route-scoped, and drives the right action.
 */
import type { KeyBinding } from "../../lib/keyboard/registry";

export interface InboxKeyActions {
  moveSelection: (delta: 1 | -1) => void;
  /** Enter: follow the selected row's link (the run, the wiki page). */
  openSelected: () => void;
  /** A: the row's primary action — expand the inline answer/approve, or open the PR. */
  primaryAction: () => void;
  /** X: dismiss — acknowledges a failure row; a no-op on rows that cannot be dismissed. */
  dismissSelected: () => void;
  /** Gate for chords that need a selected row. */
  hasSelection: () => boolean;
}

export function buildInboxBindings(a: InboxKeyActions): KeyBinding[] {
  const sel = a.hasSelection;
  return [
    {
      id: "inbox.next",
      scope: "route",
      chord: "j",
      title: "Next row",
      group: "Inbox",
      run: () => a.moveSelection(1),
    },
    {
      id: "inbox.prev",
      scope: "route",
      chord: "k",
      title: "Previous row",
      group: "Inbox",
      run: () => a.moveSelection(-1),
    },
    {
      id: "inbox.open",
      scope: "route",
      chord: "enter",
      title: "Open row",
      group: "Inbox",
      enabled: sel,
      run: a.openSelected,
    },
    {
      id: "inbox.act",
      scope: "route",
      chord: "a",
      title: "Answer / approve",
      group: "Inbox",
      enabled: sel,
      run: a.primaryAction,
    },
    {
      id: "inbox.dismiss",
      scope: "route",
      chord: "x",
      title: "Dismiss",
      group: "Inbox",
      enabled: sel,
      run: a.dismissSelected,
    },
  ];
}
