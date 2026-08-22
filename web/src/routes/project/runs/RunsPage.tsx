/*
 * The run list (UI spec §5.7): status dot + label · agent · ticket · trigger-or-manual ·
 * duration · cost · started. Filters are client-side over the unfiltered list (instant
 * chips), live in the URL (rule 12), and the default view is **Needs attention**. Saved
 * views persist in localStorage until a saved_views API exists (see viewState.ts).
 *
 * Empty states are distinct (§8): never-had-any gets the "No runs yet → Go to board" card;
 * a filtered-empty gets "No runs match these filters" with the chips still removable.
 */
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import { CostChip } from "../../../components/CostChip/CostChip";
import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { StatusDot } from "../../../components/StatusDot/StatusDot";
import type { Run, RunState } from "../../../lib/api/client";
import { useAgentsQuery } from "../../../lib/api/agentQueries";
import { useRunsQuery } from "../../../lib/api/runQueries";
import { formatDuration, formatRelativeTime } from "../../../lib/format/format";
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { useTicketList } from "../ticket/ticketData";
import styles from "./runs.module.css";
import {
  ALL_RUNS,
  NEEDS_ATTENTION,
  RUN_STATES,
  applyFilters,
  emptyFilters,
  filtersToSearch,
  inFlight,
  loadSavedViews,
  resolveFilters,
  sameFilters,
  saveSavedViews,
  type RunFilters,
} from "./viewState";

const STATE_LABELS: Record<RunState, string> = {
  queued: "Queued",
  provisioning: "Provisioning",
  running: "Running",
  needs_input: "Needs input",
  awaiting_approval: "Awaiting approval",
  completed: "Completed",
  failed: "Failed",
  timed_out: "Timed out",
  canceled: "Canceled",
  loop_stopped: "Loop stopped",
};

function runDuration(r: Run, now: number): string {
  if (r.started_at === null) return "—";
  const start = new Date(r.started_at).getTime();
  const end = r.ended_at !== null ? new Date(r.ended_at).getTime() : now;
  return formatDuration(end - start);
}

