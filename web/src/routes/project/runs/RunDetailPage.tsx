/*
 * Run detail (UI spec §5.7): three panes — step timeline · tool-aware detail · Context &
 * cost — over a current-step line and the (S24-disabled) steering bar.
 *
 * The load-bearing rules, in code:
 * - Verbosity is client-side over `level`, instant, no refetch; the default drops to
 *   Summary when ≥4 of the project's runs are in flight.
 * - ToolCallRow grouping is client-side over the normalized stream (timeline.ts).
 * - Failed steps auto-expand and are the default selection; `f` jumps to the next failure.
 * - Selection (?step=, ?line=) and verbosity (?level=) live in the URL (rule 12).
 * - Live activities stream over SSE topic run:<id> into the query cache (applyEvent.ts);
 *   the §7 acknowledgment SLA renders a stall warning when a running run has produced no
 *   first thought within 10 seconds.
 * - The timeline is a hand-rolled fixed-height virtualized list (VirtualList.tsx) so a
 *   500-step run scrolls at 60fps.
 */
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";

import { CostChip } from "../../../components/CostChip/CostChip";
import { StatusDot } from "../../../components/StatusDot/StatusDot";
import type {
  Run,
  RunActivity,
  RunContextItem,
  RunOutput,
} from "../../../lib/api/client";
import { useAgentsQuery } from "../../../lib/api/agentQueries";
import {
  useRunActivitiesQuery,
  useRunDetailQuery,
  useRunsQuery,
} from "../../../lib/api/runQueries";
import { formatDuration, formatRelativeTime, formatTokenCount } from "../../../lib/format/format";
import { useKeyBindings, useKeyScope } from "../../../lib/keyboard/hooks";
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { ActivityDetail } from "./renderers";
import { InterventionBar } from "./Intervention";
import styles from "./runs.module.css";
import {
  buildTimeline,
  defaultSelection,
  nextFailure,
  timingSplit,
  type TimelineRow,
  type Verbosity,
} from "./timeline";
import { VirtualList } from "./VirtualList";
import { inFlight } from "./viewState";

/** Every timeline row is one line high — what makes the windowing exact. */
const ROW_HEIGHT = 28;

const TERMINAL = new Set(["completed", "failed", "timed_out", "canceled", "loop_stopped"]);

function isTerminal(state: Run["state"]): boolean {
  return TERMINAL.has(state);
}

/** A ticking "now" while the run is live — header elapsed and the stall warning both read it. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

const GLYPHS: Record<RunActivity["type"], string> = {
  thought: "…",
  action: "●",
  elicitation: "▲",
  response: "↩",
  error: "✕",
  system: "·",
  provision: "○",
};

function stepGlyph(a: RunActivity): { glyph: string; tone: string } {
  if (a.type === "provision") {
    const state = (a.payload as { state?: string } | null)?.state;
    if (state === "ok") return { glyph: "✓", tone: "ok" };
    if (state === "failed") return { glyph: "✕", tone: "fail" };
    if (state === "running") return { glyph: "●", tone: "running" };
    return { glyph: "○", tone: "muted" };
  }
  if (a.ok === false) return { glyph: "✕", tone: "fail" };
  if (a.type === "action") {
    return a.ok === true ? { glyph: "✓", tone: "ok" } : { glyph: "●", tone: "running" };
  }
  if (a.type === "elicitation") return { glyph: "▲", tone: "needs-you" };
  if (a.type === "error") return { glyph: "✕", tone: "fail" };
  return { glyph: GLYPHS[a.type], tone: "muted" };
}

/** The §5.7 timing gutter: a right-aligned duration with a queued/model/tool split bar. */
function TimingGutter({ a }: { a: RunActivity }) {
  const split = timingSplit(a);
  if (split === null) return <span className={styles.gutter} />;
  const known = split.queuedMs + split.modelMs + split.toolMs;
  const base = Math.max(split.totalMs, known, 1);
  const pct = (n: number) => `${((n / base) * 100).toFixed(1)}%`;
  return (
    <span
      className={styles.gutter}
      title={`queued ${formatDuration(split.queuedMs)} · model ${formatDuration(split.modelMs)} · tool ${formatDuration(split.toolMs)}`}
    >
      {known > 0 && (
        <span className={styles.gutterBar} aria-hidden="true">
          <span className={styles.segQueued} style={{ width: pct(split.queuedMs) }} />
          <span className={styles.segModel} style={{ width: pct(split.modelMs) }} />
          <span className={styles.segTool} style={{ width: pct(split.toolMs) }} />
        </span>
      )}
      <span className={styles.gutterText}>{formatDuration(split.totalMs)}</span>
    </span>
  );
}

