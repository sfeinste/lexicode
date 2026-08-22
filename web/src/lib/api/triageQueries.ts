/*
 * The triage queries (S31). One query family, ["triage", key], feeds both the queue page
 * and the tab badge (ProjectLayout renders `pending_count` — actionable items only, per UI
 * spec §2.1; snoozed items never count). The SSE reducer invalidates the family on every
 * triage.* event; a modest refetch interval covers tabs without a live stream.
 *
 * Every verb invalidates the queue AND the board/ticket families: accept makes a ticket
 * board-visible, duplicate/decline archive one, and a merge rewrites the survivor's
 * stream, labels and criteria.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { triageApi } from "./client";

export const triageKeys = {
  list: (projectKey: string) => ["triage", projectKey] as const,
};

/** The queue: unresolved items (pending first, then snoozed) + the badge counts. */
export function useTriageQuery(projectKey: string) {
  return useQuery({
    queryKey: triageKeys.list(projectKey),
    queryFn: ({ signal }) => triageApi.list(projectKey, signal),
    refetchInterval: 30_000,
  });
}

function useTriageInvalidation(projectKey: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: triageKeys.list(projectKey) });
    void qc.invalidateQueries({ queryKey: ["board", projectKey] });
    void qc.invalidateQueries({ queryKey: ["ticket", projectKey] });
  };
}

export function useTriageAccept(projectKey: string) {
  const invalidate = useTriageInvalidation(projectKey);
  return useMutation({
    mutationFn: (id: string) => triageApi.accept(id),
    onSettled: invalidate,
  });
}

export function useTriageDuplicate(projectKey: string) {
  const invalidate = useTriageInvalidation(projectKey);
  return useMutation({
    mutationFn: ({ id, ofTicketId }: { id: string; ofTicketId: string }) =>
      triageApi.duplicate(id, { of_ticket_id: ofTicketId }),
    onSettled: invalidate,
  });
}

export function useTriageDecline(projectKey: string) {
  const invalidate = useTriageInvalidation(projectKey);
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason?: string }) =>
      triageApi.decline(id, { reason }),
    onSettled: invalidate,
  });
}

export function useTriageSnooze(projectKey: string) {
  const invalidate = useTriageInvalidation(projectKey);
  return useMutation({
    mutationFn: ({ id, until }: { id: string; until: string | null }) =>
      triageApi.snooze(id, { until }),
    onSettled: invalidate,
  });
}
