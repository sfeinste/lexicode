/*
 * The board's ONE data structure (S11): a filtered ticket list grouped by `group_by`.
 * The board layout renders these groups as columns, the list layout renders the same groups
 * as sections — group membership is computed here and nowhere else, which is what makes the
 * "identical membership in both layouts" acceptance criterion structural rather than lucky.
 *
 * Pure functions over the API read models; every rule is unit-tested in grouping.test.ts.
 */
import type { Column, Label, Ticket, TicketPriority } from "../../../lib/api/client";

export type GroupBy = "status" | "assignee" | "delegate" | "priority" | "label";

/** The sentinel group id for "the property is unset" (Unassigned, No delegate, No label). */
export const NONE_GROUP = "none";

/** Filter-chip state, mirroring the URL params. "none" filters for the unset value. */
export interface BoardFilters {
  assignee?: string;
  delegate?: string;
  label?: string;
  priority?: string;
  /** Client-side search over key and title, case-insensitive. */
  q?: string;
}

export interface BoardGroup {
  /** Stable id: column id / user id / agent id / priority value / label id / NONE_GROUP. */
  id: string;
  name: string;
  tickets: Ticket[];
  /** Status grouping only — carries WIP limit and the auto-start marker for the header. */
  column?: Column;
}

/** Priority display order (urgent first) — also the group order for group_by=priority. */
export const PRIORITY_ORDER: TicketPriority[] = ["urgent", "high", "medium", "low", "none"];

export const PRIORITY_LABELS: Record<TicketPriority, string> = {
  urgent: "Urgent",
  high: "High",
  medium: "Medium",
  low: "Low",
  none: "No priority",
};

function matchesFilter(value: string | null, filter: string): boolean {
  return filter === NONE_GROUP ? value === null : value === filter;
}

/** Apply the filter chips and the search box. Order-preserving. */
export function filterTickets(tickets: Ticket[], f: BoardFilters): Ticket[] {
  const q = f.q?.trim().toLowerCase();
  return tickets.filter((t) => {
    if (f.assignee !== undefined && !matchesFilter(t.assignee_id, f.assignee)) return false;
    if (f.delegate !== undefined && !matchesFilter(t.delegate_agent_id, f.delegate)) {
      return false;
    }
    if (f.priority !== undefined && t.priority !== f.priority) return false;
    if (f.label !== undefined) {
      if (f.label === NONE_GROUP) {
        if (t.label_ids.length > 0) return false;
      } else if (!t.label_ids.includes(f.label)) {
        return false;
      }
    }
    if (q !== undefined && q !== "") {
      const hay = `${t.key} ${t.title}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
}

export interface GroupContext {
  /** Board columns in position order (status grouping). */
  columns: Column[];
  /** Project labels (label grouping and chip names). */
  labels: Label[];
}

/**
 * Group tickets by the picked property.
 *
 * - `status`: one group per column (every column, even empty — they are drop targets),
 *   tickets in fractional-position order.
 * - `priority`: all five buckets in PRIORITY_ORDER, even empty — also drop targets.
 * - `assignee` / `delegate`: one group per value that occurs, sorted by name, with the
 *   unset group last and only when non-empty (an empty "Unassigned" can't be invented as a
 *   target for values we can't enumerate).
 * - `label`: one group per label that has tickets (a ticket with two labels appears in
 *   both groups), plus "No label" last when non-empty.
 */
export function groupTickets(
  tickets: Ticket[],
  groupBy: GroupBy,
  ctx: GroupContext,
): BoardGroup[] {
  switch (groupBy) {
    case "status": {
      const byColumn = new Map<string, Ticket[]>();
      for (const t of tickets) {
        const list = byColumn.get(t.column_id) ?? [];
        list.push(t);
        byColumn.set(t.column_id, list);
      }
      return [...ctx.columns]
        .sort((a, b) => a.position - b.position)
        .map((col) => ({
          id: col.id,
          name: col.name,
          column: col,
          tickets: (byColumn.get(col.id) ?? []).sort((a, b) => a.position - b.position),
        }));
    }
    case "priority":
      return PRIORITY_ORDER.map((p) => ({
        id: p,
        name: PRIORITY_LABELS[p],
        tickets: tickets.filter((t) => t.priority === p),
      }));
    case "assignee":
      return groupByValue(tickets, (t) => t.assignee_id, "Unassigned");
    case "delegate":
      return groupByValue(tickets, (t) => t.delegate_agent_id, "No delegate");
    case "label": {
      const byLabel = new Map<string, Ticket[]>();
      const unlabeled: Ticket[] = [];
      for (const t of tickets) {
        if (t.label_ids.length === 0) {
          unlabeled.push(t);
          continue;
        }
        for (const id of t.label_ids) {
          const list = byLabel.get(id) ?? [];
          list.push(t);
          byLabel.set(id, list);
        }
      }
      const groups: BoardGroup[] = [...ctx.labels]
        .filter((l) => byLabel.has(l.id))
        .sort((a, b) => a.name.localeCompare(b.name))
        .map((l) => ({ id: l.id, name: l.name, tickets: byLabel.get(l.id) ?? [] }));
      if (unlabeled.length > 0) {
        groups.push({ id: NONE_GROUP, name: "No label", tickets: unlabeled });
      }
      return groups;
    }
  }
}

function groupByValue(
  tickets: Ticket[],
  value: (t: Ticket) => string | null,
  noneName: string,
): BoardGroup[] {
  const byValue = new Map<string, Ticket[]>();
  const unset: Ticket[] = [];
  for (const t of tickets) {
    const v = value(t);
    if (v === null) {
      unset.push(t);
      continue;
    }
    const list = byValue.get(v) ?? [];
    list.push(t);
    byValue.set(v, list);
  }
  const groups: BoardGroup[] = [...byValue.keys()]
    .sort((a, b) => a.localeCompare(b))
    .map((id) => ({ id, name: id, tickets: byValue.get(id) ?? [] }));
  if (unset.length > 0) groups.push({ id: NONE_GROUP, name: noneName, tickets: unset });
  return groups;
}

/**
 * The flattened J/K traversal order: groups in order, tickets within each group. For
 * group_by=label a ticket may occur twice; duplicates after the first are dropped so J/K
 * never sticks.
 */
export function flattenGroups(groups: BoardGroup[]): Ticket[] {
  const seen = new Set<string>();
  const out: Ticket[] = [];
  for (const g of groups) {
    for (const t of g.tickets) {
      if (seen.has(t.id)) continue;
      seen.add(t.id);
      out.push(t);
    }
  }
  return out;
}

/**
 * The fractional position for dropping into `tickets` (the destination group's tickets in
 * position order, WITH the dragged ticket already removed) at `index`. Empty group → 1;
 * top → before the first; end → after the last; between → the midpoint. Positions are
 * opaque fractional numbers (data model) — the server accepts an explicit `position`.
 */
export function dropPosition(tickets: Ticket[], index: number): number {
  if (tickets.length === 0) return 1;
  if (index <= 0) return tickets[0].position - 1;
  if (index >= tickets.length) return tickets[tickets.length - 1].position + 1;
  return (tickets[index - 1].position + tickets[index].position) / 2;
}
