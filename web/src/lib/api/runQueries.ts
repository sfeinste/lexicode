/*
 * Shared TanStack Query definitions for runs (S23). Key families match what the SSE
 * reducer touches (applyEvent.ts): ["runs", key] for a project's list (invalidated on any
 * run.state), ["run", id, "detail"] for the row + outputs + context, and
 * ["run", id, "activities"] for the transcript (streamed activities are appended/merged
 * into this cache without a refetch).
 */
import { useQuery } from "@tanstack/react-query";

import { runsApi } from "./client";

export const runKeys = {
  list: (projectKey: string) => ["runs", projectKey] as const,
  detail: (id: string) => ["run", id, "detail"] as const,
  activities: (id: string) => ["run", id, "activities"] as const,
};

/**
 * The project's runs, unfiltered — filter chips and saved views are applied client-side so
 * a chip toggle is instant and never refetches (the same §13 rule that makes the verbosity
 * switch instant). Also what "≥4 runs in flight" and the never-had-any empty state read.
 */
export function useRunsQuery(projectKey: string) {
  return useQuery({
    queryKey: runKeys.list(projectKey),
    queryFn: ({ signal }) => runsApi.list(projectKey, signal),
  });
}

export function useRunDetailQuery(id: string, enabled = true) {
  return useQuery({
    queryKey: runKeys.detail(id),
    queryFn: ({ signal }) => runsApi.get(id, signal),
    enabled,
  });
}

export function useRunActivitiesQuery(id: string, enabled = true) {
  return useQuery({
    queryKey: runKeys.activities(id),
    queryFn: ({ signal }) => runsApi.activities(id, signal),
    enabled,
  });
}