export function RunsPage() {
  const { key } = useParams({ from: "/shell/p/$key/runs/" });
  const search = useSearch({ from: "/shell/p/$key/runs/" });
  const navigate = useNavigate({ from: "/p/$key/runs" });

  const runsQuery = useRunsQuery(key);
  const agents = useAgentsQuery(key);
  const tickets = useTicketList(key);

  const [savedViews, setSavedViews] = useState(() => loadSavedViews(key));
  const [savingName, setSavingName] = useState<string | null>(null);

  const runs = useMemo(() => runsQuery.data?.runs ?? [], [runsQuery.data]);
  const { filters, viewName } = resolveFilters(search, savedViews);
  const shown = useMemo(() => applyFilters(runs, filters), [runs, filters]);

  // Live dots: run.* frames ride topic run:<id>, so subscribe the in-flight runs (capped —
  // a huge backlog of active runs does not need a thousand topics for a list refresh; the
  // run.state handler invalidates the whole ["runs"] family anyway).
  const liveTopics = useMemo(
    () =>
      inFlight(runs)
        .slice(0, 30)
        .map((r) => `run:${r.id}`),
    [runs],
  );
  useStreamTopics(liveTopics);

  const agentName = (id: string) =>
    agents.data?.agents.find((a) => a.id === id)?.name ?? "agent";
  const ticketKey = (id: string | null) =>
    id === null ? null : (tickets.data?.tickets.find((t) => t.id === id)?.key ?? id);

  const setFilters = (f: RunFilters) => {
    void navigate({ search: filtersToSearch(f), replace: false });
  };
  const setView = (name: string) => {
    void navigate({ search: { view: name === NEEDS_ATTENTION.name ? undefined : name } });
  };

  const now = Date.now();
  const currentIsSaved =
    sameFilters(filters, NEEDS_ATTENTION.filters) ||
    emptyFilters(filters) ||
    savedViews.some((v) => sameFilters(v.filters, filters));

  const saveCurrent = (name: string) => {
    const trimmed = name.trim();
    if (trimmed === "") return;
    const next = [
      ...savedViews.filter((v) => v.name !== trimmed),
      { name: trimmed, filters },
    ];
    setSavedViews(next);
    saveSavedViews(key, next);
    setSavingName(null);
    setView(trimmed);
  };

  return (
    <div className={styles.page}>
      <div className={styles.pageTitle}>
        <h1>Runs</h1>
      </div>

      {/* Saved views: the built-ins, then this project's own. */}
      <div className={styles.viewsRow} role="tablist" aria-label="Saved views">
        {[NEEDS_ATTENTION, ALL_RUNS, ...savedViews].map((v) => (
          <button
            key={v.name}
            type="button"
            role="tab"
            aria-selected={viewName === v.name}
            className={styles.viewTab}
            data-active={viewName === v.name || undefined}
            onClick={() => setView(v.name === ALL_RUNS.name ? "all" : v.name)}
          >
            {v.name}
          </button>
        ))}
        {!currentIsSaved &&
          (savingName === null ? (
            <button
              type="button"
              className={styles.saveViewButton}
              onClick={() => setSavingName("")}
            >
              Save view…
            </button>
          ) : (
            <form
              className={styles.saveViewForm}
              onSubmit={(e) => {
                e.preventDefault();
                saveCurrent(savingName);
              }}
            >
              <input
                autoFocus
                className={styles.saveViewInput}
                placeholder="View name"
                value={savingName}
                onChange={(e) => setSavingName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") setSavingName(null);
                }}
              />
              <button type="submit" className={styles.saveViewButton}>
                Save
              </button>
            </form>
          ))}
      </div>

      {/* Filter chips — individually removable (§5.7) — plus the add-filter selects. */}
      <div className={styles.filterRow} aria-label="Filters">
        {filters.states.map((s) => (
          <button
            key={s}
            type="button"
            className={styles.chip}
            onClick={() =>
              setFilters({ ...filters, states: filters.states.filter((x) => x !== s) })
            }
            aria-label={`Remove filter: ${STATE_LABELS[s]}`}
          >
            {STATE_LABELS[s]} <span aria-hidden="true">✕</span>
          </button>
        ))}
        {filters.agent !== undefined && (
          <button
            type="button"
            className={styles.chip}
            onClick={() => setFilters({ ...filters, agent: undefined })}
            aria-label="Remove agent filter"
          >
            agent: {agentName(filters.agent)} <span aria-hidden="true">✕</span>
          </button>
        )}
        {filters.ticket !== undefined && (
          <button
            type="button"
            className={styles.chip}
            onClick={() => setFilters({ ...filters, ticket: undefined })}
            aria-label="Remove ticket filter"
          >
            ticket: {ticketKey(filters.ticket)} <span aria-hidden="true">✕</span>
          </button>
        )}
        <select
          className={styles.filterSelect}
          aria-label="Add state filter"
          value=""
          onChange={(e) => {
            const s = e.target.value as RunState;
            if (s !== ("" as string) && !filters.states.includes(s)) {
              setFilters({ ...filters, states: [...filters.states, s] });
            }
          }}
        >
          <option value="">+ State</option>
          {RUN_STATES.filter((s) => !filters.states.includes(s)).map((s) => (
            <option key={s} value={s}>
              {STATE_LABELS[s]}
            </option>
          ))}
        </select>
        <select
          className={styles.filterSelect}
          aria-label="Filter by agent"
          value={filters.agent ?? ""}
          onChange={(e) =>
            setFilters({ ...filters, agent: e.target.value === "" ? undefined : e.target.value })
          }
        >
          <option value="">+ Agent</option>
          {(agents.data?.agents ?? []).map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
      </div>

      {runsQuery.isPending ? (
        <p className={styles.muted}>Loading runs…</p>
      ) : runsQuery.isError ? (
        <p className={styles.errorText}>The run list failed to load.</p>
      ) : runs.length === 0 ? (
        <EmptyState
          headline="No runs yet"
          body="Delegate a ticket to an agent and its run appears here."
          primary={
            <Link to="/p/$key/board" params={{ key }} className={styles.primaryLink}>
              Go to board
            </Link>
          }
        />
      ) : shown.length === 0 ? (
        <EmptyState
          headline="No runs match these filters"
          body="Every chip above is removable — clear them to see the project's runs."
          primary={
            <button type="button" className={styles.primaryLink} onClick={() => setView("all")}>
              Clear filters
            </button>
          }
        />
      ) : (
        <table className={styles.runTable}>
          <thead>
            <tr>
              <th>Status</th>
              <th>Agent</th>
              <th>Ticket</th>
              <th>Trigger</th>
              <th className={styles.num}>Duration</th>
              <th className={styles.num}>Cost</th>
              <th>Started</th>
            </tr>
          </thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.id}>
                <td>
                  <Link
                    to="/p/$key/runs/$id"
                    params={{ key, id: r.id }}
                    className={styles.runLink}
                  >
                    <StatusDot
                      status={r.state}
                      label={
                        r.state === "queued" && r.hold_reason !== ""
                          ? `Queued — ${r.hold_reason}`
                          : undefined
                      }
                    />
                  </Link>
                </td>
                <td>{agentName(r.agent_id)}</td>
                <td>
                  {r.ticket_id !== null ? (
                    <span className={styles.ticketKey}>{ticketKey(r.ticket_id)}</span>
                  ) : (
                    <span className={styles.muted}>—</span>
                  )}
                </td>
                <td>{r.trigger_id !== null ? "trigger" : "manual"}</td>
                <td className={styles.num}>{runDuration(r, now)}</td>
                <td className={styles.num}>
                  <CostChip usd={r.cost_cents > 0 ? r.cost_cents / 100 : null} />
                </td>
                <td>{r.started_at !== null ? formatRelativeTime(r.started_at) : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