export function RunDetailPage() {
  const { key, id } = useParams({ from: "/shell/p/$key/runs/$id" });
  const { step, line, level } = useSearch({ from: "/shell/p/$key/runs/$id" });
  const navigate = useNavigate({ from: "/p/$key/runs/$id" });

  useStreamTopics([`run:${id}`]);

  const detailQuery = useRunDetailQuery(id);
  const activitiesQuery = useRunActivitiesQuery(id);
  const runsQuery = useRunsQuery(key);
  const agents = useAgentsQuery(key);

  const run = detailQuery.data?.run;
  const activities = useMemo(
    () => activitiesQuery.data?.activities ?? [],
    [activitiesQuery.data],
  );

  // Verbosity: URL wins; else the concurrency default (≥4 in flight → Summary). Switching
  // writes the URL and filters client-side — no refetch anywhere on this path.
  const inFlightCount = inFlight(runsQuery.data?.runs ?? []).length;
  const verbosity: Verbosity = level ?? (inFlightCount >= 4 ? "summary" : "normal");
  const setVerbosity = (v: Verbosity) => {
    void navigate({
      search: (prev) => ({ ...prev, level: v }),
      replace: true,
    });
  };

  // Selection: ?step= wins; else the first failed step (auto-expanded on load, no click),
  // else the tail of the stream.
  const selectedSeq = step ?? defaultSelection(activities);
  const selected = activities.find((a) => a.seq === selectedSeq);
  const selectStep = (seq: number) => {
    void navigate({
      search: (prev) => ({ ...prev, step: seq, line: undefined }),
      replace: false,
    });
  };
  const selectLine = (n: number) => {
    void navigate({ search: (prev) => ({ ...prev, line: n }), replace: true });
  };

  // Manually-toggled groups; failed groups and the selection's group auto-expand in
  // buildTimeline regardless.
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const toggleGroup = (firstSeq: number) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(firstSeq)) next.delete(firstSeq);
      else next.add(firstSeq);
      return next;
    });
  };

  const rows = useMemo(
    () => buildTimeline(activities, { verbosity, expanded, selectedSeq }),
    [activities, verbosity, expanded, selectedSeq],
  );

  // Jump the window to the selection when the selection changes (permalink open, `f`) —
  // but not on every streamed append.
  const selectedIndex = rows.findIndex(
    (r) => r.kind === "step" && r.activity.seq === selectedSeq,
  );
  const [jump, setJump] = useState<number | undefined>(undefined);
  const lastJumpSeq = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (selectedSeq === undefined || selectedSeq === lastJumpSeq.current) return;
    if (selectedIndex === -1) return;
    lastJumpSeq.current = selectedSeq;
    setJump(selectedIndex);
  }, [selectedSeq, selectedIndex]);

  // `f` — next failure (§6), wrapping.
  useKeyScope("route", true);
  useKeyBindings(
    () => [
      {
        id: "run.next-failure",
        scope: "route",
        chord: "f",
        title: "Next failure",
        group: "Run",
        run: () => {
          const seq = nextFailure(activities, selectedSeq);
          if (seq !== null) selectStep(seq);
        },
      },
    ],
    [activities, selectedSeq],
  );

  const live = run !== undefined && !isTerminal(run.state);
  const now = useNow(live);

  // §7 acknowledgment SLA: a running run must emit its first thought within 10 seconds.
  const hasThought = activities.some((a) => a.type === "thought");
  const stalled =
    run !== undefined &&
    run.state === "running" &&
    !hasThought &&
    run.started_at !== null &&
    now - new Date(run.started_at).getTime() > 10_000;

  if (detailQuery.isPending) {
    return <p className={styles.muted}>Loading run…</p>;
  }
  if (detailQuery.isError || run === undefined) {
    return <p className={styles.errorText}>This run failed to load.</p>;
  }

  const agentName = agents.data?.agents.find((a) => a.id === run.agent_id)?.name ?? "agent";
  const elapsed =
    run.started_at !== null
      ? formatDuration(
          (run.ended_at !== null ? new Date(run.ended_at).getTime() : now) -
            new Date(run.started_at).getTime(),
        )
      : null;

  return (
    <div className={styles.detailPage}>
      <header className={styles.runHeader}>
        <Link to="/p/$key/runs" params={{ key }} className={styles.backLink}>
          ← Runs
        </Link>
        <h1 className={styles.runTitle}>Run #{run.seq}</h1>
        <StatusDot status={run.state} />
        <span className={styles.headerMeta}>{agentName}</span>
        <span className={styles.headerMeta}>{run.model}</span>
        {elapsed !== null && <span className={styles.headerMeta}>{elapsed}</span>}
        <CostChip
          usd={run.cost_cents > 0 ? run.cost_cents / 100 : null}
          split={{
            inputTokens: run.tokens_in,
            outputTokens: run.tokens_out,
            cacheReadTokens: run.tokens_cache_read,
          }}
        />
        {run.state === "queued" && run.hold_reason !== "" && (
          <span className={styles.holdReason}>{run.hold_reason}</span>
        )}
      </header>

      <div className={styles.panes}>
        {/* Left — step timeline (virtualized) + the verbosity switch. */}
        <aside className={styles.timelinePane}>
          <VirtualList
            items={rows}
            rowHeight={ROW_HEIGHT}
            itemKey={rowKey}
            scrollToIndex={jump}
            className={styles.timelineScroll}
            aria-label="Step timeline"
            renderRow={(row) => (
              <TimelineRowView
                row={row}
                selectedSeq={selectedSeq}
                onSelect={selectStep}
                onToggle={toggleGroup}
              />
            )}
          />
          <div className={styles.verbosityRow} role="radiogroup" aria-label="Verbosity">
            {(["summary", "normal", "verbose"] as const).map((v) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={verbosity === v}
                data-active={verbosity === v || undefined}
                className={styles.verbosityButton}
                onClick={() => setVerbosity(v)}
              >
                {v[0].toUpperCase() + v.slice(1)}
              </button>
            ))}
          </div>
        </aside>

        {/* Centre — the tool-aware detail of the selected step. */}
        <main className={styles.detailPane} aria-label="Step detail">
          {selected !== undefined ? (
            <ActivityDetail
              a={selected}
              sel={{ selected: line, onSelect: selectLine }}
              intervene={{
                projectKey: key,
                agentID: run.agent_id,
                elicitations: detailQuery.data.elicitations,
              }}
            />
          ) : (
            <p className={styles.muted}>
              {activities.length === 0
                ? "No activity yet — steps appear here as the run works."
                : "Select a step from the timeline."}
            </p>
          )}
        </main>

        {/* Right — Context & cost. */}
        <ContextPane
          run={run}
          context={detailQuery.data.context}
          outputs={detailQuery.data.outputs}
        />
      </div>

      {/* The current-step line: the run's own mutable sentence while it works; the outcome
          when it is done; the §7 stall warning when the model has gone quiet at the start. */}
      <footer className={styles.currentStepLine} data-stalled={stalled || undefined}>
        {stalled ? (
          <span className={styles.stallWarning}>
            ▲ Still waiting on the model — no activity {`${Math.floor((now - new Date(run.started_at as string).getTime()) / 1000)}s`} after start.
          </span>
        ) : isTerminal(run.state) ? (
          <span>{outcomeSummary(run, activities)}</span>
        ) : (
          <span>
            Step {run.step_count > 0 ? run.step_count : "–"} —{" "}
            {run.current_step !== "" ? run.current_step : "starting up"}
          </span>
        )}
        <span className={styles.currentStepStatus}>
          <StatusDot status={run.state} />
        </span>
      </footer>

      {/* S24: the live steering composer, Stop, and Take over. */}
      <InterventionBar run={run} messages={detailQuery.data.messages} />
    </div>
  );
}

