/*
 * Run-list filter and saved-view state (S23, UI spec §5.7). Filters live in the URL
 * (interaction rule 12) as ?state=a,b&agent=&ticket=; ?view= names a saved view. With no
 * search at all the default is the built-in **Needs attention** view; ?view=all is the
 * explicit everything view (so "I removed every chip" survives a reload distinctly from
 * "I never filtered").
 *
 * Saved views persist in localStorage, per project. The schema has a `saved_views` table,
 * but no API for it exists yet and adding one is not S23's scope creep to take on — the
 * storage swaps to it without touching callers when that API lands (documented decision).
 */
import type { Run, RunState } from "../../../lib/api/client";

export const RUN_STATES: readonly RunState[] = [
  "queued",
  "provisioning",
  "running",
  "needs_input",
  "awaiting_approval",
  "completed",
  "failed",
  "timed_out",
  "canceled",
  "loop_stopped",
];

export interface RunFilters {
  states: RunState[];
  agent?: string;
  ticket?: string;
}

export interface SavedView {
  name: string;
  filters: RunFilters;
}

/** §5.7: the default view — `needs input` + `awaiting approval` + `failed` + `loop stopped`. */
export const NEEDS_ATTENTION: SavedView = {
  name: "Needs attention",
  filters: { states: ["needs_input", "awaiting_approval", "failed", "loop_stopped"] },
};

export const ALL_RUNS: SavedView = { name: "All runs", filters: { states: [] } };

export function emptyFilters(f: RunFilters): boolean {
  return f.states.length === 0 && f.agent === undefined && f.ticket === undefined;
}

export function sameFilters(a: RunFilters, b: RunFilters): boolean {
  return (
    a.states.length === b.states.length &&
    a.states.every((s) => b.states.includes(s)) &&
    a.agent === b.agent &&
    a.ticket === b.ticket
  );
}

/** Client-side application — a chip toggle filters the already-fetched list instantly. */
export function applyFilters(runs: Run[], f: RunFilters): Run[] {
  return runs.filter(
    (r) =>
      (f.states.length === 0 || f.states.includes(r.state)) &&
      (f.agent === undefined || r.agent_id === f.agent) &&
      (f.ticket === undefined || r.ticket_id === f.ticket),
  );
}

/** States that occupy a container or a queue slot — what "≥4 runs in flight" counts for the
 * verbosity default, and what the list's live dots subscribe to. */
export function inFlight(runs: Run[]): Run[] {
  return runs.filter(
    (r) =>
      r.state === "queued" ||
      r.state === "provisioning" ||
      r.state === "running" ||
      r.state === "needs_input" ||
      r.state === "awaiting_approval",
  );
}

// ---- saved views (localStorage until the saved_views API exists) ------------------------

const storageKey = (projectKey: string) => `lexicode-run-views:${projectKey}`;

export function loadSavedViews(projectKey: string): SavedView[] {
  try {
    const raw = localStorage.getItem(storageKey(projectKey));
    if (raw === null) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (v): v is SavedView =>
        typeof v === "object" &&
        v !== null &&
        typeof (v as SavedView).name === "string" &&
        Array.isArray((v as SavedView).filters?.states),
    );
  } catch {
    return [];
  }
}

export function saveSavedViews(projectKey: string, views: SavedView[]): void {
  try {
    localStorage.setItem(storageKey(projectKey), JSON.stringify(views));
  } catch {
    // Storage full/blocked: the view lives for the session via URL state anyway.
  }
}

/** Resolve what the URL means: explicit filters win; then ?view=; then the default. */
export function resolveFilters(
  search: { state?: string; agent?: string; ticket?: string; view?: string },
  savedViews: SavedView[],
): { filters: RunFilters; viewName: string | null } {
  const states = (search.state ?? "")
    .split(",")
    .filter((s): s is RunState => (RUN_STATES as readonly string[]).includes(s));
  const explicit: RunFilters = {
    states,
    agent: search.agent,
    ticket: search.ticket,
  };
  if (!emptyFilters(explicit)) return { filters: explicit, viewName: null };
  if (search.view !== undefined) {
    if (search.view === "all") return { filters: ALL_RUNS.filters, viewName: ALL_RUNS.name };
    const named = savedViews.find((v) => v.name === search.view);
    if (named) return { filters: named.filters, viewName: named.name };
    if (search.view === NEEDS_ATTENTION.name) {
      return { filters: NEEDS_ATTENTION.filters, viewName: NEEDS_ATTENTION.name };
    }
  }
  return { filters: NEEDS_ATTENTION.filters, viewName: NEEDS_ATTENTION.name };
}

/** The URL search params for a filter set — chips write the URL, nothing else. */
export function filtersToSearch(f: RunFilters): {
  state?: string;
  agent?: string;
  ticket?: string;
  view?: string;
} {
  if (emptyFilters(f)) return { view: "all" };
  return {
    state: f.states.length > 0 ? f.states.join(",") : undefined,
    agent: f.agent,
    ticket: f.ticket,
    view: undefined,
  };
}
