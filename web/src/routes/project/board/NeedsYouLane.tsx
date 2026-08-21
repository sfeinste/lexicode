/*
 * The pinned "Needs you" lane above the columns (UI spec §5.3): full-width, amber left
 * border, cannot be collapsed away when non-empty, cannot be reordered — it is not a
 * column, it is an interruption surface. Auto-populated from tickets whose run is in a
 * needs-you state; each card states the §4.3 flavor IN WORDS (rule 1) with one inline
 * action.
 *
 * The query behind it (useNeedsYouRuns) targets the contracts §5 runs view that S21/S22
 * implement; until then it answers 404 and the lane renders empty — which per spec means it
 * renders nothing at all (no empty shell to collapse).
 */
import { Link } from "@tanstack/react-router";

import { NEEDS_YOU_FLAVORS, type NeedsYouFlavor } from "./TicketCard";
import type { NeedsYouRun } from "./boardData";
import styles from "./board.module.css";

const FLAVOR_ACTIONS: Record<NeedsYouFlavor, string> = {
  question: "Answer",
  approval: "Approve",
  review: "View diff",
  failure: "View run",
};

export function NeedsYouLane({
  projectKey,
  runs,
}: {
  projectKey: string;
  runs: NeedsYouRun[];
}) {
  if (runs.length === 0) return null;
  return (
    <section className={styles.needsYouLane} aria-label={`Needs you (${runs.length})`}>
      <h2 className={styles.needsYouTitle}>Needs you ({runs.length})</h2>
      <div className={styles.needsYouCards}>
        {runs.map((r) => (
          <div key={r.id} className={styles.needsYouCard}>
            <span className={styles.cardKey}>{r.ticket_key ?? r.agent}</span>
            <span className={styles.needsYouCardTitle}>{r.ticket_title ?? ""}</span>
            <span className={styles.needsYouFlavor}>▲ {NEEDS_YOU_FLAVORS[r.flavor]}</span>
            <Link
              to="/p/$key/runs/$id"
              params={{ key: projectKey, id: r.id }}
              className={styles.needsYouAction}
            >
              {FLAVOR_ACTIONS[r.flavor]}
            </Link>
          </div>
        ))}
      </div>
    </section>
  );
}
