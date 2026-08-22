/*
 * Draft ⇄ API mapping for the trigger editor: a stored Trigger loads into the TriggerDraft
 * the form renders, and the draft serializes back to the TriggerInput the create/patch
 * endpoints take. Kept out of the page component so the mapping is unit-testable.
 */
import type { Trigger, TriggerFilters, TriggerInput } from "../../../lib/api/client";
import { parseConditions, serializeConditions } from "./conditions";
import { DEFAULT_LOOP_CONFIG } from "./loopDefaults";
import type { DraftAction, TriggerDraft } from "./TriggerForm";

/**
 * The reserved filter key that carries the cron expression through the catalog-generated
 * WHEN section (the S32 cron source's `cron` FilterField). The schema stores the expression
 * in the trigger's dedicated `cron` column, not in the filters JSON — this mapping is the
 * one seam where the two meet: the form renders it like any other filter, the API sees it
 * as the top-level `cron` field.
 */
const CRON_FILTER_KEY = "cron";

export function emptyDraft(): TriggerDraft {
  return {
    name: "",
    source_id: "",
    event: "",
    activity_types: [],
    filters: {},
    groups: [[]],
    rawConditions: "",
    actions: [],
    loop_config: { ...DEFAULT_LOOP_CONFIG },
  };
}

export function draftFromTrigger(tr: Trigger): TriggerDraft {
  const groups = parseConditions(tr.conditions);
  const filters: Record<string, string[]> = {};
  const stored = tr.filters ?? {};
  for (const key of Object.keys(stored) as (keyof TriggerFilters)[]) {
    const values = stored[key];
    if (Array.isArray(values) && values.length > 0) filters[key] = values;
  }
  if (typeof tr.cron === "string" && tr.cron !== "") filters[CRON_FILTER_KEY] = [tr.cron];
  return {
    name: tr.name,
    source_id: tr.source_id,
    event: tr.event,
    activity_types: [...tr.activity_types],
    filters,
    groups,
    rawConditions: groups === null ? JSON.stringify(tr.conditions, null, 2) : "",
    actions: (tr.actions ?? []).map(
      (a): DraftAction => ({
        action_id: a.action_id,
        params: (a.params ?? {}) as Record<string, unknown>,
      }),
    ),
    loop_config: { ...DEFAULT_LOOP_CONFIG, ...(tr.loop_config ?? {}) },
  };
}

export function draftToInput(draft: TriggerDraft): TriggerInput {
  let conditions: unknown;
  if (draft.groups === null) {
    try {
      conditions = JSON.parse(draft.rawConditions);
    } catch {
      // Send the unparseable text so the server's validation names the problem.
      conditions = draft.rawConditions;
    }
  } else {
    conditions = serializeConditions(
      draft.groups.map((rows) => rows.filter((r) => r.op !== "")),
    );
  }
  const { [CRON_FILTER_KEY]: cronValues, ...filters } = draft.filters;
  return {
    name: draft.name,
    source_id: draft.source_id,
    event: draft.event,
    activity_types: draft.activity_types,
    filters,
    conditions,
    actions: draft.actions.map((a) => ({ action_id: a.action_id, params: a.params })),
    loop_config: draft.loop_config,
    // "" clears the stored expression; the server refuses a schedule trigger without one.
    cron: cronValues?.[0]?.trim() ?? "",
  };
}
