/*
 * Inbox — `/inbox` (UI spec §5.10): the cross-project needs-you list, one row per blocked
 * run, updated in place. S28 lands the rows; S07 ships the surface and its empty state.
 */
import { EmptyState } from "../../components/EmptyState/EmptyState";

export function InboxPage() {
  return (
    <EmptyState
      headline="Inbox zero"
      body="Everything awaiting a human, across all projects, lands here. Nothing is waiting right now."
    />
  );
}
