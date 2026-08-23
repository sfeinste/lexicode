/*
 * RunNotice — the honest inline answer to "I just started a run" (UI spec §5.3: `D` and the
 * ticket's Run button are the two human ways to start one).
 *
 * Delegating returns a run id and nothing else: the run is QUEUED, not running. Admission
 * control decides when it starts, and a queued run's `hold_reason` says in words what is
 * holding it ("waiting: dev is at its 1-run limit"). So the notice reads the run back and
 * states where it actually is — never a bare "started", never a silent success — and links
 * to the run so the next question has an answer one click away.
 *
 * `runNoticeText` (its own module, pure and separately testable) is the whole vocabulary;
 * this component is the query + the link around it. It subscribes to the run's SSE topic,
 * so a hold that lands a second later rewrites the sentence in place.
 */
import { Link } from "@tanstack/react-router";

import { useRunDetailQuery } from "../../lib/api/runQueries";
import { useStreamTopics } from "../../lib/sse/useStreamTopics";
import { RUN_STATUSES, StatusDot, type Status } from "../StatusDot/StatusDot";
import styles from "./RunNotice.module.css";
import { runNoticeText } from "./runNoticeText";

export function RunNotice({
  projectKey,
  runId,
  agentName,
  ticketKey,
  onDismiss,
}: {
  projectKey: string;
  runId: string;
  agentName: string;
  /** The ticket the run is scoped to, when the surface does not already say. */
  ticketKey?: string;
  onDismiss?: () => void;
}) {
  useStreamTopics([`run:${runId}`]);
  const detail = useRunDetailQuery(runId);
  const run = detail.data?.run;
  const status: Status =
    run !== undefined && run.state in RUN_STATUSES ? (run.state as Status) : "queued";

  return (
    <p role="status" className={styles.root}>
      <StatusDot status={status} label={runNoticeText(run, agentName)} />
      {ticketKey !== undefined && <span className={styles.on}>on {ticketKey}</span>}
      <Link to="/p/$key/runs/$id" params={{ key: projectKey, id: runId }}>
        View run
      </Link>
      {onDismiss !== undefined && (
        <button type="button" className={styles.dismiss} onClick={onDismiss}>
          Dismiss
        </button>
      )}
    </p>
  );
}
