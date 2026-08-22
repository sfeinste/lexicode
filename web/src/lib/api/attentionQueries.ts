/*
 * The attention queries (S24; architecture §12): the workspace-wide needs-you rows behind
 * the home strip, the left rail and /inbox — one query, three renderings — and the
 * notifications behind the inbox badge and its dropdown. Key families ["inbox"] and
 * ["notifications"] are what the SSE reducer invalidates on notification.updated; a modest
 * refetch interval covers run-state changes that arrive on run topics this tab is not
 * subscribed to.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { inboxApi, notificationsApi } from "./client";

export const attentionKeys = {
  inbox: ["inbox"] as const,
  notifications: ["notifications"] as const,
};

/** The cross-project needs-you rows (question → approval → failure, oldest first). */
export function useInboxQuery() {
  return useQuery({
    queryKey: attentionKeys.inbox,
    queryFn: ({ signal }) => inboxApi.get(signal),
    refetchInterval: 15_000,
  });
}

/** The caller's notifications and the unread badge count. */
export function useNotificationsQuery() {
  return useQuery({
    queryKey: attentionKeys.notifications,
    queryFn: ({ signal }) => notificationsApi.list(signal),
    refetchInterval: 30_000,
  });
}

export function useMarkNotificationRead() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => notificationsApi.read(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: attentionKeys.notifications });
    },
  });
}

export function useDismissNotification() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => notificationsApi.dismiss(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: attentionKeys.notifications });
    },
  });
}
