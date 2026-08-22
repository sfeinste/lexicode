/*
 * Shared TanStack Query definitions for projects (S08). One key shape everywhere so the rail,
 * Home and the settings screens invalidate each other with a single prefix.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  projectsApi,
  workspaceApi,
  type UpdateProjectRequest,
  type UpdateWorkspaceSettingsRequest,
} from "./client";

export const projectKeys = {
  all: ["projects"] as const,
  list: (archived: boolean) => ["projects", "list", { archived }] as const,
  detail: (key: string) => ["projects", "detail", key] as const,
  overview: (key: string) => ["projects", "overview", key] as const,
  budget: (key: string) => ["projects", "budget", key] as const,
  counts: (key: string) => ["projects", "counts", key] as const,
};

export function useProjectsQuery(opts?: { archived?: boolean }) {
  const archived = opts?.archived ?? false;
  return useQuery({
    queryKey: projectKeys.list(archived),
    queryFn: ({ signal }) => projectsApi.list({ archived }, signal),
  });
}

export function useProjectQuery(key: string) {
  return useQuery({
    queryKey: projectKeys.detail(key),
    queryFn: ({ signal }) => projectsApi.get(key, signal),
  });
}

export function useProjectOverviewQuery(key: string) {
  return useQuery({
    queryKey: projectKeys.overview(key),
    queryFn: ({ signal }) => projectsApi.overview(key, signal),
  });
}

/**
 * The S37 budget standing: header chip + exhaustion banner. Refetched on an interval so the
 * banner appears without a navigation once the scheduler starts failing runs.
 */
export function useProjectBudgetQuery(key: string) {
  return useQuery({
    queryKey: projectKeys.budget(key),
    queryFn: ({ signal }) => projectsApi.budget(key, signal),
    refetchInterval: 30_000,
  });
}

export function useProjectCountsQuery(key: string, enabled = true) {
  return useQuery({
    queryKey: projectKeys.counts(key),
    queryFn: ({ signal }) => projectsApi.counts(key, signal),
    enabled,
  });
}

export function useDeleteProject(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (confirm: string) => projectsApi.remove(key, confirm),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: projectKeys.all });
    },
  });
}

export function useUpdateProject(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateProjectRequest) => projectsApi.update(key, body),
    onSuccess: (project) => {
      qc.setQueryData(projectKeys.detail(key), project);
      void qc.invalidateQueries({ queryKey: projectKeys.all });
    },
  });
}

export const workspaceSettingsKey = ["workspace", "settings"] as const;

export function useWorkspaceSettingsQuery(enabled = true) {
  return useQuery({
    queryKey: workspaceSettingsKey,
    queryFn: ({ signal }) => workspaceApi.settings(signal),
    enabled,
  });
}

export function useUpdateWorkspaceSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateWorkspaceSettingsRequest) => workspaceApi.update(body),
    onSuccess: (ws) => {
      qc.setQueryData(workspaceSettingsKey, ws);
      // Inherited project values may have followed the workspace change.
      void qc.invalidateQueries({ queryKey: projectKeys.all });
    },
  });
}
