/*
 * Run detail (UI spec §5.7) — the LEXI-13 proof-of-concept conversion to MUI.
 *
 * Why this screen: it is the one the library evaluation had to answer for. Three panes, a
 * virtualised timeline of hundreds of rows, live SSE updates, tool-aware renderings, an
 * inline approval surface, and the full §4 status vocabulary. If a component library can
 * carry this screen it can carry the other twenty.
 *
 * What changed beyond the framework swap (this is a redesign, not a re-skin):
 * - Every action now has a visible, labelled control. The `f` "next failure" chord is gone,
 *   replaced by a **Next failure** button that says how many failures there are. The
 *   permalink — previously "selection state lives in the URL", discoverable only if someone
 *   told you — is now a **Copy link to step** button. The Context & cost pane had a toggle
 *   only below 1400px; it now has one at every width.
 * - The verbosity radio group is labelled **Detail level** in words instead of three bare
 *   buttons, and sits in the toolbar with the other view controls rather than hiding under
 *   the timeline.
 * - Stop no longer swaps the toolbar into an inline confirm; it opens a real Dialog that
 *   states what stopping does.
 *
 * Composition notes (LEXI-13 acceptance: no invented components). Everything here is a MUI
 * component or one of four documented exceptions:
 *   1. `VirtualList` — windowing is a scrolling strategy, not a component. MUI ships no
 *      virtualiser and its own docs point at react-window / react-virtuoso; the in-repo
 *      112-line fixed-height windower predates this work, has its own test, and costs no
 *      dependency. Its rows are MUI `ListItemButton`s.
 *   2. `StatusDot` — the §4 status vocabulary (architecture §13: the ONE place a status
 *      becomes a colour and a glyph). Product semantics, not chrome; it is what guarantees
 *      colour is never the only carrier.
 *   3. `CostChip` / `ContextMeter` / `LoopChain` — shared with screens this run does not
 *      convert. Converting them would push MUI into unconverted screens; they convert with
 *      their own screens (see plan/06-ui-redesign-plan.md, stages 3 and 5).
 *   4. The three-pane frame is `Box` with CSS grid — MUI's documented layout primitive.
 *      MUI ships no split-pane.
 */
import Alert from "@mui/material/Alert";
import AlertTitle from "@mui/material/AlertTitle";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Chip from "@mui/material/Chip";
import Divider from "@mui/material/Divider";
import MuiLink from "@mui/material/Link";
import List from "@mui/material/List";
import ListItem from "@mui/material/ListItem";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import ToggleButton from "@mui/material/ToggleButton";
import ToggleButtonGroup from "@mui/material/ToggleButtonGroup";
import Tooltip from "@mui/material/Tooltip";
import Typography from "@mui/material/Typography";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";

