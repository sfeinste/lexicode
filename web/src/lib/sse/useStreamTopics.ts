import { useEffect } from "react";

import { queryClient } from "../api/queryClient";
import { StreamManager } from "./stream";

/** The tab's one StreamManager, bound to the app's QueryClient. */
export const streamManager = new StreamManager(queryClient);

/**
 * Declare the SSE topics this component needs while mounted, e.g.
 * `useStreamTopics(["project:PAY", `run:${id}`])`. The manager keeps one EventSource for
 * the union of every mounted component's topics; changing the set reconnects (see stream.ts).
 */
export function useStreamTopics(topics: string[]): void {
  const key = topics.join(",");
  useEffect(() => {
    if (key === "") return;
    return streamManager.acquire(key.split(","));
  }, [key]);
}
