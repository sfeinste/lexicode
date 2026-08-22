/*
 * The rule card's prose (UI spec §5.9): a trigger renders as three readable lines —
 *
 *   WHEN pull request opened, pushed to · branch main
 *   IF   author is an agent · files changed < 400
 *   THEN run agent Reviewer
 *
 * WHEN and IF are composed here, client-side, from the same catalog the editor is
 * generated from — so the card and the editor can never disagree about vocabulary. THEN
 * uses the server's per-action Describe() sentences (`action_summaries`), because only the
 * server can resolve an agent_id to its current name. The split is deliberate; see the S29
 * notes in triggersApi.
 */
import type {
  CatalogOperator,
  FiringOutcome,
  Trigger,
  TriggerCatalog,
  TriggerFilters,
} from "../../../lib/api/client";
import { parseConditions, type ConditionRow } from "./conditions";

/** "pr.files_changed" → "files changed". */
export function humanizeField(path: string): string {
  const last = path.split(".").pop() ?? path;
  return last.replace(/_/g, " ");
}

const FILTER_NOUNS: Record<string, string> = {
  branches: "branch",
  paths: "path",
  labels: "label",
};

/** The WHEN line: event label + activity-type labels + filters, all from the catalog. */
export function composeWhen(trigger: Trigger, catalog: TriggerCatalog): string {
  const source = catalog.sources.find((s) => s.id === trigger.source_id);
  const event = source?.events.find((e) => e.kind === trigger.event);
  const eventLabel = (event?.label ?? trigger.event.replace(/_/g, " ")).toLowerCase();

  const activities = trigger.activity_types.map((v) => {
    const at = event?.activity_types.find((a) => a.value === v);
    return at?.label ?? v;
  });

  const parts: string[] = [];
  parts.push(activities.length > 0 ? `${eventLabel} ${activities.join(", ")}` : eventLabel);

  const filters = trigger.filters ?? {};
  for (const key of Object.keys(filters) as (keyof TriggerFilters)[]) {
    const values = filters[key];
    if (!Array.isArray(values) || values.length === 0) continue;
    parts.push(`${FILTER_NOUNS[key] ?? key} ${values.join(", ")}`);
  }
  return parts.join(" · ");
}

const NUMBER_SYMBOLS: Record<string, string> = {
  "number.eq": "=",
  "number.gt": ">",
  "number.gte": "≥",
  "number.lt": "<",
  "number.lte": "≤",
};

function valueWords(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (Array.isArray(value)) return value.map((v) => String(v)).join(", ");
  return String(value);
}

/** One row in words: "author is an agent" / "files changed < 400" / "title contains WIP". */
export function rowProse(row: ConditionRow, operators: CatalogOperator[]): string {
  const op = operators.find((o) => o.op === row.op);
  const label = op?.label ?? row.op;
  if (row.op.startsWith("actor.")) {
    const tail = op?.value === "none" ? label : `${label} ${valueWords(row.value)}`;
    return `author ${tail}`.trim();
  }
  const field = humanizeField(row.field);
  const symbol = NUMBER_SYMBOLS[row.op];
  if (symbol !== undefined) return `${field} ${symbol} ${valueWords(row.value)}`;
  if (op?.value === "none") return `${field} ${label}`;
  return `${field} ${label} ${valueWords(row.value)}`.trim();
}

/**
 * The IF line. Rows join with " · " (AND); groups join with " or ", parenthesized when
 * there is more than one. "" for an empty rule; a tree too deep for rows renders honestly
 * as "custom conditions".
 */
export function composeIf(conditions: unknown, catalog: TriggerCatalog): string {
  const groups = parseConditions(conditions);
  if (groups === null) return "custom conditions";
  const nonEmpty = groups.filter((rows) => rows.length > 0);
  if (nonEmpty.length === 0) return "";
  const rendered = nonEmpty.map((rows) =>
    rows.map((r) => rowProse(r, catalog.operators)).join(" · "),
  );
  if (rendered.length === 1) return rendered[0];
  return rendered.map((g) => `(${g})`).join(" or ");
}

/** The THEN line: the server's Describe() sentences, in action order. */
export function composeThen(trigger: Trigger): string {
  const summaries = trigger.action_summaries ?? [];
  if (summaries.length === 0) return "nothing configured yet";
  return summaries.join(", then ");
}

/** §4.2 outcome classes → the breakdown's compact words ("14 ok · 3 no action · 1 loop"). */
export const OUTCOME_WORDS: Record<FiringOutcome, string> = {
  succeeded: "ok",
  no_action: "no action",
  awaiting_approval: "awaiting approval",
  errored: "errored",
  debounced: "debounced",
  superseded: "superseded",
  loop_stopped: "loop",
  budget_exceeded: "over budget",
};

const OUTCOME_ORDER: FiringOutcome[] = [
  "succeeded",
  "no_action",
  "awaiting_approval",
  "errored",
  "debounced",
  "superseded",
  "loop_stopped",
  "budget_exceeded",
];

/** "14 ok · 3 no action · 1 loop" — never collapsed to success/failure. */
export function composeBreakdown(counts: Record<string, number>): string {
  const parts: string[] = [];
  for (const outcome of OUTCOME_ORDER) {
    const n = counts[outcome];
    if (n !== undefined && n > 0) parts.push(`${n} ${OUTCOME_WORDS[outcome]}`);
  }
  return parts.join(" · ");
}

/** Total firings over the health window. */
export function firedCount(counts: Record<string, number>): number {
  return Object.values(counts).reduce((a, b) => a + b, 0);
}

/**
 * The actor-suppression line. The identities are the agents this rule's own actions act
 * as — the ones layer 1 suppresses (kernel/guard layers.go).
 */
export function suppressionLine(trigger: Trigger, agentNames: string[]): string {
  const cfg = trigger.loop_config ?? {};
  if (cfg.actor_suppression === false) {
    return "Reacts to its own agents' events — actor suppression is off";
  }
  if (agentNames.length === 0) return "Ignores events caused by its own agents";
  return `Ignores events caused by ${agentNames.map((n) => `@${n}`).join(", ")}`;
}

/** The agent_id params referenced by the rule's actions, in order, deduplicated. */
export function actionAgentIds(trigger: Trigger): string[] {
  const ids: string[] = [];
  for (const ref of trigger.actions ?? []) {
    const params = ref.params as { agent_id?: unknown } | undefined;
    const id = params?.agent_id;
    if (typeof id === "string" && id !== "" && !ids.includes(id)) ids.push(id);
  }
  return ids;
}
