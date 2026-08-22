/*
 * Selection → sub-tickets (UI spec §5.4 step 4): the primary agent→human handoff. An agent
 * writes a plan as a checklist in the description; a human selects the lines and presses
 * ⌘⇧O; each non-empty line becomes one sub-ticket title. List markers and checkbox syntax
 * are stripped — "- [ ] Add tests" becomes the title "Add tests".
 */

/** Turn selected description text into sub-ticket titles, one per non-empty line. */
export function selectionToTitles(selection: string): string[] {
  return selection
    .split(/\r?\n/)
    .map(stripListMarker)
    .filter((l) => l !== "");
}

function stripListMarker(line: string): string {
  return line
    .trim()
    .replace(/^(?:[-*+]\s+)?\[[ xX]\]\s+/, "") // "- [ ] " / "[x] "
    .replace(/^[-*+]\s+/, "") // "- " / "* "
    .replace(/^\d+[.)]\s+/, "") // "1. " / "2) "
    .replace(/^#+\s+/, "") // headings
    .trim();
}
