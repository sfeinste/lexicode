/*
 * verified_until helpers (S33). The page header renders `verified until 2026-11-01` and
 * turns red once the date is past — a purely client-side check against the viewer's clock,
 * deliberately independent of the S34 demotion job: staleness must be visible before any
 * server-side enforcement runs.
 */

/** Whether a `YYYY-MM-DD` verified_until date is past. The named day itself still counts as
 * verified — "verified until 2026-11-01" holds through that day and turns red after it. */
export function isPastDue(verifiedUntil: string, today: Date = new Date()): boolean {
  const y = today.getFullYear();
  const m = String(today.getMonth() + 1).padStart(2, "0");
  const d = String(today.getDate()).padStart(2, "0");
  return verifiedUntil < `${y}-${m}-${d}`;
}

/** The header chip's exact copy: `verified until 2026-11-01`. */
export function verifiedLabel(verifiedUntil: string): string {
  return `verified until ${verifiedUntil}`;
}