import { ContextMeter } from "../../../components/ContextMeter/ContextMeter";
import { CostChip } from "../../../components/CostChip/CostChip";
import { LoopChain } from "../../../components/LoopChain/LoopChain";
import { RouterLink } from "../../../components/RouterLink/RouterLink";
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
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { useMediaQuery } from "../../../lib/useMediaQuery";
import { MuiThemeProvider } from "../../../styles/MuiThemeProvider";
import { MONO_FONT } from "../../../styles/muiTheme";
import { runAnnouncement, type AnnounceSnapshot } from "./announce";
import { ActivityDetail } from "./renderers";
import { InterventionBar } from "./Intervention";
import {
  buildTimeline,
  defaultSelection,
  failureSeqs,
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

/** §10: every step glyph carries its own shape as well as its hue. */
function stepGlyph(a: RunActivity): { glyph: string; token: string } {
  if (a.type === "provision") {
    const state = (a.payload as { state?: string } | null)?.state;
    if (state === "ok") return { glyph: "✓", token: "ok" };
    if (state === "failed") return { glyph: "✕", token: "fail" };
    if (state === "running") return { glyph: "●", token: "running" };
    return { glyph: "○", token: "muted" };
  }
  if (a.ok === false) return { glyph: "✕", token: "fail" };
  if (a.type === "action") {
    return a.ok === true ? { glyph: "✓", token: "ok" } : { glyph: "●", token: "running" };
  }
  if (a.type === "elicitation") return { glyph: "▲", token: "needs-you" };
  if (a.type === "error") return { glyph: "✕", token: "fail" };
  return { glyph: GLYPHS[a.type], token: "muted" };
}

/** The §5.7 timing gutter: a right-aligned duration with a queued/model/tool split bar. */
function TimingGutter({ a }: { a: RunActivity }) {
  const split = timingSplit(a);
  if (split === null) return <Box sx={{ width: 64, flexShrink: 0 }} />;
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
        sx={{
          width: 64,
          flexShrink: 0,
          display: "flex",
          alignItems: "center",
          justifyContent: "flex-end",
          gap: 0.5,
        }}
      >
        {known > 0 && (
          <Box
            aria-hidden="true"
            sx={{ display: "flex", width: 18, height: 3, borderRadius: 1, overflow: "hidden" }}
          >
            <Box sx={{ width: pct(split.queuedMs), bgcolor: "var(--muted)" }} />
            <Box sx={{ width: pct(split.modelMs), bgcolor: "var(--accent)" }} />
            <Box sx={{ width: pct(split.toolMs), bgcolor: "var(--running)" }} />
          </Box>
        )}
        <Typography component="span" variant="caption" sx={{ fontFamily: MONO_FONT }}>
          {formatDuration(split.totalMs)}
        </Typography>
      </Box>
    </Tooltip>
  );
}

export function RunDetailPage() {
  return (
    <MuiThemeProvider>
      <RunDetail />
    </MuiThemeProvider>
  );
}

