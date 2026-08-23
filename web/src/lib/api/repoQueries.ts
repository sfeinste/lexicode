/*
 * TanStack Query definitions for repo connect and bootstrap (S15). Connect/disconnect and a
 * bootstrap apply invalidate broadly: they change the About card, the board, the wiki, the
 * agents and the triggers in one stroke.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  bootstrapApi,
  repoApi,
  type BootstrapApplyRequest,
  type ConnectRepoRequest,
  type UpdateRepoNetworkRequest,
  type UpdateRepoSettingsRequest,
} from "./client";
import { projectKeys } from "./projectQueries";

export const repoKeys = {
  status: (key: string) => ["repo", "status", key] as const,
  preview: (key: string) => ["bootstrap", "preview", key] as const,
};

export function useRepoStatusQuery(key: string) {
  return useQuery({
    queryKey: repoKeys.status(key),
    queryFn: ({ signal }) => repoApi.status(key, signal),
  });
}

export function useConnectRepo(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: ConnectRepoRequest) => repoApi.connect(key, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoKeys.status(key) });
      void qc.invalidateQueries({ queryKey: projectKeys.overview(key) });
    },
  });
}

export function useDisconnectRepo(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => repoApi.disconnect(key),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoKeys.status(key) });
      void qc.invalidateQueries({ queryKey: projectKeys.overview(key) });
    },
  });
}

/** Network policy + allowlist (S18). Only the repo status card depends on these columns. */
export function useUpdateRepoNetwork(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateRepoNetworkRequest) => repoApi.updateNetwork(key, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoKeys.status(key) });
    },
  });
}

/**
 * Setup script + branch template. Same shape as the network mutation: only the repo status
 * card reads these columns.
 */
export function useUpdateRepoSettings(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateRepoSettingsRequest) => repoApi.updateSettings(key, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: repoKeys.status(key) });
    },
  });
}

/**
 * The scan is a POST (it reads the forge with stored credentials) but writes nothing, so it
 * is safe to model as a query — re-running it is exactly the "Re-scan repository" action.
 */
export function useBootstrapPreviewQuery(key: string, enabled = true) {
  return useQuery({
    queryKey: repoKeys.preview(key),
    queryFn: () => bootstrapApi.preview(key),
    enabled,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
}

export function useBootstrapApply(key: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: BootstrapApplyRequest) => bootstrapApi.apply(key, body),
    onSuccess: () => {
      // The apply may have touched tickets, wiki, triggers, agents and the description.
      void qc.invalidateQueries({ queryKey: projectKeys.all });
      void qc.invalidateQueries({ queryKey: ["tickets"] });
      void qc.invalidateQueries({ queryKey: repoKeys.status(key) });
      void qc.invalidateQueries({ queryKey: repoKeys.preview(key) });
    },
  });
}
