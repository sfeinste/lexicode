/*
 * Run detail (UI spec §5.7): three panes — step timeline · tool-aware detail · Context &
 * cost — over a current-step line and the steering bar.
 *
 * The load-bearing rules, in code:
 * - Verbosity is client-side over `level`, instant, no refetch; the default drops to
 *   Summary when ≥4 of the project's runs are in flight.
 * - ToolCallRow grouping is client-side over the normalized stream (timeline.ts).
 * - Failed steps auto-expand and are the default selection; `f` jumps to the next failure —
 *   and so does the **Next failure** button beside the verbosity switch (see below).
 * - Selection (?step=, ?line=) and verbosity (?level=) live in the URL (rule 12).
 * - Live activities stream over SSE topic run:<id> into the query cache (applyEvent.ts);
 *   the §7 acknowledgment SLA renders a stall warning when a running run has produced no
 *   first thought within 10 seconds.
 * - The timeline is a hand-rolled fixed-height virtualized list (VirtualList.tsx) so a
 *   500-step run scrolls at 60fps.
 *
 * ---- D-1 (amended): this screen is the Material UI proof of concept (S39) --------------
 *
 * Why this screen and not an easier one: it is the hardest thing in the app. Three panes,
 * a live stream, a virtualized list, a radio-style view switch, a dense selectable
 * timeline, inline approvals, a modal, and the entire §4 status vocabulary. If the library
 * could not carry this, it could not carry Lexicode.
 *
 * What the library supplies here, by name:
 *   Box · Paper · Stack · Typography · Button · ToggleButtonGroup/ToggleButton · Tooltip ·
 *   Chip · Alert/AlertTitle · Divider · List/ListItem/ListItemButton · Link
 *
 * The two compositions, declared rather than smuggled:
 *   1. THE THREE-PANE FRAME is `Box` with a CSS grid and a `Paper` per pane. Material UI
 *      has no application-layout component, so the frame is spec §5.7's own geometry
 *      expressed in `sx`, with the §10 breakpoints as MUI breakpoint keys.
 *   2. THE VIRTUALIZED TIMELINE keeps VirtualList.tsx (~80 lines, no dependency) and
 *      renders `ListItemButton` rows inside it. MUI's own virtualized grid is a paid tier
 *      (MUI X Pro), and this list is fixed-height and one-dimensional, which is the case
 *      where windowing is arithmetic rather than a product.
 *
 * The discoverability fix this screen was carrying: `f` — "next failure" — was reachable
 * ONLY by pressing the key. Nothing on the screen said the affordance existed. It now has
 * a labelled button that shares one implementation with the chord, so the shortcut is a
 * shortcut rather than the only door.
 */
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";
import Alert from "@mui/material/Alert";
import AlertTitle from "@mui/material/AlertTitle";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Divider from "@mui/material/Divider";
import Link from "@mui/material/Link";
import List from "@mui/material/List";
import ListItem from "@mui/material/ListItem";
import ListItemButton from "@mui/material/ListItemButton";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import ToggleButton from "@mui/material/ToggleButton";
import ToggleButtonGroup from "@mui/material/ToggleButtonGroup";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { visuallyHidden } from "@mui/utils";