function RunDetail() {
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
    void navigate({ search: (prev) => ({ ...prev, level: v }), replace: true });
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

  // Jump the window to the selection when the selection changes (permalink open, the
  // Next failure button) — but not on every streamed append.
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

  // The `f` chord is gone (LEXI-13): jumping to the next failure is a labelled button that
  // states how many failures there are, so the affordance is visible without a cheatsheet.
  const failures = useMemo(() => failureSeqs(activities), [activities]);
  const goToNextFailure = () => {
    const seq = nextFailure(activities, selectedSeq);
    if (seq !== null) selectStep(seq);
  };

  const [copied, setCopied] = useState(false);
  const copyStepLink = () => {
    void navigator.clipboard.writeText(window.location.href).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  const live = run !== undefined && !isTerminal(run.state);
  const now = useNow(live);

  // §10 responsive: >=1400 all three panes by default; below that the context pane starts
  // closed. Either way the toggle is visible at every width — it used to appear only under
  // 1400px, which made the pane undiscoverable on a wide screen once it was closed.
  const wide = useMediaQuery("(min-width: 1400px)", true);
  const [contextOpen, setContextOpen] = useState<boolean | null>(null);
  const contextShown = contextOpen ?? wide;

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
  const [showFirstCompleted] = useState(() => momentPending("first-completed-run", key));
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
      <Typography color="text.secondary" sx={{ p: 2 }}>
        Loading run…
      </Typography>
    );
  }
  if (detailQuery.isError || run === undefined) {
    return (
      <Alert severity="error" sx={{ m: 2 }}>
        <AlertTitle>This run failed to load</AlertTitle>
        The run may have been deleted, or the server is unreachable.{" "}
        <MuiLink component={RouterLink} to="/p/$key/runs" params={{ key }}>
          Back to all runs
        </MuiLink>
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
    <Box
      sx={{
        display: "flex",
        flexDirection: "column",
        height: "100%",
        minHeight: 0,
        gap: 1,
        p: 1,
      }}
    >
      {/* ---- header ------------------------------------------------------------------ */}
      <Paper component="header" sx={{ px: 1.5, py: 1 }}>
        <Stack direction="row" spacing={1.5} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }}>
          <Button component={RouterLink} to="/p/$key/runs" params={{ key }} size="small">
            ← All runs
          </Button>
          <Typography variant="h1" component="h1">
            Run #{run.seq}
          </Typography>
          <StatusDot status={run.state} />
          <Chip variant="outlined" label={agentName} />
          <Chip variant="outlined" label={run.model} />
          {elapsed !== null && (
            <Typography variant="body2" color="text.secondary" sx={{ fontFamily: MONO_FONT }}>
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
            <Typography variant="body2" color="warning.main">
              {run.hold_reason}
            </Typography>
          )}
        </Stack>

        <Divider sx={{ my: 1 }} />

        {/* The view controls, all visible and all labelled. */}
        <Stack direction="row" spacing={1.5} useFlexGap sx={{ alignItems: "center", flexWrap: "wrap" }}>
          <Stack direction="row" spacing={0.75} sx={{ alignItems: "center" }}>
            <Typography variant="body2" component="label" id="detail-level-label">
              Detail level
            </Typography>
            <ToggleButtonGroup
              size="small"
              exclusive
              value={verbosity}
              aria-labelledby="detail-level-label"
              onChange={(_e, v: Verbosity | null) => {
                if (v !== null) setVerbosity(v);
              }}
            >
              <ToggleButton value="summary">Summary</ToggleButton>
              <ToggleButton value="normal">Normal</ToggleButton>
              <ToggleButton value="verbose">Verbose</ToggleButton>
            </ToggleButtonGroup>
          </Stack>

          <Divider orientation="vertical" flexItem />

          <Button
            onClick={goToNextFailure}
            disabled={failures.length === 0}
            title={
              failures.length === 0
                ? "This run has no failed steps"
                : "Select the next failed step"
            }
          >
            ✕ Next failure{failures.length > 0 ? ` (${failures.length})` : ""}
          </Button>

          <Button onClick={copyStepLink} disabled={selectedSeq === undefined}>
            {copied ? "✓ Link copied" : "Copy link to step"}
          </Button>

          <Box sx={{ flexGrow: 1 }} />

          <ToggleButton
            size="small"
            value="context"
            selected={contextShown}
            onChange={() => setContextOpen(!contextShown)}
          >
            Context &amp; cost
          </ToggleButton>
        </Stack>
      </Paper>

      {/* §10: state transitions and step boundaries only — never the log stream. */}
      <Box
        aria-live="polite"
        role="status"
        sx={{
          position: "absolute",
          width: 1,
          height: 1,
          overflow: "hidden",
          clip: "rect(0 0 0 0)",
          whiteSpace: "nowrap",
        }}
      >
        {announced}
      </Box>

      {/* §8: the first completed run is the activation event — mark it, teach the next
          action. Restrained: one alert, shown exactly once per project. */}
      {firstCompletedMoment && (
        <Alert severity="success" icon={<span aria-hidden="true">✓</span>}>
          <AlertTitle>Your first completed run</AlertTitle>
          Next:{" "}
          {(() => {
            const pr = detailQuery.data.outputs.find((o) => o.kind === "pull_request");
            const out = pr ?? detailQuery.data.outputs.find((o) => o.url !== "");
            return out !== undefined && out.url !== "" ? (
              <MuiLink href={out.url} target="_blank" rel="noreferrer">
                review the diff
              </MuiLink>
            ) : (
              <>review the output in the Context &amp; cost pane</>
            );
          })()}
          , or turn the feedback into a{" "}
          <MuiLink component={RouterLink} to="/p/$key/wiki" params={{ key }}>
            wiki page
          </MuiLink>{" "}
          so the next run starts smarter.
        </Alert>
      )}

      {/* §8: the first `needs input` teaches that agents are interactive — unmissable. */}
      {firstNeedsInputMoment && (
        <Alert severity="warning" icon={<span aria-hidden="true">▲</span>}>
          <AlertTitle>{agentName} is asking you a question</AlertTitle>
          Agents aren&apos;t fire-and-forget: this run is paused until you answer, right here
          in the step detail below. Your answer goes straight back into the running session.
        </Alert>
      )}

      {/* S29: a loop-stopped run leads with the cycle it built — the §5.9 chain view. */}
      {run.state === "loop_stopped" && <LoopChainPanel projectKey={key} runId={run.id} />}

      {/* ---- the three panes ---------------------------------------------------------- */}
      <Box
        sx={{
          flex: "1 1 auto",
          minHeight: 0,
          display: "grid",
          gap: 1,
          // §10's breakpoints, literally: ≥1400 is the full three-pane; 1100–1400 keeps two
          // columns and the context pane spans the row beneath them; <1100 stacks. The 1400
          // value is a raw query rather than MUI's `lg` (1200px) because the spec names it.
          gridTemplateColumns: { xs: "1fr", md: "300px minmax(0, 1fr)" },
          "@media (min-width: 1400px)": {
            gridTemplateColumns: contextShown
              ? "300px minmax(0, 1fr) 320px"
              : "300px minmax(0, 1fr)",
          },
        }}
      >
        {/* Left — the step timeline, windowed. */}
        <Paper sx={{ display: "flex", flexDirection: "column", minHeight: 0, minWidth: 0 }}>
          <Typography variant="h2" component="h2" sx={{ px: 1.5, py: 1 }}>
            Steps{rows.length > 0 ? ` (${rows.length})` : ""}
          </Typography>
          <Divider />
          {rows.length === 0 ? (
            <Typography color="text.secondary" sx={{ p: 1.5 }} variant="body2">
              No steps yet. They appear here as the agent works — usually within a few
              seconds of the container starting.
            </Typography>
          ) : (
            // The windower measures its own element, so it needs a bounded box to live in;
            // `& > *` gives its scroll container the pane's remaining height.
            <Box sx={{ flex: "1 1 auto", minHeight: 0, "& > *": { height: "100%" } }}>
              <VirtualList
                items={rows}
                rowHeight={ROW_HEIGHT}
                itemKey={rowKey}
                scrollToIndex={jump}
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
            </Box>
          )}
        </Paper>

        {/* Centre — the tool-aware detail of the selected step. */}
        <Paper
          component="main"
          aria-label="Step detail"
          sx={{ minHeight: 0, minWidth: 0, overflow: "auto", p: 1.5 }}
        >
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
          ) : activities.length === 0 ? (
            <Stack spacing={1} sx={{ alignItems: "flex-start" }}>
              <Typography variant="h2" component="h2">
                Nothing has happened yet
              </Typography>
              <Typography variant="body2" color="text.secondary">
                Steps stream in here as the agent works. If this run stays empty for more
                than a few seconds, its container is probably still starting.
              </Typography>
              <Button variant="outlined" component={RouterLink} to="/p/$key/runs" params={{ key }}>
                See all runs
              </Button>
            </Stack>
          ) : (
            <Stack spacing={1} sx={{ alignItems: "flex-start" }}>
              <Typography variant="h2" component="h2">
                Pick a step to inspect it
              </Typography>
              <Typography variant="body2" color="text.secondary">
                Choose any row in the Steps list on the left. Failed steps open expanded, and
                the <strong>Next failure</strong> button above jumps straight to one.
              </Typography>
            </Stack>
          )}
        </Paper>

        {/* Right — Context & cost. */}
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
      {stalled ? (
        <Alert severity="warning" icon={<span aria-hidden="true">▲</span>}>
          Still waiting on the model — no activity{" "}
          {Math.floor((now - new Date(run.started_at as string).getTime()) / 1000)}s after
          start.
        </Alert>
      ) : (
        <Paper
          component="footer"
          sx={{ px: 1.5, py: 0.75, display: "flex", alignItems: "center", gap: 1 }}
        >
          <Typography variant="body2" sx={{ flexGrow: 1 }}>
            {isTerminal(run.state)
              ? outcomeSummary(run, activities)
              : `Step ${run.step_count > 0 ? run.step_count : "–"} — ${
                  run.current_step !== "" ? run.current_step : "starting up"
                }`}
          </Typography>
          <StatusDot status={run.state} />
        </Paper>
      )}

      {/* S24: the live steering composer, Stop, and Take over. */}
      <InterventionBar run={run} messages={detailQuery.data.messages} />
    </Box>
  );
}

