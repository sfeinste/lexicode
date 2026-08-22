/*
 * System-row sentences for the unified stream (S12). Every non-comment, non-run stream
 * entry renders as one compact line; the payload's `event` verb (written by the S10/S12
 * service) maps to a human sentence here and nowhere else. Unknown verbs fall back to the
 * verb itself so a new backend event is visible before this file learns its copy.
 */
import type { TicketStreamEntry } from "../../../lib/api/client";

export interface StreamContext {
  columnName: (id: string) => string;
  userName: (id: string) => string;
  labelName: (id: string) => string;
}

interface KnownPayload {
  event?: string;
  from_column_id?: string;
  to_column_id?: string;
  column_id?: string;
  assignee_id?: string;
  delegate_agent_id?: string;
  parent_id?: string;
  fields?: string[];
  text?: string;
  name?: string;
  keys?: string[];
  count?: number;
  active_runs_cancelled?: number;
}

export function systemLine(entry: TicketStreamEntry, ctx: StreamContext): string {
  const p = (entry.payload ?? {}) as KnownPayload;
  switch (p.event) {
    case "created":
      return `created this ticket in ${ctx.columnName(p.column_id ?? "")}`;
    case "moved":
      return `moved ${ctx.columnName(p.from_column_id ?? "")} → ${ctx.columnName(p.to_column_id ?? "")}`;
    case "updated":
      return `edited ${(p.fields ?? []).join(", ")}`;
    case "assigned":
      return `assigned to ${ctx.userName(p.assignee_id ?? "")}`;
    case "unassigned":
      return "unassigned";
    case "delegated":
      return "delegated to an agent";
    case "undelegated":
      return "removed the delegate";
    case "reparented":
      return "attached to a parent ticket";
    case "detached_from_parent":
      return "detached from its parent";
    case "subtickets_added":
      return `created ${p.count ?? (p.keys ?? []).length} sub-tickets: ${(p.keys ?? []).join(", ")}`;
    case "criterion_added":
      return `added a criterion: "${p.text ?? ""}"`;
    case "criterion_checked":
      return `checked "${p.text ?? ""}"`;
    case "criterion_unchecked":
      return `unchecked "${p.text ?? ""}"`;
    case "criterion_updated":
      return "edited a criterion";
    case "criterion_removed":
      return `removed a criterion: "${p.text ?? ""}"`;
    case "label_added":
      return `added label ${p.name ?? ctx.labelName("")}`;
    case "label_removed":
      return `removed label ${p.name ?? ""}`;
    case "archived":
      return "archived this ticket";
    case "unarchived":
      return "restored this ticket";
    default:
      return p.event ?? entry.kind;
  }
}
