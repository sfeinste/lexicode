/*
 * The palette's fuzzy filter: case-insensitive subsequence match with a small score.
 * Contiguous matches and word-boundary hits score higher; null means no match.
 */
export function fuzzyScore(query: string, text: string): number | null {
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  if (q.length === 0) return 0;
  let score = 0;
  let ti = 0;
  let lastHit = -2;
  for (const ch of q) {
    if (ch === " ") continue;
    const idx = t.indexOf(ch, ti);
    if (idx === -1) return null;
    score += 1;
    if (idx === lastHit + 1) score += 2; // contiguous run
    if (idx === 0 || t[idx - 1] === " ") score += 3; // word boundary
    lastHit = idx;
    ti = idx + 1;
  }
  // Shorter targets rank higher on equal hits.
  return score - t.length / 100;
}

export function fuzzyFilter<T>(query: string, items: T[], text: (item: T) => string): T[] {
  return items
    .map((item) => ({ item, score: fuzzyScore(query, text(item)) }))
    .filter((r): r is { item: T; score: number } => r.score !== null)
    .sort((a, b) => b.score - a.score)
    .map((r) => r.item);
}
