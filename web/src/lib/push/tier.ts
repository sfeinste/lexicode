/*
 * The notification delivery tiers (architecture §12; S36), as pure functions the hook in
 * usePush.ts drives and tests exercise directly.
 *
 * Tiering: `needs input` / `awaiting approval` / `failed` push — a browser Notification —
 * while `completed` (which the backend rewrites in place as a `review` row: "finished —
 * review the output") only updates the badge, silently. The backend carries the flavor;
 * this is the one place the flavor becomes a delivery tier.
 */
import type { Notification as NotificationRow } from "../api/client";

export type PushTier = "push" | "badge";

/** question / approval / failure push; review (completed, PRs, proposals) is badge-only. */
export function pushTier(flavor: string): PushTier {
  switch (flavor) {
    case "question":
    case "approval":
    case "failure":
      return "push";
    default:
      return "badge";
  }
}

/**
 * Which rows of a freshly fetched notification list deserve a browser push. `prev` is the
 * id → updated_at snapshot from the last pass; `null` means this is the first data after
 * page load, and NOTHING pushes — permission is requested at the first occurrence of a
 * push-tier notification, never on load (architecture §12), and rows that pre-date the tab
 * were already seen elsewhere.
 *
 * A row pushes when it is unread, its flavor is push-tier, and its updated_at moved (the
 * in-place update rule means "the same row, new content" — a re-asked question, a run that
 * failed — must push again, while a mere refetch must not).
 */
export function freshPushes(
  prev: ReadonlyMap<string, string> | null,
  items: readonly NotificationRow[],
): NotificationRow[] {
  if (prev === null) return [];
  return items.filter(
    (n) =>
      n.state === "unread" &&
      pushTier(n.flavor) === "push" &&
      prev.get(n.id) !== n.updated_at,
  );
}
