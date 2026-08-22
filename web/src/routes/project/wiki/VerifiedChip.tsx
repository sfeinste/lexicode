/*
 * VerifiedChip — the page header's `verified until 2026-11-01` chip (UI spec §5.6), turning
 * red once the date is past. Client-side date check only: staleness shows before the S34
 * demotion job ever runs.
 */
import styles from "./wiki.module.css";
import { isPastDue, verifiedLabel } from "./verified";

export function VerifiedChip({
  verifiedUntil,
  today,
}: {
  verifiedUntil: string;
  /** Test seam; defaults to the viewer's clock. */
  today?: Date;
}) {
  const past = isPastDue(verifiedUntil, today);
  return (
    <span className={styles.verified} data-past-due={past || undefined}>
      {verifiedLabel(verifiedUntil)}
    </span>
  );
}
