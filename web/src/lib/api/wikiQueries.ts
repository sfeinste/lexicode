/*
 * Shared TanStack Query definitions for the wiki (S33). Key families: ["wiki", key] for a
 * project's tree payload, ["wikiPage", id] for one page's detail (backlinks included),
 * ["wikiSearch", key, q] for search results — the tree, the page view, mention autocomplete
 * and the tag index all invalidate together on a save.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  wikiApi,
  type CreateWikiPageRequest,
  type DocChoice,
  type UpdateWikiPageRequest,
} from "./client";

export const wikiKeys = {
  list: (projectKey: string) => ["wiki", projectKey] as const,
  detail: (id: string) => ["wikiPage", id] as const,
  search: (projectKey: string, q: string) => ["wikiSearch", projectKey, q] as const,
};

export function useWikiListQuery(projectKey: string) {
  return useQuery({
    queryKey: wikiKeys.list(projectKey),
    queryFn: ({ signal }) => wikiApi.list(projectKey, signal),
  });
}

export function useWikiPageQuery(id: string | undefined) {
  return useQuery({
    queryKey: wikiKeys.detail(id ?? "missing"),
    queryFn: ({ signal }) => wikiApi.get(id ?? "", signal),
    enabled: id !== undefined,
  });
}

export function useWikiSearchQuery(projectKey: string, q: string) {
  return useQuery({
    queryKey: wikiKeys.search(projectKey, q),
    queryFn: ({ signal }) => wikiApi.search(projectKey, q, signal),
    enabled: q.trim() !== "",
    placeholderData: (prev) => prev, // keep results steady while the next keystroke fetches
  });
}

export function useCreateWikiPage(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateWikiPageRequest) => wikiApi.create(projectKey, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) }),
  });
}

export function useUpdateWikiPage(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateWikiPageRequest }) =>
      wikiApi.update(id, body),
    onSuccess: (_page, { id }) => {
      void qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) });
      void qc.invalidateQueries({ queryKey: wikiKeys.detail(id) });
      // A rename rewrites labels (and backlink paragraphs) in OTHER pages — every cached
      // detail may be stale, so drop the whole family rather than guessing which.
      void qc.invalidateQueries({ queryKey: ["wikiPage"] });
    },
  });
}

export function useArchiveWikiPage(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => wikiApi.archive(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) });
      void qc.invalidateQueries({ queryKey: ["wikiPage"] });
    },
  });
}

// ---- the S35 proposal review verbs ----------------------------------------------------

export function useAcceptProposal(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => wikiApi.accept(id),
    onSuccess: () => {
      // Accepting reshapes the tree (a page went live / a target changed) and retires the
      // proposal — refresh the family plus the needs-you surfaces its row sat on.
      void qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) });
      void qc.invalidateQueries({ queryKey: ["wikiPage"] });
      void qc.invalidateQueries({ queryKey: ["inbox"] });
    },
  });
}

export function useDismissProposal(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => wikiApi.dismiss(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) });
      void qc.invalidateQueries({ queryKey: ["wikiPage"] });
      void qc.invalidateQueries({ queryKey: ["inbox"] });
    },
  });
}

// ---- the S35 repo import --------------------------------------------------------------

export function useWikiImportPreview(projectKey: string, open: boolean) {
  return useQuery({
    queryKey: ["wikiImportPreview", projectKey] as const,
    queryFn: () => wikiApi.importPreview(projectKey),
    enabled: open,
    // Always re-detect when the dialog opens: the marks must reflect this moment's pages.
    staleTime: 0,
    gcTime: 0,
  });
}

export function useWikiImport(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (files: DocChoice[]) => wikiApi.import(projectKey, files),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) });
      void qc.invalidateQueries({ queryKey: ["wikiImportPreview", projectKey] });
    },
  });
}
