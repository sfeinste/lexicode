/*
 * Formatters for machine-generated values (architecture §13). Timestamps arrive as RFC3339
 * UTC text (architecture §14) and relative rendering is client-side only — these are the one
 * place that rendering happens.
 */

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/**
 * "just now" · "45s ago" · "2m ago" · "3h ago" · "5d ago" · "2026-06-01" for anything older
 * than 30 days. Matches the density of "⟳ 2m ago" in the spec's chrome (§2.1).
 */
export function formatRelativeTime(iso: string, now: Date = new Date()): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return iso;
  const delta = now.getTime() - then;
  if (delta < 10_000) return "just now";
  if (delta < MINUTE) return `${Math.floor(delta / 1000)}s ago`;
  if (delta < HOUR) return `${Math.floor(delta / MINUTE)}m ago`;
  if (delta < DAY) return `${Math.floor(delta / HOUR)}h ago`;
  if (delta < 30 * DAY) return `${Math.floor(delta / DAY)}d ago`;
  return iso.slice(0, 10);
}

/**
 * Durations for run steps and timelines: "4s" · "31s" · "1m 12s" · "2h 3m". Sub-second
 * durations render as "0.4s" — a step that took 400ms is not "0s".
 */
export function formatDuration(ms: number): string {
  if (ms < 0 || Number.isNaN(ms)) return "—";
  if (ms < 1000) return `${(ms / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) {
    const rs = s % 60;
    return rs === 0 ? `${m}m` : `${m}m ${rs}s`;
  }
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm === 0 ? `${h}h` : `${h}h ${rm}m`;
}

/**
 * Token counts the way the spec renders them (§5.7: "84.2k", "71k", "13k"): exact below
 * 1000, one decimal "k" below a million, one decimal "M" above. Token counts are exact
 * numbers (D-5) — only the rendering is compact.
 */
export function formatTokenCount(n: number): string {
  if (Number.isNaN(n) || n < 0) return "—";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${trimZero((n / 1000).toFixed(1))}k`;
  return `${trimZero((n / 1_000_000).toFixed(1))}M`;
}

/**
 * Dollar amounts: "$1.42", "$0.03", "$12.50". CostChip owns the D-5 estimate affordance;
 * this renders only the number.
 */
export function formatUSD(amount: number): string {
  if (Number.isNaN(amount) || amount < 0) return "—";
  return `$${amount.toFixed(2)}`;
}

function trimZero(s: string): string {
  return s.replace(/\.0$/, "");
}