import { ContextMeter } from "../../../components/ContextMeter/ContextMeter";
import { CostChip } from "../../../components/CostChip/CostChip";
import { LoopChain } from "../../../components/LoopChain/LoopChain";
import { StatusDot } from "../../../components/StatusDot/StatusDot";
import type {
  Run,
  RunActivity,
  RunContextItem,
  RunOutput,
} from "../../../lib/api/client";
import { useAgentsQuery } from "../../../lib/api/agentQueries";
import { useContextBudgetQuery } from "../../../lib/api/contextQueries";
import { useProjectQuery } from "../../../lib/api/projectQueries";
import {
  useRunActivitiesQuery,
  useRunDetailQuery,
  useRunsQuery,
} from "../../../lib/api/runQueries";
import { useRunChainQuery } from "../../../lib/api/triggerQueries";
import { markMomentSeen, momentPending } from "../../../lib/activation";
import { formatDuration, formatRelativeTime, formatTokenCount } from "../../../lib/format/format";
import { formatDiffStat, isLargeDiff } from "../../../lib/format/prSize";
import { chordLabel } from "../../../lib/keyboard/hooks";
import { useKeyBindings, useKeyScope } from "../../../lib/keyboard/hooks";
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { useMediaQuery } from "../../../lib/useMediaQuery";
import { AppLink, AppLinkButton } from "../../../theme/routerLinks";
import { runAnnouncement, type AnnounceSnapshot } from "./announce";
import { ActivityDetail } from "./renderers";
import { InterventionBar } from "./Intervention";
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

/** The timeline pane’s scroll viewport. Windowing needs a bounded box (VirtualList). */
const TIMELINE_HEIGHT = 420;

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

