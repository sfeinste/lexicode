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

export function applyStreamEvent(
  qc: QueryClient,
  type: StreamEventType,
  frame: StreamFrame,
): void {
  const [, id] = splitTopic(frame.topic);

  switch (type) {
    case "run.state":
    case "run.activity":
    case "run.step":
    case "run.usage":
    case "run.elicitation":
      // Later stories will setQueryData for the hot paths (appending an activity beats a
      // refetch mid-run); invalidation is the correct conservative default until then.
      void qc.invalidateQueries({ queryKey: ["run", id] });
      break;
    case "ticket.updated":
      void qc.invalidateQueries({ queryKey: ["ticket", id] });
      break;
    case "board.updated":
      void qc.invalidateQueries({ queryKey: ["board", id] });
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
    case "provision.step":
      void qc.invalidateQueries({ queryKey: ["run", id, "provision"] });
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
