/*
 * Pure tree mechanics for the wiki (S33): build the two-level tree from the flat list, and
 * compute the fractional position a drag writes — kept free of React so behavior is
 * testable as functions.
 */
import type { WikiPage } from "../../../lib/api/client";

export interface WikiTreeNode {
  page: WikiPage;
  children: WikiPage[];
}

/** Build the two-level tree: roots by position, children by position under each. A child
 * whose parent is missing from the list (archived defensively) is lifted to the root. */
export function buildTree(pages: WikiPage[]): WikiTreeNode[] {
  const byPos = (a: WikiPage, b: WikiPage) => a.position - b.position || (a.id < b.id ? -1 : 1);
  const ids = new Set(pages.map((p) => p.id));
  const roots = pages
    .filter((p) => p.parent_id === null || !ids.has(p.parent_id))
    .sort(byPos);
  return roots.map((root) => ({
    page: root,
    children: pages.filter((p) => p.parent_id === root.id).sort(byPos),
  }));
}

/** The fractional position for dropping at `index` within an ordered sibling list —
 * midpoint between neighbors, one beyond at either end (same math as the board's). */
export function dropPosition(siblings: WikiPage[], index: number): number {
  if (siblings.length === 0) return 1;
  if (index <= 0) return siblings[0].position - 1;
  if (index >= siblings.length) return siblings[siblings.length - 1].position + 1;
  return (siblings[index - 1].position + siblings[index].position) / 2;
}

/** All tags in a page list with their page counts, alphabetical, case-preserving on first
 * appearance (the tag index derives client-side from the one tree payload). */
export function tagIndex(pages: WikiPage[]): Array<{ tag: string; count: number }> {
  const counts = new Map<string, { tag: string; count: number }>();
  for (const p of pages) {
    for (const t of p.tags) {
      const key = t.toLowerCase();
      const cur = counts.get(key);
      if (cur) cur.count += 1;
      else counts.set(key, { tag: t, count: 1 });
    }
  }
  return [...counts.values()].sort((a, b) => a.tag.localeCompare(b.tag));
}

/** Pages carrying a tag (case-insensitive), tree order preserved. */
export function pagesWithTag(pages: WikiPage[], tag: string): WikiPage[] {
  const key = tag.toLowerCase();
  return pages.filter((p) => p.tags.some((t) => t.toLowerCase() === key));
}