/** §3.2 hues as theme palette paths — the same seven meanings StatusDot renders. */
const TONE: Record<string, string> = {
  ok: "success.main",
  fail: "error.main",
  running: "lexicode.running",
  "needs-you": "lexicode.needsYou",
  muted: "text.disabled",
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

/**
 * The §5.7 timing gutter: a right-aligned duration with a queued/model/tool split bar.
 * A composition — three `Box` segments inside a `Tooltip`, because "why was this slow"
 * has to be answerable at a glance and no library ships a three-segment micro-bar.
 */
function TimingGutter({ a }: { a: RunActivity }) {
  const split = timingSplit(a);
  if (split === null) return <Box component="span" sx={{ minWidth: 72 }} />;
  const known = split.queuedMs + split.modelMs + split.toolMs;
  const base = Math.max(split.totalMs, known, 1);
  const pct = (n: number) => `${((n / base) * 100).toFixed(1)}%`;
  return (
    <Tooltip
      title={`queued ${formatDuration(split.queuedMs)} · model ${formatDuration(
        split.modelMs,
      )} · tool ${formatDuration(split.toolMs)}`}
    >
      <Box
        component="span"
        sx={{ display: "inline-flex", alignItems: "center", gap: "6px", minWidth: 72 }}
      >
        {known > 0 && (
          <Box
            component="span"
            aria-hidden="true"
            sx={{ display: "inline-flex", width: 36, height: 4, borderRadius: 2, overflow: "hidden" }}
          >
            <Box component="span" sx={{ width: pct(split.queuedMs), bgcolor: "text.disabled" }} />
            <Box component="span" sx={{ width: pct(split.modelMs), bgcolor: "lexicode.running" }} />
            <Box component="span" sx={{ width: pct(split.toolMs), bgcolor: "primary.main" }} />
          </Box>
        )}
        <Typography
          component="span"
          variant="body2"
          sx={{ fontFamily: "var(--font-mono)", color: "text.disabled", ml: "auto" }}
        >
          {formatDuration(split.totalMs)}
        </Typography>
      </Box>
    </Tooltip>
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

  /**
   * Next failure — ONE implementation, TWO doors (§6 keyboard map + the button beside the
   * verbosity switch). The `f` chord used to be the only way to reach this, which is
   * exactly the pattern LEXI-13 is about: the capability existed, the way in did not.
   */
  const failureTarget = nextFailure(activities, selectedSeq);
  const goToNextFailure = () => {
    if (failureTarget !== null) selectStep(failureTarget);
  };

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

  // §10 responsive: >=1400 all three panes; below, the context pane collapses to a header
  // toggle (1100-1400), and the whole thing stacks vertically under 1100.
  const threePane = useMediaQuery("(min-width: 1400px)", true);
  const [contextOpen, setContextOpen] = useState(false);
  const contextShown = threePane || contextOpen;

  // §10 live region: announce run state transitions and step boundaries ONLY. The input is
  // (state, step_count, current_step) — a streamed log line can never reach the announcer.
  const [announced, setAnnounced] = useState("");
  const lastSnapshot = useRef<AnnounceSnapshot | null>(null);
  useEffect(() => {
    if (run === undefined) return;
    const next: AnnounceSnapshot = {
      state: run.state,
      stepCount: run.step_count,
      currentStep: run.current_step,
    };
    const text = runAnnouncement(lastSnapshot.current, next);
    lastSnapshot.current = next;
    if (text !== null) setAnnounced(text);
  }, [run?.state, run?.step_count, run?.current_step]); // eslint-disable-line react-hooks/exhaustive-deps -- snapshot fields only

  // §8's two activation moments, each shown once per project (localStorage; activation.ts).
  const completedRuns = (runsQuery.data?.runs ?? []).filter((r) => r.state === "completed");
  const isFirstCompleted =
    run !== undefined &&
    run.state === "completed" &&
    completedRuns.length === 1 &&
    completedRuns[0].id === run.id;
  const [showFirstCompleted] = useState(
    () => momentPending("first-completed-run", key),
  );
  const firstCompletedMoment = isFirstCompleted && showFirstCompleted;
  useEffect(() => {
    if (firstCompletedMoment) markMomentSeen("first-completed-run", key);
  }, [firstCompletedMoment, key]);

  const isNeedsInput = run !== undefined && run.state === "needs_input";
  const [showFirstNeedsInput] = useState(() => momentPending("first-needs-input", key));
  const firstNeedsInputMoment = isNeedsInput && showFirstNeedsInput;
  useEffect(() => {
    if (firstNeedsInputMoment) markMomentSeen("first-needs-input", key);
  }, [firstNeedsInputMoment, key]);

  // §7 acknowledgment SLA: a running run must emit its first thought within 10 seconds.
  const hasThought = activities.some((a) => a.type === "thought");
  const stalled =
    run !== undefined &&
    run.state === "running" &&
    !hasThought &&
    run.started_at !== null &&
    now - new Date(run.started_at).getTime() > 10_000;

  if (detailQuery.isPending) {
    return (
      <Typography variant="body1" sx={{ color: "text.secondary", p: 2 }}>
        Loading run…
      </Typography>
    );
  }
  if (detailQuery.isError || run === undefined) {
    return (
      <Alert severity="error" sx={{ m: 2 }}>
        This run failed to load.
      </Alert>
    );
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
    <Box sx={{ display: "grid", gap: 1, p: 1, minWidth: 0 }}>
      <Stack
        component="header"
        direction="row"
        spacing={1}
        useFlexGap
        sx={{ alignItems: "center", flexWrap: "wrap" }}
      >
        <AppLinkButton to="/p/$key/runs" params={{ key }} size="small">
          ← Runs
        </AppLinkButton>
        <Typography variant="h1" sx={{ fontFamily: "var(--font-mono)" }}>
          Run #{run.seq}
        </Typography>
        <StatusDot status={run.state} />
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          {agentName}
        </Typography>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          {run.model}
        </Typography>
        {elapsed !== null && (
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {elapsed}
          </Typography>
        )}
        <CostChip
          usd={run.cost_cents > 0 ? run.cost_cents / 100 : null}
          split={{
            inputTokens: run.tokens_in,
            outputTokens: run.tokens_out,
            cacheReadTokens: run.tokens_cache_read,
          }}
        />
        {run.state === "queued" && run.hold_reason !== "" && (
          <Chip size="small" variant="outlined" label={run.hold_reason} />
        )}
        {!threePane && (
          <Button
            size="small"
            aria-expanded={contextShown}
            onClick={() => setContextOpen((v) => !v)}
            sx={{ ml: "auto" }}
          >
            Context {contextShown ? "▾" : "▸"}
          </Button>
        )}
      </Stack>

      {/* §10: state transitions and step boundaries only — never the log stream. */}
      <Box aria-live="polite" role="status" sx={visuallyHidden}>
        {announced}
      </Box>

      {/* §8: the first completed run is the activation event — mark it, teach the next
          action. Restrained: one card, shown exactly once per project. */}
      {firstCompletedMoment && (
        <Alert
          severity="success"
          icon={
            <Box component="span" aria-hidden="true">
              ✓
            </Box>
          }
          aria-label="First completed run"
        >
          <AlertTitle>Your first completed run.</AlertTitle>
          Next:{" "}
          {(() => {
            const pr = detailQuery.data.outputs.find((o) => o.kind === "pull_request");
            const out = pr ?? detailQuery.data.outputs.find((o) => o.url !== "");
            return out !== undefined && out.url !== "" ? (
              <Link href={out.url} target="_blank" rel="noreferrer">
                review the diff
              </Link>
            ) : (
              <>review the output below</>
            );
          })()}
          , or turn the feedback into a{" "}
          <AppLink to="/p/$key/wiki" params={{ key }}>
            wiki page
          </AppLink>{" "}
          so the next run starts smarter.
        </Alert>
      )}

      {/* §8: the first `needs input` teaches that agents are interactive — unmissable. */}
      {firstNeedsInputMoment && (
        <Alert
          severity="warning"
          icon={
            <Box component="span" aria-hidden="true">
              ▲
            </Box>
          }
          aria-label="First question from an agent"
        >
          <AlertTitle>{agentName} is asking you a question.</AlertTitle>
          Agents aren&apos;t fire-and-forget: this run is paused until you answer, right here
          in the step detail below. Your answer goes straight back into the running session.
        </Alert>
      )}

      {/* S29: a loop-stopped run leads with the cycle it built — the §5.9 chain view. */}
      {run.state === "loop_stopped" && <LoopChainPanel projectKey={key} runId={run.id} />}

      <Box
        sx={{
          display: "grid",
          gap: 1,
          minWidth: 0,
          alignItems: "start",
          // §10's breakpoints are 1100 and 1400, which are NOT MUI's `md`/`lg` (900/1200).
          // The spec's numbers win, so they are written as raw media queries rather than
          // rounded to the nearest theme breakpoint: stacked below 1100; timeline + detail
          // from 1100; the context pane joins at 1400 (or earlier, via the header toggle).
          gridTemplateColumns: "1fr",
          "@media (min-width: 1100px)": {
            gridTemplateColumns: contextShown
              ? "300px minmax(0, 1fr) 280px"
              : "300px minmax(0, 1fr)",
          },
        }}
      >
        {/* Left — step timeline (virtualized) + the verbosity switch + Next failure. */}
        <Paper
          component="aside"
          variant="outlined"
          sx={{ display: "grid", gridTemplateRows: "minmax(0, 1fr) auto", minWidth: 0 }}
        >
          {/* §8: an empty timeline says what happens next, and it replaces the list rather
              than sitting under an empty 420px box. */}
          {rows.length === 0 ? (
            <Box
              aria-label="Step timeline"
              sx={{ height: TIMELINE_HEIGHT, display: "grid", alignContent: "start", p: 1 }}
            >
              <Typography variant="body2" sx={{ color: "text.secondary" }}>
                {live
                  ? "No steps yet. They appear here as the agent works — you can queue a message for it in the composer below."
                  : "This run recorded no steps. Its outcome is on the status line below."}
              </Typography>
            </Box>
          ) : (
            <VirtualList
              items={rows}
              rowHeight={ROW_HEIGHT}
              itemKey={rowKey}
              scrollToIndex={jump}
              aria-label="Step timeline"
              height={TIMELINE_HEIGHT}
              defaultHeight={TIMELINE_HEIGHT}
              renderRow={(row) => (
                <TimelineRowView
                  row={row}
                  selectedSeq={selectedSeq}
                  onSelect={selectStep}
                  onToggle={toggleGroup}
                />
              )}
            />
          )}
          <Divider />
          <Stack
            direction="row"
            spacing={1}
            sx={{ alignItems: "center", justifyContent: "space-between", p: "6px" }}
          >
            <ToggleButtonGroup
              size="small"
              exclusive
              value={verbosity}
              aria-label="Verbosity"
              onChange={(_e, v: Verbosity | null) => {
                if (v !== null) setVerbosity(v);
              }}
            >
              {(["summary", "normal", "verbose"] as const).map((v) => (
                <ToggleButton key={v} value={v}>
                  {v[0].toUpperCase() + v.slice(1)}
                </ToggleButton>
              ))}
            </ToggleButtonGroup>
            {/*
              The visible door to `f`. Disabled — with the reason in the tooltip — rather
              than hidden when a run has no failures, so the affordance still teaches that
              the capability exists.
            */}
            <Tooltip
              title={
                failureTarget === null
                  ? "No failed steps in this run"
                  : `Jump to the next failed step (${chordLabel("f")})`
              }
            >
              <Box component="span">
                <Button
                  size="small"
                  color="error"
                  disabled={failureTarget === null}
                  onClick={goToNextFailure}
                >
                  <Box component="span" aria-hidden="true" sx={{ mr: "4px" }}>
                    ✕
                  </Box>
                  Next failure
                </Button>
              </Box>
            </Tooltip>
          </Stack>
        </Paper>

        {/* Centre — the tool-aware detail of the selected step. */}
        <Box component="main" aria-label="Step detail" sx={{ minWidth: 0, display: "grid", gap: 1 }}>
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
            <Typography variant="body1" sx={{ color: "text.secondary" }}>
              {activities.length === 0
                ? "No activity yet — steps appear here as the run works. Send it a message from the composer below and it lands as soon as the agent starts."
                : "Select a step from the timeline to see what the agent did."}
            </Typography>
          )}
        </Box>

        {/* Right — Context & cost. Collapses to the header toggle below 1400 (§10). */}
        {contextShown && (
          <ContextPane
            run={run}
            projectKey={key}
            context={detailQuery.data.context}
            outputs={detailQuery.data.outputs}
          />
        )}
      </Box>

      {/* The current-step line: the run's own mutable sentence while it works; the outcome
          when it is done; the §7 stall warning when the model has gone quiet at the start. */}
      <Paper
        component="footer"
        variant="outlined"
        data-stalled={stalled || undefined}
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 1,
          px: 1,
          py: "6px",
          backgroundColor: "lexicode.surface2",
        }}
      >
        {stalled ? (
          <Typography variant="body1" sx={{ color: "lexicode.needsYou" }}>
            ▲ Still waiting on the model — no activity{" "}
            {`${Math.floor((now - new Date(run.started_at as string).getTime()) / 1000)}s`} after
            start.
          </Typography>
        ) : isTerminal(run.state) ? (
          <Typography variant="body1">{outcomeSummary(run, activities)}</Typography>
        ) : (
          <Typography variant="body1">
            Step {run.step_count > 0 ? run.step_count : "–"} —{" "}
            {run.current_step !== "" ? run.current_step : "starting up"}
          </Typography>
        )}
        <StatusDot status={run.state} />
      </Paper>

      {/* S24: the live steering composer, Stop, and Take over. */}
      <InterventionBar run={run} messages={detailQuery.data.messages} />
    </Box>
  );
}

