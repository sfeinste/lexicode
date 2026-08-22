/*
 * Shared TanStack Query definitions for agents (S16). One key family — ["agents", key] for a
 * project's roster, ["agent", id] for one agent, ["agent", id, "directives"] for the version
 * list — so the roster, the detail screen, the board's delegate picker and the mention
 * autocomplete invalidate together.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  agentsApi,
  type CreateAgentRequest,
  type SaveDirectiveRequest,
  type UpdateAgentRequest,
} from "./client";

export const agentKeys = {
  list: (projectKey: string) => ["agents", projectKey] as const,
  detail: (id: string) => ["agent", id] as const,
  directives: (id: string) => ["agent", id, "directives"] as const,
};

export function useAgentsQuery(projectKey: string) {
  return useQuery({
    queryKey: agentKeys.list(projectKey),
    queryFn: ({ signal }) => agentsApi.list(projectKey, undefined, signal),
  });
}

/** The delegate-eligible subset (enabled, non-archived) — derived client-side from the same
 * cached roster so a toggle updates every picker without a second fetch. */
export function useEligibleAgents(projectKey: string) {
  const q = useAgentsQuery(projectKey);
  return {
    ...q,
    agents: (q.data?.agents ?? []).filter((a) => a.enabled && a.archived_at === null),
  };
}

export function useAgentQuery(id: string) {
  return useQuery({
    queryKey: agentKeys.detail(id),
    queryFn: ({ signal }) => agentsApi.get(id, signal),
  });
}

export function useDirectivesQuery(id: string) {
  return useQuery({
    queryKey: agentKeys.directives(id),
    queryFn: ({ signal }) => agentsApi.directives(id, signal),
  });
}

export function useCreateAgent(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateAgentRequest) => agentsApi.create(projectKey, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: agentKeys.list(projectKey) }),
  });
}

export function useStarterRoster(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => agentsApi.starter(projectKey),
    onSuccess: () => void qc.invalidateQueries({ queryKey: agentKeys.list(projectKey) }),
  });
}

export function useUpdateAgent(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateAgentRequest }) =>
      agentsApi.update(id, body),
    onSuccess: (agent) => {
      qc.setQueryData(agentKeys.detail(agent.id), agent);
      void qc.invalidateQueries({ queryKey: agentKeys.list(projectKey) });
    },
  });
}

export function useSaveDirective(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: SaveDirectiveRequest) => agentsApi.saveDirective(id, body),
    onSuccess: (res) => {
      if (res.created) {
        void qc.invalidateQueries({ queryKey: agentKeys.directives(id) });
        void qc.invalidateQueries({ queryKey: agentKeys.detail(id) });
      }
    },
  });
}