/** The §5.9 loop chain panel: why this run was stopped, as the cycle itself. */
function LoopChainPanel({ projectKey, runId }: { projectKey: string; runId: string }) {
  const chain = useRunChainQuery(runId);
  return (
    <Alert severity="warning" icon={<span aria-hidden="true">⊗</span>}>
      <AlertTitle>Loop stopped — the causal chain</AlertTitle>
      {chain.isPending ? (
        <Typography variant="body2" color="text.secondary">
          Loading chain…
        </Typography>
      ) : chain.isError || chain.data === undefined ? (
        <Typography variant="body2" color="text.secondary">
          The chain failed to load.
        </Typography>
      ) : (
        <LoopChain chain={chain.data.chain} projectKey={projectKey} />
      )}
    </Alert>
  );
}

function rowKey(row: TimelineRow): string {
  return row.kind === "group"
    ? `g${row.firstSeq}`
    : `s${row.activity.seq}${row.child ? "c" : ""}`;
}

/** One windowed timeline row: a MUI ListItemButton sized to the fixed row height. */
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
    minHeight: ROW_HEIGHT,
    gap: 0.75,
    px: 1,
    py: 0,
    fontSize: 12,
  } as const;

  if (row.kind === "group") {
    return (
      <ListItemButton
        sx={rowSx}
        onClick={() => onToggle(row.firstSeq)}
        aria-expanded={row.expanded}
      >
        <Box
          component="span"
          aria-hidden="true"
          sx={{ color: row.ok ? "var(--ok)" : "var(--fail)", width: 12, flexShrink: 0 }}
        >
          {row.ok ? "✓" : "✕"}
        </Box>
        <ListItemText
          primary={`${row.label} ${row.expanded ? "▾" : "▸"}`}
          slotProps={{
            primary: { variant: "body2", noWrap: true, sx: { fontSize: 12 } },
          }}
        />
        {row.costCents > 0 && <CostChip usd={row.costCents / 100} />}
        <Typography component="span" variant="caption" sx={{ fontFamily: MONO_FONT }}>
          {row.durationMs !== null ? formatDuration(row.durationMs) : ""}
        </Typography>
      </ListItemButton>
    );
  }

  const a = row.activity;
  const { glyph, token } = stepGlyph(a);
  return (
    <ListItemButton
      sx={{ ...rowSx, pl: row.child ? 3 : 1 }}
      selected={a.seq === selectedSeq}
      onClick={() => onSelect(a.seq)}
    >
      <Box
        component="span"
        aria-hidden="true"
        sx={{ color: `var(--${token})`, width: 12, flexShrink: 0 }}
      >
        {glyph}
      </Box>
      <ListItemText
        primary={a.title}
        slotProps={{ primary: { variant: "body2", noWrap: true, sx: { fontSize: 12 } } }}
      />
      {a.attempt > 1 && <Chip label={`×${a.attempt}`} size="small" color="warning" />}
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

/**
 * The right pane: run_context_items with each reason verbatim (§11), the shared
 * ContextMeter, the token split, the cost with its hover breakdown, and the run's outputs.
 */
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

  return (
    <Paper
      component="aside"
      aria-label="Context and cost"
      sx={{
        minHeight: 0,
        minWidth: 0,
        overflow: "auto",
        p: 1.5,
        // Below 1400 there is no third column, so the pane spans the full width of the row
        // beneath the other two rather than being squeezed into the timeline's 300px.
        gridColumn: "1 / -1",
        "@media (min-width: 1400px)": { gridColumn: "auto" },
      }}
    >
      <Typography variant="h2" component="h2" gutterBottom>
        Loaded context
      </Typography>
      {budget.data !== undefined && (
        <ContextMeter
          alwaysTokens={budget.data.always_tokens}
          thresholdTokens={budget.data.threshold_tokens}
          pageCount={budget.data.pages.length}
        />
      )}
      {context.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          Nothing beyond the ticket itself. Wiki pages scoped <code>always</code> — or
          matching this run&apos;s paths — would be listed here.
        </Typography>
      ) : (
        <List dense disablePadding>
          {context.map((c) => (
            <ListItem
              key={`${c.provider}:${c.source_ref}:${c.position}`}
              disableGutters
              sx={{ display: "block", py: 0.5 }}
            >
              <Typography variant="body2">▸ {c.title}</Typography>
              <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                {c.reason}
              </Typography>
              <Typography variant="caption" sx={{ fontFamily: MONO_FONT }}>
                {formatTokenCount(c.tokens)} tok
              </Typography>
            </ListItem>
          ))}
        </List>
      )}

      <Divider sx={{ my: 1.5 }} />

      <Stack direction="row" sx={{ justifyContent: "space-between", alignItems: "center" }}>
        <Typography variant="body2">Tokens</Typography>
        <Typography variant="body2" sx={{ fontFamily: MONO_FONT }}>
          {formatTokenCount(run.tokens_in + run.tokens_out)}
        </Typography>
      </Stack>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
        in {formatTokenCount(run.tokens_in)} · out {formatTokenCount(run.tokens_out)} · cache{" "}
        {formatTokenCount(run.tokens_cache_read)}
      </Typography>
      <Stack direction="row" sx={{ justifyContent: "space-between", alignItems: "center", mt: 0.5 }}>
        <Typography variant="body2">Cost</Typography>
        <CostChip
          usd={run.cost_cents > 0 ? run.cost_cents / 100 : null}
          split={{
            inputTokens: run.tokens_in,
            outputTokens: run.tokens_out,
            cacheReadTokens: run.tokens_cache_read,
          }}
        />
      </Stack>

      <Divider sx={{ my: 1.5 }} />

      <Typography variant="h2" component="h2" gutterBottom>
        Outputs
      </Typography>
      {outputs.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          Nothing pushed yet. A branch appears here as soon as the agent commits, even if the
          run later fails.
        </Typography>
      ) : (
        <List dense disablePadding>
          {outputs.map((o) => {
            const large =
              o.kind === "pull_request" &&
              o.additions !== undefined &&
              o.deletions !== undefined &&
              isLargeDiff(o.additions, o.deletions, prSizeThreshold);
            return (
              <ListItem key={o.id} disableGutters sx={{ display: "block", py: 0.5 }}>
                <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                  {o.kind.replace("_", " ")}
                </Typography>
                {o.url !== "" ? (
                  <MuiLink
                    href={o.url}
                    target="_blank"
                    rel="noreferrer"
                    sx={{ fontFamily: MONO_FONT, fontSize: 12 }}
                  >
                    {o.ref}
                  </MuiLink>
                ) : (
                  <Typography component="code" sx={{ fontFamily: MONO_FONT, fontSize: 12 }}>
                    {o.ref}
                  </Typography>
                )}
                {o.summary !== "" && (
                  <Typography variant="caption" sx={{ display: "block" }}>
                    {o.summary}
                  </Typography>
                )}
                {o.kind === "pull_request" &&
                  o.additions !== undefined &&
                  o.deletions !== undefined && (
                    <Stack direction="row" spacing={0.5} sx={{ alignItems: "center" }}>
                      <Typography
                        variant="caption"
                        sx={{ fontFamily: MONO_FONT }}
                        color={large ? "warning.main" : "text.secondary"}
                      >
                        {formatDiffStat(o.additions, o.deletions)}
                      </Typography>
                      {large && (
                        <Chip
                          size="small"
                          color="warning"
                          variant="outlined"
                          label="⚠ large diff"
                          title={`Above the ${prSizeThreshold}-line warning threshold (project settings)`}
                        />
                      )}
                    </Stack>
                  )}
              </ListItem>
            );
          })}
        </List>
      )}

      <Divider sx={{ my: 1.5 }} />
      <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
        queued {formatRelativeTime(run.queued_at)}
      </Typography>
      {run.branch !== null && (
        <Typography component="code" sx={{ fontFamily: MONO_FONT, fontSize: 12 }}>
          {run.branch}
        </Typography>
      )}
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
