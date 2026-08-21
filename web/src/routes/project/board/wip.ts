/*
 * WIP limit rendering (UI spec §5.3): a column with a limit shows `3/4` — amber AT the
 * limit, red OVER it. On the `running` column the limit is enforcing and the header grows
 * to `4/4 · queued: 2` — the queued count arrives with S22; the structure renders now.
 */

export type WipLevel = "under" | "at" | "over";

export interface WipDisplay {
  /** "3/4", or "4/4 · queued: 2" when runs are queued behind an enforcing limit. */
  label: string;
  level: WipLevel;
}

/**
 * The header's WIP fragment. Null when the column has no limit — no empty badge slots.
 * `queued` is the S22 queued-run count; 0 (the only value today) renders nothing extra.
 */
export function wipDisplay(count: number, limit: number | null, queued = 0): WipDisplay | null {
  if (limit === null) return null;
  let level: WipLevel;
  if (count < limit) level = "under";
  else if (count === limit) level = "at";
  else level = "over";
  const label = queued > 0 ? `${count}/${limit} · queued: ${queued}` : `${count}/${limit}`;
  return { label, level };
}