/** The §5.9 loop chain panel: why this run was stopped, as the cycle itself. */
function LoopChainPanel({ projectKey, runId }: { projectKey: string; runId: string }) {
  const chain = useRunChainQuery(runId);
  return (
    <Paper component="section" variant="outlined" aria-label="Loop chain" sx={{ p: 1 }}>
      <Typography variant="h2" sx={{ mb: 1 }}>
        Loop stopped — the causal chain
      </Typography>
      {chain.isPending ? (
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          Loading chain…
        </Typography>
      ) : chain.isError || chain.data === undefined ? (
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          The chain failed to load.
        </Typography>
      ) : (
        <LoopChain chain={chain.data.chain} projectKey={projectKey} />
      )}
    </Paper>
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
  const rowSx = {
    height: ROW_HEIGHT,
    gap: 1,
    px: 1,
    py: 0,
    fontSize: "var(--fs-body)",
    whiteSpace: "nowrap" as const,
  };
  if (row.kind === "group") {
    return (
      <ListItemButton
        dense
        onClick={() => onToggle(row.firstSeq)}
        aria-expanded={row.expanded}
        sx={rowSx}
      >
        <Box component="span" aria-hidden="true" sx={{ color: row.ok ? "success.main" : "error.main" }}>
          {row.ok ? "✓" : "✕"}
        </Box>
        <Typography component="span" variant="body1" noWrap sx={{ flex: 1, minWidth: 0 }}>
          {row.label} <span aria-hidden="true">{row.expanded ? "▾" : "▸"}</span>
        </Typography>
        {row.costCents > 0 && <CostChip usd={row.costCents / 100} />}
        <Typography
          component="span"
          variant="body2"
          sx={{ fontFamily: "var(--font-mono)", color: "text.disabled", minWidth: 72, textAlign: "right" }}
        >
          {row.durationMs !== null ? formatDuration(row.durationMs) : ""}
        </Typography>
      </ListItemButton>
    );
  }
  const a = row.activity;
  const { glyph, tone } = stepGlyph(a);
  return (
    <ListItemButton
      dense
      selected={a.seq === selectedSeq}
      onClick={() => onSelect(a.seq)}
      sx={{ ...rowSx, pl: row.child ? 3 : 1 }}
    >
      <Box component="span" aria-hidden="true" sx={{ color: TONE[tone] ?? "text.disabled" }}>
        {glyph}
      </Box>
      <Typography component="span" variant="body1" noWrap sx={{ flex: 1, minWidth: 0 }}>
        {a.title}
      </Typography>
      {a.attempt > 1 && <Chip size="small" variant="outlined" label={`×${a.attempt}`} />}
      {a.cost_cents > 0 && (
        <CostChip
          usd={a.cost_cents / 100}
          split={{
            inputTokens: a.tokens_in,
            outputTokens: a.tokens_out,
            cacheReadTokens: a.tokens_cache_read,
          }}
        />
      )}
      <TimingGutter a={a} />
    </ListItemButton>
  );
}

/** The right pane: run_context_items with each reason verbatim (§11), the shared
 * ContextMeter (S34 — same component as the wiki tree and the Agents tab), the token
 * split, the cost with the hover breakdown, and the run's outputs. */
function ContextPane({
  run,
  projectKey,
  context,
  outputs,
}: {
  run: Run;
  projectKey: string;
  context: RunContextItem[];
  outputs: RunOutput[];
}) {
  const budget = useContextBudgetQuery(projectKey);
  // The effective diff-size warning threshold (S37): project override or workspace default.
  const project = useProjectQuery(projectKey);
  const prSizeThreshold = project.data?.settings.pr_size_warning_lines.value ?? 0;
  const pr = outputs.find((o) => o.kind === "pull_request");
  const branch =
    outputs.find((o) => o.kind === "branch") ?? outputs.find((o) => o.kind === "partial_work");
  return (
    <Paper
      component="aside"
      variant="outlined"
      aria-label="Context and cost"
      sx={{ p: 1, display: "grid", gap: 1, minWidth: 0 }}
    >
      <Typography variant="h2">Loaded context</Typography>
      {budget.data !== undefined && (
        <ContextMeter
          alwaysTokens={budget.data.always_tokens}
          thresholdTokens={budget.data.threshold_tokens}
          pageCount={budget.data.pages.length}
        />
      )}
      {context.length === 0 ? (
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          Nothing beyond the ticket itself was loaded. To give the next run more to work
          from, scope a{" "}
          <AppLink to="/p/$key/wiki" params={{ key: projectKey }}>
            wiki page
          </AppLink>{" "}
          to <code>always</code> or to the paths this run touches.
        </Typography>
      ) : (
        <List dense disablePadding>
          {context.map((c) => (
            <ListItem
              key={`${c.provider}:${c.source_ref}:${c.position}`}
              disableGutters
              sx={{ display: "grid", gap: 0, py: "2px" }}
            >
              <Typography variant="body1">▸ {c.title}</Typography>
              <Typography variant="body2" sx={{ color: "text.secondary" }}>
                {c.reason}
              </Typography>
              <Typography
                variant="body2"
                sx={{ fontFamily: "var(--font-mono)", color: "text.disabled" }}
              >
                {formatTokenCount(c.tokens)} tok
              </Typography>
            </ListItem>
          ))}
        </List>
      )}
      <Divider />
      <Box sx={{ display: "grid", gap: "2px" }}>
        <Box sx={{ display: "flex", justifyContent: "space-between" }}>
          <Typography variant="body1">Tokens</Typography>
          <Typography variant="body1" sx={{ fontFamily: "var(--font-mono)" }}>
            {formatTokenCount(run.tokens_in + run.tokens_out)}
          </Typography>
        </Box>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          in {formatTokenCount(run.tokens_in)} · out {formatTokenCount(run.tokens_out)} · cache{" "}
          {formatTokenCount(run.tokens_cache_read)}
        </Typography>
        <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
          <Typography variant="body1">Cost</Typography>
          <CostChip
            usd={run.cost_cents > 0 ? run.cost_cents / 100 : null}
            split={{
              inputTokens: run.tokens_in,
              outputTokens: run.tokens_out,
              cacheReadTokens: run.tokens_cache_read,
            }}
          />
        </Box>
      </Box>
      {(branch !== undefined || pr !== undefined || outputs.length > 0) && (
        <>
          <Divider />
          <Typography variant="h2">Outputs</Typography>
          <List dense disablePadding>
            {outputs.map((o) => (
              <ListItem key={o.id} disableGutters sx={{ display: "grid", gap: 0, py: "2px" }}>
                <Typography variant="body2" sx={{ color: "text.secondary" }}>
                  {o.kind.replace("_", " ")}
                </Typography>
                {o.url !== "" ? (
                  <Link
                    href={o.url}
                    target="_blank"
                    rel="noreferrer"
                    sx={{ fontFamily: "var(--font-mono)" }}
                  >
                    {o.ref}
                  </Link>
                ) : (
                  <Box component="code" sx={{ fontFamily: "var(--font-mono)" }}>
                    {o.ref}
                  </Box>
                )}
                {o.summary !== "" && <Typography variant="body1">{o.summary}</Typography>}
                {o.kind === "pull_request" &&
                  o.additions !== undefined &&
                  o.deletions !== undefined && (
                    <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                      <Typography
                        variant="body2"
                        sx={{ fontFamily: "var(--font-mono)", color: "text.secondary" }}
                      >
                        {formatDiffStat(o.additions, o.deletions)}
                      </Typography>
                      {isLargeDiff(o.additions, o.deletions, prSizeThreshold) && (
                        <Tooltip
                          title={`Above the ${prSizeThreshold}-line warning threshold (project settings)`}
                        >
                          <Chip
                            size="small"
                            variant="outlined"
                            color="warning"
                            label="⚠ large diff"
                          />
                        </Tooltip>
                      )}
                    </Box>
                  )}
              </ListItem>
            ))}
          </List>
        </>
      )}
      <Divider />
      <Box sx={{ display: "flex", justifyContent: "space-between", gap: 1 }}>
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          queued {formatRelativeTime(run.queued_at)}
        </Typography>
        {run.branch !== null && (
          <Box component="code" sx={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-mono)" }}>
            {run.branch}
          </Box>
        )}
      </Box>
    </Paper>
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
