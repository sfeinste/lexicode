/*
 * Shared fixtures for the S29 tests. The operator table mirrors the server's static
 * contracts §4.1 catalog (catalog.go); the "novel" source and action exist nowhere in the
 * codebase — they are what proves the editor is generated from the catalog alone.
 */
import type { CatalogOperator, Trigger, TriggerCatalog } from "../../../lib/api/client";

export const OPERATORS: CatalogOperator[] = [
  { op: "text.is", family: "text", label: "is", value: "text", field: true },
  { op: "text.is_not", family: "text", label: "is not", value: "text", field: true },
  { op: "text.contains", family: "text", label: "contains", value: "text", field: true },
  { op: "text.not_contains", family: "text", label: "does not contain", value: "text", field: true },
  { op: "text.starts_with", family: "text", label: "starts with", value: "text", field: true },
  { op: "text.matches_glob", family: "text", label: "matches glob", value: "text", field: true },
  { op: "text.is_empty", family: "text", label: "is empty", value: "none", field: true },
  { op: "number.eq", family: "number", label: "equals", value: "number", field: true },
  { op: "number.gt", family: "number", label: "greater than", value: "number", field: true },
  { op: "number.gte", family: "number", label: "at least", value: "number", field: true },
  { op: "number.lt", family: "number", label: "less than", value: "number", field: true },
  { op: "number.lte", family: "number", label: "at most", value: "number", field: true },
  { op: "enum.is", family: "enum", label: "is", value: "enum", field: true },
  { op: "enum.is_not", family: "enum", label: "is not", value: "enum", field: true },
  { op: "enum.in", family: "enum", label: "is one of", value: "enum_list", field: true },
  { op: "bool.is", family: "bool", label: "is", value: "bool", field: true },
  { op: "set.includes", family: "set", label: "includes", value: "text", field: true },
  { op: "set.excludes", family: "set", label: "does not include", value: "text", field: true },
  { op: "set.is_empty", family: "set", label: "is empty", value: "none", field: true },
  { op: "actor.is_agent", family: "actor", label: "is an agent", value: "none", field: false },
  { op: "actor.is_human", family: "actor", label: "is a human", value: "none", field: false },
  { op: "actor.is", family: "actor", label: "is", value: "text", field: false },
];

/** A github.poll-shaped catalog for the prose tests. */
export const GITHUB_CATALOG: TriggerCatalog = {
  sources: [
    {
      id: "github.poll",
      events: [
        {
          kind: "pull_request",
          label: "Pull request",
          activity_types: [
            { value: "opened", label: "opened" },
            {
              value: "synchronize",
              label: "pushed to",
              help: "New commits on the head branch — distinguishing this from opened is where the runaway loop lives.",
            },
            { value: "ready_for_review", label: "marked ready for review" },
          ],
          filters: [
            { key: "branches", kind: "glob-list", label: "Branches" },
            { key: "paths", kind: "glob-list", label: "Paths" },
            { key: "labels", kind: "label-list", label: "Labels" },
          ],
          fields: [
            { path: "pr.title", type: "text" },
            { path: "pr.files_changed", type: "number" },
            { path: "pr.state", type: "enum", enum: ["open", "closed"] },
            { path: "pr.draft", type: "bool" },
            { path: "pr.labels", type: "set" },
          ],
          subject_key: "pr:{{pr.number}}",
        },
      ],
    },
  ],
  actions: [
    {
      id: "run_agent",
      label: "Run an agent",
      schema: {
        fields: [
          { key: "agent_id", label: "Agent", type: "agent", required: true },
          { key: "prompt_override", label: "Prompt override", type: "template", required: false },
        ],
      },
    },
  ],
  operators: OPERATORS,
};

/**
 * A catalog whose event kind and action exist NOWHERE in the codebase. If the editor
 * renders this, it renders anything a future EventSource or TriggerAction registers —
 * the S32 cron source proof rides on exactly this property.
 */
export const NOVEL_CATALOG: TriggerCatalog = {
  sources: [
    {
      id: "weather.station",
      events: [
        {
          kind: "storm_warning",
          label: "Storm warning",
          activity_types: [
            { value: "issued", label: "issued" },
            {
              value: "escalated",
              label: "escalated",
              help: "Escalations can re-fire on the same storm — this is where the loop lives.",
            },
          ],
          filters: [{ key: "regions", kind: "label-list", label: "Regions" }],
          fields: [
            { path: "storm.name", type: "text" },
            { path: "storm.wind_speed", type: "number" },
            { path: "storm.severity", type: "enum", enum: ["watch", "warning", "emergency"] },
          ],
          subject_key: "storm:{{storm.name}}",
        },
      ],
    },
  ],
  actions: [
    {
      id: "sound_alarm",
      label: "Sound the alarm",
      schema: {
        fields: [
          {
            key: "volume",
            label: "Volume",
            type: "enum",
            required: true,
            enum: ["quiet", "loud"],
          },
          { key: "message", label: "Message", type: "template", required: false },
        ],
      },
    },
  ],
  operators: OPERATORS,
};

/** A complete Trigger row; override what the test cares about. */
export function makeTrigger(overrides: Partial<Trigger> = {}): Trigger {
  return {
    id: "tr1",
    project_id: "p1",
    name: "Review new PRs",
    enabled: true,
    source_id: "github.poll",
    event: "pull_request",
    activity_types: ["opened", "ready_for_review"],
    filters: { branches: ["main"] },
    conditions: {
      all: [
        { op: "actor.is_agent" },
        { field: "pr.files_changed", op: "number.lt", value: 400 },
      ],
    },
    actions: [{ action_id: "run_agent", params: { agent_id: "a1" } }],
    loop_config: {
      actor_suppression: true,
      debounce_seconds: 90,
      cancel_in_progress: true,
      depth_limit: 3,
      daily_budget_cents: null,
    },
    cron: null,
    created_by: null,
    created_at: "2026-08-20T10:00:00Z",
    updated_at: "2026-08-20T10:00:00Z",
    health: { counts: {}, last_fired_at: null, recent: [] },
    action_summaries: ["run agent Reviewer"],
    ...overrides,
  };
}
