/*
 * RunSessionCard (UI spec §5.4/§7) — how a run renders inside a stream: collapsed by
 * default (agent, status via StatusDot, elapsed, cost via CostChip, one-line current step),
 * expanding to the activity list inline with an "Open full run" link to the run detail.
 * One component, three placements (ticket stream, runs list, overview).
 *
 * S12 ships it fixture-driven and covered by its own test; the ticket stream renders it for
 * kind='run' entries, which do not exist until the S23 run lifecycle writes them — nothing
 * fake is wired into the real page.
 */
import { useState } from "react";

import { formatDuration, formatRelativeTime } from "../../lib/format/format";
import { CostChip } from "../CostChip/CostChip";
import { StatusDot, type Status } from "../StatusDot/StatusDot";
import styles from "./RunSessionCard.module.css";

export type RunActivityType = "thought" | "action" | "elicitation" | "response" | "error";

export interface RunSessionActivity {
  id: string;
  type: RunActivityType;
  text: string;
  at?: string;
}

export interface RunSessionData {
  id: string;
  agent: string;
  status: Status;
  elapsedMs: number;
  /** USD estimate; null while unknown. */
  costUsd: number | null;
  /** The run's own mutable one-liner: action + specific item (§5.7). */
  currentStep: string;
  activities: RunSessionActivity[];
  /** Where "Open full run" goes (/p/:key/runs/:id). */
  runHref: string;
}

const ACTIVITY_GLYPHS: Record<RunActivityType, string> = {
  thought: "…",
  action: "$",
  elicitation: "?",
  response: "↩",
  error: "✕",
};

export function RunSessionCard({ run }: { run: RunSessionData }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <section className={styles.root} aria-label={`Run by ${run.agent}`}>
      <button
        type="button"
        className={styles.head}
        aria-expanded={expanded}
        onClick={() => setExpanded((e) => !e)}
      >
        <span className={styles.agent}>{run.agent}</span>
        <StatusDot status={run.status} />
        <span className={styles.elapsed}>{formatDuration(run.elapsedMs)}</span>
        <CostChip usd={run.costUsd} />
        <span className={styles.step}>{run.currentStep}</span>
        <span aria-hidden="true" className={styles.chevron}>
          {expanded ? "▾" : "▸"}
        </span>
      </button>
      {expanded && (
        <div className={styles.body}>
          <ol className={styles.activities}>
            {run.activities.map((a) => (
              <li key={a.id} className={styles.activity} data-type={a.type}>
                <span aria-hidden="true" className={styles.glyph}>
                  {ACTIVITY_GLYPHS[a.type]}
                </span>
                <span className={styles.text}>{a.text}</span>
                {a.at !== undefined && (
                  <span className={styles.time}>{formatRelativeTime(a.at)}</span>
                )}
              </li>
            ))}
          </ol>
          <a className={styles.openFull} href={run.runHref}>
            Open full run →
          </a>
        </div>
      )}
    </section>
  );
}
