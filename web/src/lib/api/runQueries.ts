/*
 * Shared TanStack Query definitions for runs (S23). Key families match what the SSE
 * reducer touches (applyEvent.ts): ["runs", key] for a project's list (invalidated on any
 * run.state), ["run", id, "detail"] for the row + outputs + context, and
 * ["run", id, "activities"] for the transcript (streamed activities are appended/merged
 * into this cache without a refetch).
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  elicitationsApi,
  runsApi,
  type RespondElicitationRequest,
} from "./client";

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

// ---- S24 intervention mutations ---------------------------------------------------------

/** Queue a steering message; the detail refetch shows the queued chip immediately, and the
 * run.message SSE frame flips it to delivered. */
export function useSteerRun(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: string) => runsApi.steer(id, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: runKeys.detail(id) });
    },
  });
}

/** Stop the run: terminal canceled, §10.5 artifact push preserved. */
export function useStopRun(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (reason: string) => runsApi.stop(id, reason),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["run", id] });
      void qc.invalidateQueries({ queryKey: ["runs"] });
    },
  });
}

/** Take over: stop + note + the copy-paste checkout block (§10.7). */
export function useTakeoverRun(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (note: string) => runsApi.takeover(id, note),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["run", id] });
      void qc.invalidateQueries({ queryKey: ["runs"] });
    },
  });
}

/** Answer / approve / deny / approve-with-edits / remember a blocked elicitation. */
export function useRespondElicitation(runID: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: RespondElicitationRequest }) =>
      elicitationsApi.respond(id, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["run", runID] });
      void qc.invalidateQueries({ queryKey: ["runs"] });
      // S36: answering happens from the home card and the inbox row too — the needs-you
      // surfaces must drop the row without waiting for the refetch interval.
      void qc.invalidateQueries({ queryKey: ["inbox"] });
    },
  });
}

/** Dismiss a terminal failure from the needs-you surfaces. */
export function useAcknowledgeRun(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => runsApi.acknowledge(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["run", id] });
      void qc.invalidateQueries({ queryKey: ["runs"] });
      void qc.invalidateQueries({ queryKey: ["inbox"] });
    },
  });
}
