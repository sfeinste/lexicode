/*
 * Shared TanStack Query definitions for board columns (S09). One key per project's column
 * list; every mutation invalidates it, so the settings screen (and later the board, S11)
 * re-render from the server's ordering — positions are server-assigned, never client-guessed.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { columnsApi, type CreateColumnRequest, type UpdateColumnRequest } from "./client";

export const columnKeys = {
  list: (projectKey: string) => ["columns", "list", projectKey] as const,
};

export function useColumnsQuery(projectKey: string) {
  return useQuery({
    queryKey: columnKeys.list(projectKey),
    queryFn: ({ signal }) => columnsApi.list(projectKey, signal),
  });
}

export function useCreateColumn(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateColumnRequest) => columnsApi.create(projectKey, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: columnKeys.list(projectKey) }),
  });
}

export function useUpdateColumn(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateColumnRequest }) =>
      columnsApi.update(id, body),
    onSuccess: () => void qc.invalidateQueries({ queryKey: columnKeys.list(projectKey) }),
  });
}

export function useDeleteColumn(projectKey: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, destinationColumnId }: { id: string; destinationColumnId?: string }) =>
      columnsApi.remove(id, destinationColumnId),
    onSuccess: () => void qc.invalidateQueries({ queryKey: columnKeys.list(projectKey) }),
  });
}
