/*
 * THE cache-application reducer (architecture §13): the single place where an SSE frame
 * touches the TanStack Query cache. "A run activity arrived" becomes "the timeline
 * re-renders" here and nowhere else — no component ever applies a frame itself.
 *
 * S07 establishes the funnel; the cases fill in as the stories that own each screen land
 * (runs S21+, board S13+, inbox S28…). Until a screen exists, invalidation is a no-op
 * because nothing holds the query key.
 */
import type { QueryClient } from "@tanstack/react-query";

import type { components } from "../api/types.gen";

export type StreamEventType = components["schemas"]["StreamEventType"];
export type StreamFrame = components["schemas"]["StreamFrame"];

type RunActivity = components["schemas"]["RunActivity"];
type RunDetailResponse = components["schemas"]["RunDetailResponse"];
type RunActivitiesResponse = components["schemas"]["RunActivitiesResponse"];

/**
 * Merge one streamed activity into the cached transcript (S23). Same-Seq merge semantics
 * per contracts §2.4: a re-emitted Seq REPLACES its row (that is how a tool_result completes
 * its originating tool_use); a new Seq is inserted in seq order.
 */
export function mergeActivity(
  cached: RunActivitiesResponse,
  activity: RunActivity,
): RunActivitiesResponse {
  const activities = cached.activities;
  const i = activities.findIndex((a) => a.seq === activity.seq);
  if (i !== -1) {
    const next = activities.slice();
    next[i] = activity;
    return { activities: next };
  }
  // The common case is strictly-increasing seq → plain append.
  if (activities.length === 0 || activities[activities.length - 1].seq < activity.seq) {
    return { activities: [...activities, activity] };
  }
  const next = activities.slice();
  const at = next.findIndex((a) => a.seq > activity.seq);
  next.splice(at, 0, activity);
  return { activities: next };
}

export function applyStreamEvent(
  qc: QueryClient,
  type: StreamEventType,
  frame: StreamFrame,
): void {
  const [, id] = splitTopic(frame.topic);

  switch (type) {
    // The hot path (architecture §13): a full activity row rides the frame, and appending
    // it beats a refetch mid-run. No cached transcript → nothing to do; the query fetches
    // fresh on mount. A malformed frame falls back to invalidation.
    case "run.activity": {
      const activity = (frame.payload as { activity?: RunActivity } | null)?.activity;
      if (!activity || typeof activity.seq !== "number") {
        void qc.invalidateQueries({ queryKey: ["run", id] });
        break;
      }
      qc.setQueryData<RunActivitiesResponse>(["run", id, "activities"], (old) =>
        old === undefined ? undefined : mergeActivity(old, activity),
      );
      break;
    }
    // The mutable one-liner (§5.7's current-step line): patch it in place.
    case "run.step": {
      const step = (frame.payload as { step?: string } | null)?.step;
      if (typeof step !== "string") break;
      qc.setQueryData<RunDetailResponse>(["run", id, "detail"], (old) =>
        old === undefined ? undefined : { ...old, run: { ...old.run, current_step: step } },
      );
      break;
    }
    // A state edge changes the run row (and, on terminal states, its outputs), and it can
    // change what every run list shows — refetch both families.
    case "run.state":
      void qc.invalidateQueries({ queryKey: ["run", id] });
      void qc.invalidateQueries({ queryKey: ["runs"] });
      break;
    // Usage frames carry a delta; the run row carries the rollup — refetch the row rather
    // than double-count a replayed delta.
    case "run.usage":
      void qc.invalidateQueries({ queryKey: ["run", id, "detail"] });
      break;
    // The elicitation's activity row is appended MCP-side without a run.activity frame, so
    // the transcript must refetch; the run row's state edge arrives as run.state.
    case "run.elicitation":
      void qc.invalidateQueries({ queryKey: ["run", id] });
      break;
    // Ticket events arrive on the project topic ("project:PAY" → id "PAY"). Every one of
    // them can change what the board shows (S11), so they invalidate the ["board", key]
    // family alongside the ticket cache.
    case "ticket.created":
    case "ticket.updated":
    case "ticket.commented":
    case "ticket.moved":
    case "ticket.archived":
    case "ticket.unarchived":
      void qc.invalidateQueries({ queryKey: ["ticket", id] });
      void qc.invalidateQueries({ queryKey: ["board", id] });
      break;
    // Agent events arrive on the project topic: the roster, the detail screen (via the list's
    // consumers) and every delegate picker read the ["agents", key] family.
    case "agent.created":
    case "agent.updated":
    case "agent.archived":
      void qc.invalidateQueries({ queryKey: ["agents", id] });
      break;
    // Label CRUD changes chip names/colors on cards and the filter menu.
    case "label.created":
    case "label.updated":
    case "label.deleted":
      void qc.invalidateQueries({ queryKey: ["board", id] });
      break;
    // Column changes (S09 settings in another tab): the board reads columns through the S09
    // query key, so both families refetch.
    case "board.updated":
      void qc.invalidateQueries({ queryKey: ["board", id] });
      void qc.invalidateQueries({ queryKey: ["columns", "list", id] });
      break;
    case "triage.created":
      void qc.invalidateQueries({ queryKey: ["triage", id] });
      break;
    case "trigger.fired":
      void qc.invalidateQueries({ queryKey: ["trigger", id] });
      break;
    case "notification.updated":
      void qc.invalidateQueries({ queryKey: ["inbox"] });
      void qc.invalidateQueries({ queryKey: ["notifications"] });
      break;
    case "wiki.proposed":
      void qc.invalidateQueries({ queryKey: ["wiki", id] });
      break;
    // Provisioning steps are activity rows updated in place (§10.3) but their frame carries
    // no seq — refetch the transcript the checklist renders from.
    case "provision.step":
      void qc.invalidateQueries({ queryKey: ["run", id, "activities"] });
      break;
    case "module.degraded":
      void qc.invalidateQueries({ queryKey: ["system", "modules"] });
      break;
  }
}

function splitTopic(topic: string): [string, string] {
  const i = topic.indexOf(":");
  return i === -1 ? [topic, ""] : [topic.slice(0, i), topic.slice(i + 1)];
}