function rowKey(row: TimelineRow): string {
  return row.kind === "group"
    ? `g${row.firstSeq}`
    : `s${row.activity.seq}${row.child ? "c" : ""}`;
}

function TimelineRowView({
  row,
  selectedSeq,
  onSelect,
  onToggle,
}: {
  row: TimelineRow;
  selectedSeq: number | undefined;
  onSelect: (seq: number) => void;
  onToggle: (firstSeq: number) => void;
}) {
  if (row.kind === "group") {
    return (
      <button
        type="button"
        className={styles.timelineRow}
        data-group="true"
        data-failed={!row.ok || undefined}
        onClick={() => onToggle(row.firstSeq)}
        aria-expanded={row.expanded}
      >
        <span className={styles.rowGlyph} data-tone={row.ok ? "ok" : "fail"} aria-hidden="true">
          {row.ok ? "✓" : "✕"}
        </span>
        <span className={styles.rowTitle}>
          {row.label} <span aria-hidden="true">{row.expanded ? "▾" : "▸"}</span>
        </span>
        {row.costCents > 0 && (
          <span className={styles.rowCost}>
            <CostChip usd={row.costCents / 100} />
          </span>
        )}
        <span className={styles.gutter}>
          <span className={styles.gutterText}>
            {row.durationMs !== null ? formatDuration(row.durationMs) : ""}
          </span>
        </span>
      </button>
    );
  }
  const a = row.activity;
  const { glyph, tone } = stepGlyph(a);
  return (
    <button
      type="button"
      className={styles.timelineRow}
      data-child={row.child || undefined}
      data-selected={a.seq === selectedSeq || undefined}
      data-failed={a.ok === false || undefined}
      onClick={() => onSelect(a.seq)}
    >
      <span className={styles.rowGlyph} data-tone={tone} aria-hidden="true">
        {glyph}
      </span>
      <span className={styles.rowTitle}>{a.title}</span>
      {a.attempt > 1 && <span className={styles.retryBadge}>×{a.attempt}</span>}
      {a.cost_cents > 0 && (
        <span className={styles.rowCost}>
          <CostChip usd={a.cost_cents / 100} />
        </span>
      )}
      <TimingGutter a={a} />
    </button>
  );
}

