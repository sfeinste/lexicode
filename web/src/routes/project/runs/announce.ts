/*
 * The run detail's live-region policy (UI spec §10, S38): announce state transitions and
 * step boundaries ONLY — the log stream must never spam the announcer. This module is the
 * pure decision; RunDetailPage feeds it the run row on every render and puts whatever it
 * returns into one aria-live="polite" region.
 *
 * By construction, streamed log lines cannot reach the announcer: the input is only
 * (state, step_count, current_step), so an activity append that starts no new step and
 * changes no state produces null.
 */
import { STATUS_VOCABULARY } from "../../../components/StatusDot/StatusDot";
import type { RunState } from "../../../lib/api/client";

export interface AnnounceSnapshot {
  state: RunState;
  stepCount: number;
  currentStep: string;
}

/**
 * The announcement a change deserves, or null for "stay quiet". `prev === null` (first
 * observation) is quiet too — announcing the initial render would read the whole screen's
 * state back to someone who just navigated here.
 */
export function runAnnouncement(
  prev: AnnounceSnapshot | null,
  next: AnnounceSnapshot,
): string | null {
  if (prev === null) return null;
  if (prev.state !== next.state) {
    return `Run is now ${STATUS_VOCABULARY[next.state].label.toLowerCase()}`;
  }
  if (prev.stepCount !== next.stepCount && next.stepCount > 0) {
    return next.currentStep !== ""
      ? `Step ${next.stepCount}: ${next.currentStep}`
      : `Step ${next.stepCount}`;
  }
  return null;
}
