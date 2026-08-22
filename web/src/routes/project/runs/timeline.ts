/*
 * The step-timeline model (UI spec §5.7, architecture §13): pure functions from the
 * normalized activity stream to render rows. Everything here is client-side over data the
 * page already holds — the verbosity switch and the grouping never refetch, and the
 * grouping survives filtering because it runs AFTER the verbosity filter.
 */
import type { RunActivity } from "../../../lib/api/client";

// ---- verbosity --------------------------------------------------------------------------

export type Verbosity = "summary" | "normal" | "verbose";

/** activities.level: 0 summary · 1 normal · 2 verbose. A view shows its level and below. */
export const VERBOSITY_LEVEL: Record<Verbosity, number> = {
  summary: 0,
  normal: 1,
  verbose: 2,
};

export function filterByVerbosity(
  activities: RunActivity[],
  verbosity: Verbosity,
): RunActivity[] {
  const max = VERBOSITY_LEVEL[verbosity];
  return activities.filter((a) => a.level <= max);
}

// ---- grouping (ToolCallRow, §7) ---------------------------------------------------------

/** A single activity row in the timeline. `child` marks a member of an expanded group. */
export interface StepRow {
  kind: "step";
  activity: RunActivity;
  child: boolean;
}

/** One collapsed-or-expanded run of consecutive same-group_key tool calls. */
export interface GroupRow {
  kind: "group";
  /** The seq of the group's first member — the group's stable identity. */
  firstSeq: number;
  groupKey: string;
  label: string;
  count: number;
  expanded: boolean;
  /** Rollups (§5.7: cost on every step, rolled up to parents). */
  costCents: number;
  tokensIn: number;
  tokensOut: number;
  durationMs: number | null;
  /** false when any member failed — the row wears the failure. */
  ok: boolean;
  members: RunActivity[];
}

export type TimelineRow = StepRow | GroupRow;

/** Consecutive same-group_key runs of at least this many actions collapse (§5.7). */
export const GROUP_MIN = 2;

/** "mcp__lexicode__ask_human" → "lexicode: ask_human"; other tools render as themselves. */
export function toolDisplayName(tool: string): string {
  if (tool.startsWith("mcp__")) {
    const rest = tool.slice("mcp__".length);
    const i = rest.indexOf("__");
    if (i > 0) return `${rest.slice(0, i)}: ${rest.slice(i + 2)}`;
  }
  return tool;
}

/** The collapsed row's one-line label: `Read 23 files ▸` for reads, `Edit ×6` style else. */
export function groupLabel(toolName: string, count: number): string {
  switch (toolName) {
    case "Read":
      return `Read ${count} files`;
    case "Grep":
    case "Glob":
      return `Search ×${count}`;
    case "Bash":
      return `Ran ${count} commands`;
    default:
      return `${toolDisplayName(toolName)} ×${count}`;
  }
}

/** Only plain tool actions group; thoughts, elicitations, errors, provisioning rows and
 * responses always render individually. */
function groupable(a: RunActivity): boolean {
  return a.type === "action" && a.group_key !== "";
}

export interface BuildOptions {
  verbosity: Verbosity;
  /** Group firstSeqs the user explicitly toggled open. */
  expanded: ReadonlySet<number>;
  /** The ?step= selection; its containing group renders expanded so the row is visible. */
  selectedSeq?: number;
}

/**
 * The whole §5.7 left pane in one pure pass: verbosity filter, then consecutive same-
 * group_key grouping with rollups. A group auto-expands when a member failed (failed steps
 * auto-expand, no click) or contains the URL-selected step.
 */
export function buildTimeline(activities: RunActivity[], opts: BuildOptions): TimelineRow[] {
  const visible = filterByVerbosity(activities, opts.verbosity);
  const rows: TimelineRow[] = [];

  let i = 0;
  while (i < visible.length) {
    const a = visible[i];
    if (!groupable(a)) {
      rows.push({ kind: "step", activity: a, child: false });
      i++;
      continue;
    }
    let j = i;
    while (j < visible.length && groupable(visible[j]) && visible[j].group_key === a.group_key) {
      j++;
    }
    const members = visible.slice(i, j);
    if (members.length < GROUP_MIN) {
      for (const m of members) rows.push({ kind: "step", activity: m, child: false });
      i = j;
      continue;
    }
    const anyFailed = members.some((m) => m.ok === false);
    const holdsSelection =
      opts.selectedSeq !== undefined && members.some((m) => m.seq === opts.selectedSeq);
    const expanded = opts.expanded.has(members[0].seq) || anyFailed || holdsSelection;
    const durations = members.map((m) => m.duration_ms).filter((d): d is number => d !== null);
    rows.push({
      kind: "group",
      firstSeq: members[0].seq,
      groupKey: a.group_key,
      label: groupLabel(a.tool_name, members.length),
      count: members.length,
      expanded,
      costCents: members.reduce((s, m) => s + m.cost_cents, 0),
      tokensIn: members.reduce((s, m) => s + m.tokens_in, 0),
      tokensOut: members.reduce((s, m) => s + m.tokens_out, 0),
      durationMs: durations.length > 0 ? durations.reduce((s, d) => s + d, 0) : null,
      ok: !anyFailed,
      members,
    });
    if (expanded) {
      for (const m of members) rows.push({ kind: "step", activity: m, child: true });
    }
    i = j;
  }
  return rows;
}

// ---- failures (`f` jumps to the next one) -----------------------------------------------

/** Seqs of every failed step, in order. Errors carry ok=false from the adapter. */
export function failureSeqs(activities: RunActivity[]): number[] {
  return activities.filter((a) => a.ok === false).map((a) => a.seq);
}

/** The next failure after `fromSeq` (wrapping), or null when the run has none. */
export function nextFailure(activities: RunActivity[], fromSeq: number | undefined): number | null {
  const seqs = failureSeqs(activities);
  if (seqs.length === 0) return null;
  if (fromSeq === undefined) return seqs[0];
  return seqs.find((s) => s > fromSeq) ?? seqs[0];
}

/** What the timeline selects on load when the URL carries no ?step=: the first failed step
 * (auto-expanded without a click), else the last visible activity. */
export function defaultSelection(activities: RunActivity[]): number | undefined {
  const failed = failureSeqs(activities);
  if (failed.length > 0) return failed[0];
  return activities.length > 0 ? activities[activities.length - 1].seq : undefined;
}

// ---- timing gutter (§5.7) ---------------------------------------------------------------

export interface TimingSplit {
  queuedMs: number;
  modelMs: number;
  toolMs: number;
  /** duration_ms when present, else the sum of the known segments. */
  totalMs: number;
}

/** The queued / model / tool split for one step's duration bar. Null when the step carries
 * no timing at all (e.g. a provisioning checklist row). */
export function timingSplit(a: RunActivity): TimingSplit | null {
  const queued = a.queued_ms ?? 0;
  const model = a.model_ms ?? 0;
  const tool = a.tool_ms ?? 0;
  const known = queued + model + tool;
  const total = a.duration_ms ?? (known > 0 ? known : null);
  if (total === null) return null;
  return { queuedMs: queued, modelMs: model, toolMs: tool, totalMs: total };
}