/** The right pane: run_context_items with each reason verbatim (§11), the token split, the
 * cost with the hover breakdown, and the run's outputs. */
function ContextPane({
  run,
  context,
  outputs,
}: {
  run: Run;
  context: RunContextItem[];
  outputs: RunOutput[];
}) {
  const pr = outputs.find((o) => o.kind === "pull_request");
  const branch = outputs.find((o) => o.kind === "branch") ?? outputs.find((o) => o.kind === "partial_work");
  return (
    <aside className={styles.contextPane} aria-label="Context and cost">
      <h2 className={styles.paneTitle}>Loaded context</h2>
      {context.length === 0 ? (
        <p className={styles.muted}>Nothing beyond the ticket itself.</p>
      ) : (
        <ul className={styles.contextList}>
          {context.map((c) => (
            <li key={`${c.provider}:${c.source_ref}:${c.position}`} className={styles.contextItem}>
              <span className={styles.contextTitle}>▸ {c.title}</span>
              <span className={styles.contextReason}>{c.reason}</span>
              <span className={styles.contextTokens}>{formatTokenCount(c.tokens)} tok</span>
            </li>
          ))}
        </ul>
      )}
      <div className={styles.costBlock}>
        <div className={styles.costRow}>
          <span>Tokens</span>
          <span>{formatTokenCount(run.tokens_in + run.tokens_out)}</span>
        </div>
        <div className={styles.costSplitRow}>
          in {formatTokenCount(run.tokens_in)} · out {formatTokenCount(run.tokens_out)} · cache{" "}
          {formatTokenCount(run.tokens_cache_read)}
        </div>
        <div className={styles.costRow}>
          <span>Cost</span>
          <CostChip
            usd={run.cost_cents > 0 ? run.cost_cents / 100 : null}
            split={{
              inputTokens: run.tokens_in,
              outputTokens: run.tokens_out,
              cacheReadTokens: run.tokens_cache_read,
            }}
          />
        </div>
      </div>
      {(branch !== undefined || pr !== undefined || outputs.length > 0) && (
        <>
          <h2 className={styles.paneTitle}>Outputs</h2>
          <ul className={styles.outputList}>
            {outputs.map((o) => (
              <li key={o.id} className={styles.outputItem}>
                <span className={styles.outputKind}>{o.kind.replace("_", " ")}</span>
                {o.url !== "" ? (
                  <a href={o.url} target="_blank" rel="noreferrer" className={styles.outputRef}>
                    {o.ref}
                  </a>
                ) : (
                  <code className={styles.outputRef}>{o.ref}</code>
                )}
                {o.summary !== "" && <span className={styles.outputSummary}>{o.summary}</span>}
              </li>
            ))}
          </ul>
        </>
      )}
      <div className={styles.paneFoot}>
        <span className={styles.muted}>queued {formatRelativeTime(run.queued_at)}</span>
        {run.branch !== null && <code className={styles.branchName}>{run.branch}</code>}
      </div>
    </aside>
  );
}

/** The outcome line a terminal run replaces its current-step line with. */
function outcomeSummary(run: Run, activities: RunActivity[]): string {
  switch (run.state) {
    case "completed": {
      const response = [...activities].reverse().find((a) => a.type === "response");
      return response !== undefined ? `Completed — ${response.title}` : "Completed";
    }
    case "failed":
    case "timed_out":
      return run.error_message !== ""
        ? `${run.state === "failed" ? "Failed" : "Timed out"} — ${run.error_message}`
        : run.state === "failed"
          ? "Failed"
          : "Timed out";
    case "canceled":
      return run.state_reason !== "" ? `Canceled — ${run.state_reason}` : "Canceled";
    case "loop_stopped":
      return run.state_reason !== "" ? `Loop stopped — ${run.state_reason}` : "Loop stopped";
    default:
      return "";
  }
}
