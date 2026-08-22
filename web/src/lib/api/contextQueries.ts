/*
 * Shared TanStack Query definitions for the S34 context surfaces: the project's context
 * budget (the ContextMeter's numbers, on all three of its surfaces) and the agent detail's
 * dry context preview. Both are read-only views over the one resolver.
 */
import { useQuery } from "@tanstack/react-query";

import { contextApi } from "./client";

export const contextKeys = {
  budget: (projectKey: string) => ["contextBudget", projectKey] as const,
  preview: (agentId: string) => ["contextPreview", agentId] as const,
};

export function useContextBudgetQuery(projectKey: string) {
  return useQuery({
    queryKey: contextKeys.budget(projectKey),
    queryFn: ({ signal }) => contextApi.budget(projectKey, signal),
  });
}

export function useAgentContextPreviewQuery(agentId: string | undefined) {
  return useQuery({
    queryKey: contextKeys.preview(agentId ?? "missing"),
    queryFn: ({ signal }) => contextApi.agentPreview(agentId ?? "", signal),
    enabled: agentId !== undefined,
  });
}
