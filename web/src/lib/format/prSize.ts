/*
 * Diff-size warning threshold logic (S37; brief §7's review-bottleneck row). The threshold
 * is the effective pr_size_warning_lines setting — a workspace default with a per-project
 * override — measured in total changed lines (additions + deletions). 0 (or negative)
 * disables the warning; unknown sizes (the poller has not detail-read the PR yet) never warn.
 */

export function isLargeDiff(
  additions: number | null | undefined,
  deletions: number | null | undefined,
  thresholdLines: number,
): boolean {
  if (thresholdLines <= 0) return false;
  if (additions == null || deletions == null) return false;
  return additions + deletions > thresholdLines;
}

/** "+1,240 −310" — the compact GitHub-style size stat. */
export function formatDiffStat(additions: number, deletions: number): string {
  const fmt = new Intl.NumberFormat("en-US");
  return `+${fmt.format(additions)} −${fmt.format(deletions)}`;
}
