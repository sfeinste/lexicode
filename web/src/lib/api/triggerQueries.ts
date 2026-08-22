/*
 * Shared TanStack Query definitions for triggers (S29). Key families:
 * ["triggers", key] for a project's rule list, ["trigger", id] for one rule,
 * ["trigger", id, "firings"] for its history, ["trigger-catalog", key] for the editor
 * catalog (sources + actions + operators — changes only on server restart, so it is
 * cached long), and ["run", id, "chain"] for the loop chain view.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { runsApi, triggersApi, type TriggerInput } from "./client";

export const triggerKeys = {
  list: (projectKey: string) => ["triggers", projectKey] as const,
  detail: (id: string) => ["trigger", id] as const,
  firings: (id: string) => ["trigger", id, "firings"] as const,
  catalog: (projectKey: string) => ["trigger-catalog", projectKey] as const,
};

export function useTriggersQuery(projectKey: string) {
  return useQuery({
    queryKey: triggerKeys.list(projectKey),
    queryFn: ({ signal }) => triggersApi.list(projectKey, signal),
  });
}

export function useTriggerQuery(id: string, enabled = true) {
  return useQuery({
    queryKey: triggerKeys.detail(id),
    queryFn: ({ signal }) => triggersApi.get(id, signal),
    enabled,
  });
}

export function useTriggerFiringsQuery(id: string, enabled = true) {
  return useQuery({
    queryKey: triggerKeys.firings(id),
    queryFn: ({ signal }) => triggersApi.firings(id, 50, signal),
    enabled,
  });
}

/** The editor catalog: static per server process, so cache it for the session. */
export function useTriggerCatalogQuery(projectKey: string) {
  return useQuery({
    queryKey: triggerKeys.catalog(projectKey),
    queryFn: ({ signal }) => triggersApi.catalog(projectKey, signal),
    staleTime: 5 * 60_000,
  });
}

export function useCreateTrigger(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: TriggerInput) => triggersApi.create(projectKey, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: triggerKeys.list(projectKey) }),
  });
}

export function useUpdateTrigger(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: TriggerInput }) =>
      triggersApi.update(id, body),
    onSuccess: (_tr, { id }) => {
      void qc.invalidateQueries({ queryKey: triggerKeys.detail(id) });
      void qc.invalidateQueries({ queryKey: triggerKeys.list(projectKey) });
    },
  });
}

export function useRunChainQuery(runId: string | null) {
  return useQuery({
    queryKey: ["run", runId, "chain"] as const,
    queryFn: ({ signal }) => runsApi.chain(runId as string, signal),
    enabled: runId !== null,
  });
}
