/*
 * The sentence a RunNotice shows. Pure: a run row in, one honest line out — kept out of the
 * component so the vocabulary can be tested without rendering anything.
 */
import type { Run } from "../../lib/api/client";
import { RUN_STATUSES } from "../StatusDot/StatusDot";

/**
 * What the run's own row says, in words. Queued with a hold is NORMAL — it reads as
 * "Queued — <reason>", never as a failure; a terminal state reads as itself plus whatever
 * reason the scheduler recorded.
 */
export function runNoticeText(run: Run | undefined, agentName: string): string {
  if (run === undefined) return `Queued a run for ${agentName}`;
  const label = RUN_STATUSES[run.state as keyof typeof RUN_STATUSES]?.label ?? run.state;
  const because = run.state === "queued" ? run.hold_reason : run.error_message || run.state_reason;
  const head = `Run #${run.seq} · ${agentName} · ${label}`;
  return because === "" ? head : `${head} — ${because}`;
}
