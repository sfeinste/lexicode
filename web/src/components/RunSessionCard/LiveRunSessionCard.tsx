/*
 * LiveRunSessionCard (S23): the fixture-driven RunSessionCard from S12, now fed from the
 * real run queries and the run's SSE topic. The ticket stream's kind='run' rows render
 * this; the presentational component underneath is unchanged (same collapsed contract:
 * agent · status · elapsed · cost · current step).
 */
import { useEffect, useState } from "react";

import type { RunActivity } from "../../lib/api/client";
import {
  useRunActivitiesQuery,
  useRunDetailQuery,
} from "../../lib/api/runQueries";
import { useStreamTopics } from "../../lib/sse/useStreamTopics";
import {
  RunSessionCard,
  type RunActivityType,
  type RunSessionActivity,
} from "./RunSessionCard";

const CARD_TYPES: ReadonlySet<string> = new Set([
  "thought",
  "action",
  "elicitation",
  "response",
  "error",
]);

/** The card's inline activity list: the stream's §7 types at Normal verbosity, latest 30. */
function toCardActivities(activities: RunActivity[]): RunSessionActivity[] {
  return activities
    .filter((a) => CARD_TYPES.has(a.type) && a.level <= 1)
    .slice(-30)
    .map((a) => ({
      id: String(a.seq),
      type: a.type as RunActivityType,
      text: a.title,
      at: a.created_at,
    }));
}

export function LiveRunSessionCard({
  projectKey,
  runId,
  agentName,
}: {
  projectKey: string;
  runId: string;
  agentName: (agentId: string) => string;
}) {
  useStreamTopics([`run:${runId}`]);
  const detail = useRunDetailQuery(runId);
  const activities = useRunActivitiesQuery(runId);

  // Tick the elapsed clock while the run is live.
  const run = detail.data?.run;
  const terminal =
    run !== undefined &&
    ["completed", "failed", "timed_out", "canceled", "loop_stopped"].includes(run.state);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (run === undefined || terminal) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [run, terminal]);

  if (run === undefined) return null;

  const start = run.started_at !== null ? new Date(run.started_at).getTime() : null;
  const end = run.ended_at !== null ? new Date(run.ended_at).getTime() : now;
  const currentStep = terminal
    ? run.error_message !== ""
      ? run.error_message
      : run.state_reason
    : run.state === "queued" && run.hold_reason !== ""
      ? run.hold_reason
      : run.current_step;

  return (
    <RunSessionCard
      run={{
        id: run.id,
        agent: agentName(run.agent_id),
        status: run.state,
        elapsedMs: start !== null ? Math.max(0, end - start) : 0,
        costUsd: run.cost_cents > 0 ? run.cost_cents / 100 : null,
        currentStep,
        activities: toCardActivities(activities.data?.activities ?? []),
        runHref: `/p/${projectKey}/runs/${run.id}`,
      }}
    />
  );
}
